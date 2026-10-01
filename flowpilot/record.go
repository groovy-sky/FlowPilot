package flowpilot

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type RunRecord struct {
	Version          int                `json:"version"`
	RunID            string             `json:"run_id"`
	Name             string             `json:"name,omitempty"`
	RequestIdentity  string             `json:"request_identity,omitempty"`
	ConfigurationID  string             `json:"configuration_id"`
	StartedAt        time.Time          `json:"started_at"`
	FinishedAt       *time.Time         `json:"finished_at,omitempty"`
	Status           string             `json:"status"`
	Phases           map[string]Outcome `json:"phases"`
	WorkloadExitCode *int               `json:"workload_exit_code,omitempty"`
	Command          *CommandRecord     `json:"command,omitempty"`
	Diagnostics      []string           `json:"diagnostics,omitempty"`
	Errors           []StructuredError  `json:"errors,omitempty"`
}

type Outcome struct {
	Status       string           `json:"status"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   time.Time        `json:"finished_at"`
	Connector    string           `json:"connector,omitempty"`
	ConnectorVer string           `json:"connector_version,omitempty"`
	Output       any              `json:"output,omitempty"`
	Diagnostics  []string         `json:"diagnostics,omitempty"`
	Error        *StructuredError `json:"error,omitempty"`
}

// CommandRecord describes a command supplied on the command line. Arguments
// are redacted; the environment is never recorded.
type CommandRecord struct {
	Executable       string   `json:"executable"`
	ResolvedPath     string   `json:"resolved_path,omitempty"`
	Args             []string `json:"args"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	PID              int      `json:"pid,omitempty"`
	Signal           string   `json:"signal,omitempty"`
	DurationMS       *int64   `json:"duration_ms,omitempty"`
	StdoutLog        string   `json:"stdout_log"`
	StderrLog        string   `json:"stderr_log"`
}

type StructuredError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Phase   string `json:"phase,omitempty"`
}

func newRunID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (r *RunRecord) Save(dir string) error {
	if r.Version != 1 || r.RunID == "" {
		return fmt.Errorf("invalid run record")
	}
	if dir == "" {
		return fmt.Errorf("run directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	copy := *r
	for name, phase := range copy.Phases {
		phase.Output = sanitize(phase.Output)
		phase.Diagnostics = redactDiagnostics(phase.Diagnostics)
		copy.Phases[name] = phase
	}
	copy.Diagnostics = redactDiagnostics(copy.Diagnostics)
	data, err := json.MarshalIndent(&copy, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".run-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Close()
	} else {
		tmp.Close()
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, r.RunID+".json"))
}
