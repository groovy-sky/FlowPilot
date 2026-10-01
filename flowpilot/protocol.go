package flowpilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const ProtocolVersion = 1

type ConnectorInput struct {
	Version int    `json:"version"`
	Phase   string `json:"phase"`
	RunID   string `json:"run_id"`
	Request any    `json:"request,omitempty"`
}

type ConnectorResult struct {
	Version     int              `json:"version"`
	OK          bool             `json:"ok"`
	RequestID   string           `json:"request_identity,omitempty"`
	Output      any              `json:"output,omitempty"`
	Diagnostics []string         `json:"diagnostics,omitempty"`
	Error       *StructuredError `json:"error,omitempty"`
}

type ConnectorRunner struct {
	MaxOutputBytes int64
}

func (r ConnectorRunner) Run(ctx context.Context, cfg ConnectorConfig, input ConnectorInput, timeout time.Duration) (ConnectorResult, []string, error) {
	if input.Version != ProtocolVersion {
		return ConnectorResult{}, nil, fmt.Errorf("unsupported protocol version")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(input)
	if err != nil {
		return ConnectorResult{}, nil, err
	}
	cmd := exec.CommandContext(ctx, cfg.Path, cfg.Args...)
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return ConnectorResult{}, diagnostics(stderr.String()), fmt.Errorf("connector timeout: %w", ctx.Err())
	}
	if int64(stdout.Len()) > r.MaxOutputBytes || int64(stderr.Len()) > r.MaxOutputBytes {
		return ConnectorResult{}, diagnostics(stderr.String()), fmt.Errorf("connector output exceeded limit")
	}
	if err != nil {
		return ConnectorResult{}, diagnostics(stderr.String()), fmt.Errorf("connector process: %w", err)
	}
	var result ConnectorResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return ConnectorResult{}, diagnostics(stderr.String()), fmt.Errorf("invalid connector result: %w", err)
	}
	if result.Version != ProtocolVersion {
		return ConnectorResult{}, diagnostics(stderr.String()), fmt.Errorf("unsupported connector result version")
	}
	return result, diagnostics(stderr.String()), nil
}

func diagnostics(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []string{s}
}
