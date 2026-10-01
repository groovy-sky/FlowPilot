package flowpilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Exit codes returned for command mode failures that are not the command's own
// exit status.
const (
	ExitInternal    = 125
	ExitCannotStart = 126
	ExitNotFound    = 127
)

// ErrLaunch reports that a command could not be resolved or started.
var ErrLaunch = errors.New("launch command")

// Command is a command supplied directly by the operator. Stdout and Stderr
// receive a mirror of the output in addition to the run's log files; nil
// writers disable mirroring.
type Command struct {
	Path   string
	Args   []string
	Name   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Exec runs command as the execute phase without requiring a workload in the
// configuration. The record is stored as <run_id>.json in the run directory,
// next to <run_id>.stdout.log and <run_id>.stderr.log. Configured prepare and
// postmortem connectors receive the current run record as their request.
func (r Runner) Exec(ctx context.Context, command Command) (*RunRecord, error) {
	if err := r.Config.ValidateCommand(); err != nil {
		return nil, err
	}
	if err := r.Config.validateExecutables(false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(command.Path) == "" {
		return nil, errors.New("command is required")
	}
	var timeout time.Duration
	if r.Config.Workload.Timeout != "" {
		parsed, err := time.ParseDuration(r.Config.Workload.Timeout)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("workload.timeout must be a positive duration")
		}
		timeout = parsed
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	dir, err := filepath.Abs(r.Config.RunDirectory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	id, err := newRunID()
	if err != nil {
		return nil, err
	}
	workingDirectory, _ := os.Getwd()
	record := &RunRecord{Version: 1, RunID: id, Name: command.Name, ConfigurationID: r.Config.Identity, StartedAt: now(), Status: "running", Phases: map[string]Outcome{},
		Command: &CommandRecord{Executable: command.Path, Args: redactArguments(command.Args), WorkingDirectory: workingDirectory,
			StdoutLog: filepath.Join(dir, id+".stdout.log"), StderrLog: filepath.Join(dir, id+".stderr.log")}}
	stdoutLog, err := createLog(record.Command.StdoutLog)
	if err != nil {
		return nil, err
	}
	defer stdoutLog.Close()
	stderrLog, err := createLog(record.Command.StderrLog)
	if err != nil {
		return nil, err
	}
	defer stderrLog.Close()
	if err := record.Save(dir); err != nil {
		return nil, err
	}
	defer func() { finished := now(); record.FinishedAt = &finished; _ = record.Save(dir) }()

	postCtx := context.WithoutCancel(ctx)
	if r.Config.Prepare != nil {
		out, phaseErr := r.runConnector(ctx, record, "prepare", *r.Config.Prepare, record, now)
		if phaseErr != nil {
			record.Status = "failed"
			r.runPostmortem(postCtx, record, record, now)
			return record, phaseErr
		}
		if out.RequestID != "" {
			record.RequestIdentity = out.RequestID
		}
	}
	stdout, stderr := io.Writer(stdoutLog), io.Writer(stderrLog)
	if command.Stdout != nil {
		stdout = io.MultiWriter(command.Stdout, stdoutLog)
	}
	if command.Stderr != nil {
		stderr = io.MultiWriter(command.Stderr, stderrLog)
	}
	if workErr := r.runCommand(ctx, record, command, timeout, stdout, stderr, now); workErr != nil {
		record.Status = "failed"
		r.runPostmortem(postCtx, record, record, now)
		return record, workErr
	}
	if postErr := r.runPostmortem(postCtx, record, record, now); postErr != nil {
		record.Status = "failed"
		return record, postErr
	}
	record.Status = "succeeded"
	return record, nil
}

func (r Runner) runCommand(ctx context.Context, record *RunRecord, command Command, timeout time.Duration, stdout, stderr io.Writer, now func() time.Time) error {
	start := now()
	outcome := Outcome{Status: "succeeded", StartedAt: start}
	fail := func(kind string, err error) error {
		e := StructuredError{Kind: kind, Message: err.Error(), Phase: "execute"}
		outcome.Status = "failed"
		outcome.Error = &e
		outcome.FinishedAt = now()
		record.Errors = append(record.Errors, e)
		record.Phases["execute"] = outcome
		return err
	}
	resolved, err := exec.LookPath(command.Path)
	if err != nil {
		return fail("launch", fmt.Errorf("%w: %w", ErrLaunch, err))
	}
	record.Command.ResolvedPath = resolved
	outcome.Connector = resolved
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, resolved, command.Args...)
	cmd.Args[0] = command.Path
	cmd.Stdin, cmd.Stdout, cmd.Stderr = command.Stdin, stdout, stderr
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return fail("launch", fmt.Errorf("%w: %w", ErrLaunch, err))
	}
	record.Command.PID = cmd.Process.Pid
	runErr := cmd.Wait()
	duration := now().Sub(start).Milliseconds()
	record.Command.DurationMS = &duration
	if cmd.ProcessState != nil {
		code, signal := exitStatus(cmd.ProcessState)
		record.WorkloadExitCode = &code
		record.Command.Signal = signal
	}
	if runErr != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			runErr = fmt.Errorf("workload timeout: %w", runErr)
		} else {
			runErr = fmt.Errorf("workload canceled: %w", runErr)
		}
	}
	if runErr != nil {
		return fail("workload", runErr)
	}
	outcome.FinishedAt = now()
	record.Phases["execute"] = outcome
	return nil
}

func exitStatus(state *os.ProcessState) (int, string) {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), status.Signal().String()
	}
	return state.ExitCode(), ""
}

// CommandExitCode maps the result of Exec to a process exit code. The
// command's own non-zero status is preserved; FlowPilot failures use 125,
// 126 when the command cannot be started, and 127 when it cannot be found.
func CommandExitCode(record *RunRecord, err error) int {
	if record != nil && record.WorkloadExitCode != nil && *record.WorkloadExitCode > 0 {
		return *record.WorkloadExitCode
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, ErrLaunch) && errors.Is(err, fs.ErrNotExist):
		return ExitNotFound
	case errors.Is(err, ErrLaunch):
		return ExitCannotStart
	default:
		return ExitInternal
	}
}

func createLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func redactArguments(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out); i++ {
		name, _, hasValue := strings.Cut(out[i], "=")
		if !strings.HasPrefix(name, "-") || !sensitiveName(strings.TrimLeft(name, "-")) {
			continue
		}
		if hasValue {
			out[i] = name + "=[REDACTED]"
		} else if i+1 < len(out) {
			out[i+1] = "[REDACTED]"
			i++
		}
	}
	return out
}

func sensitiveName(name string) bool {
	lower := strings.ToLower(name)
	for _, key := range []string{"password", "token", "secret", "credential", "authorization", "api-key", "apikey"} {
		if strings.Contains(lower, key) {
			return true
		}
	}
	return false
}
