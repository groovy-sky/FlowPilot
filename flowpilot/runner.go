package flowpilot

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Runner struct {
	Config  Config
	Connect ConnectorRunner
	Now     func() time.Time
}

func (r Runner) Run(ctx context.Context, request any) (*RunRecord, error) {
	if err := r.Config.Validate(); err != nil {
		return nil, err
	}
	if err := r.Config.ValidateExecutables(); err != nil {
		return nil, err
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	id, err := newRunID()
	if err != nil {
		return nil, err
	}
	record := &RunRecord{Version: 1, RunID: id, ConfigurationID: r.Config.Identity, StartedAt: now(), Status: "running", Phases: map[string]Outcome{}}
	defer func() { finished := now(); record.FinishedAt = &finished; _ = record.Save(r.Config.RunDirectory) }()

	prepared := request
	if r.Config.Prepare != nil {
		out, phaseErr := r.runConnector(ctx, record, "prepare", *r.Config.Prepare, request, now)
		if phaseErr != nil {
			record.Status = "failed"
			r.runPostmortem(ctx, record, prepared, now)
			return record, phaseErr
		}
		prepared = out.Output
		if out.RequestID != "" {
			record.RequestIdentity = out.RequestID
		}
	}
	if err := validatePrepared(prepared); err != nil {
		record.Phases["execute"] = failedOutcome("validation", err, now)
		record.Status = "failed"
		r.runPostmortem(ctx, record, prepared, now)
		return record, err
	}
	workErr := r.runWorkload(ctx, record, prepared, now)
	if workErr != nil {
		record.Status = "failed"
		r.runPostmortem(ctx, record, prepared, now)
		return record, workErr
	}
	postErr := r.runPostmortem(ctx, record, prepared, now)
	if postErr != nil {
		record.Status = "failed"
		return record, postErr
	}
	record.Status = "succeeded"
	return record, nil
}

func validatePrepared(v any) error {
	if v == nil {
		return fmt.Errorf("prepared inputs are required")
	}
	return nil
}

func (r Runner) runConnector(ctx context.Context, record *RunRecord, phase string, cfg ConnectorConfig, request any, now func() time.Time) (ConnectorResult, error) {
	start := now()
	timeout, err := r.Config.timeout(cfg.Timeout)
	if err != nil {
		return ConnectorResult{}, err
	}
	result, diagnostics, runErr := r.Connect.Run(ctx, cfg, ConnectorInput{Version: ProtocolVersion, Phase: phase, RunID: record.RunID, Request: request}, timeout)
	end := now()
	outcome := Outcome{Status: "succeeded", StartedAt: start, FinishedAt: end, Connector: cfg.Path, ConnectorVer: cfg.Version, Output: sanitize(result.Output), Diagnostics: redactDiagnostics(diagnostics)}
	if runErr != nil || !result.OK {
		if runErr == nil {
			runErr = fmt.Errorf("%s connector reported failure", phase)
		}
		e := StructuredError{Kind: "connector", Message: runErr.Error(), Phase: phase}
		outcome.Status = "failed"
		outcome.Error = &e
		record.Errors = append(record.Errors, e)
	}
	record.Phases[phase] = outcome
	return result, runErr
}

func (r Runner) runWorkload(ctx context.Context, record *RunRecord, input any, now func() time.Time) error {
	start := now()
	timeout, err := r.Config.timeout(r.Config.Workload.Timeout)
	if err != nil {
		record.Phases["execute"] = failedOutcome("validation", err, now)
		return err
	}
	data, err := marshalInput(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Config.Workload.Path, r.Config.Workload.Args...)
	cmd.Stdin = strings.NewReader(data)
	out, runErr := cmd.CombinedOutput()
	end := now()
	outcome := Outcome{Status: "succeeded", StartedAt: start, FinishedAt: end, Connector: r.Config.Workload.Path, ConnectorVer: r.Config.Workload.Version, Diagnostics: redactDiagnostics(splitDiagnostic(string(out)))}
	if ctx.Err() != nil {
		runErr = fmt.Errorf("workload timeout: %w", ctx.Err())
	}
	if runErr != nil {
		code := 1
		if e, ok := runErr.(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		record.WorkloadExitCode = &code
		e := StructuredError{Kind: "workload", Message: runErr.Error(), Phase: "execute"}
		outcome.Status = "failed"
		outcome.Error = &e
		record.Errors = append(record.Errors, e)
	}
	record.Phases["execute"] = outcome
	return runErr
}

func (r Runner) runPostmortem(ctx context.Context, record *RunRecord, input any, now func() time.Time) error {
	if r.Config.Postmortem == nil {
		return nil
	}
	_, err := r.runConnector(ctx, record, "postmortem", *r.Config.Postmortem, input, now)
	return err
}

func failedOutcome(kind string, err error, now func() time.Time) Outcome {
	t := now()
	e := StructuredError{Kind: kind, Message: err.Error(), Phase: "execute"}
	return Outcome{Status: "failed", StartedAt: t, FinishedAt: t, Error: &e}
}
func marshalInput(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }
func splitDiagnostic(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []string{s}
}
func redactDiagnostics(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redactString(s)
	}
	return out
}
func redactString(s string) string {
	for _, key := range []string{"password", "token", "secret", "credential", "authorization"} {
		lower := strings.ToLower(s)
		if i := strings.Index(lower, key); i >= 0 {
			if end := strings.IndexAny(s[i:], "\r\n, "); end >= 0 {
				s = s[:i] + key + "=[REDACTED]" + s[i+end:]
			} else {
				s = s[:i] + key + "=[REDACTED]"
			}
		}
	}
	return s
}
func sanitize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, value := range x {
			lower := strings.ToLower(k)
			if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") {
				out[k] = "[REDACTED]"
			} else {
				out[k] = sanitize(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = sanitize(x[i])
		}
		return out
	case string:
		return redactString(x)
	default:
		return v
	}
}
