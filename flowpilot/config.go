package flowpilot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is trusted operator configuration. Request data never changes it.
type Config struct {
	Version       int              `json:"version"`
	Identity      string           `json:"identity"`
	RunDirectory  string           `json:"run_directory"`
	Prepare       *ConnectorConfig `json:"prepare,omitempty"`
	Workload      WorkloadConfig   `json:"workload"`
	Postmortem    *ConnectorConfig `json:"postmortem,omitempty"`
	Policy        PolicyConfig     `json:"policy"`
	ConnectorTime time.Duration    `json:"-"`
}

type ConnectorConfig struct {
	Path    string   `json:"path"`
	Version string   `json:"version"`
	Timeout string   `json:"timeout,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type WorkloadConfig struct {
	Path    string   `json:"path"`
	Version string   `json:"version"`
	Args    []string `json:"args,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
}

type PolicyConfig struct {
	MaxOutputBytes int64 `json:"max_output_bytes"`
	AllowRetry     bool  `json:"allow_retry"`
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("config.version must be 1")
	}
	if strings.TrimSpace(c.Identity) == "" {
		return errors.New("config.identity is required")
	}
	if strings.TrimSpace(c.RunDirectory) == "" {
		return errors.New("config.run_directory is required")
	}
	if strings.TrimSpace(c.Workload.Path) == "" {
		return errors.New("workload.path is required")
	}
	if strings.TrimSpace(c.Workload.Version) == "" {
		return errors.New("workload.version is required")
	}
	if filepath.IsAbs(c.Workload.Path) == false {
		return errors.New("workload.path must be absolute")
	}
	if c.Prepare != nil {
		if err := c.Prepare.validate("prepare"); err != nil {
			return err
		}
	}
	if c.Postmortem != nil {
		if err := c.Postmortem.validate("postmortem"); err != nil {
			return err
		}
	}
	if c.Policy.MaxOutputBytes <= 0 {
		return errors.New("policy.max_output_bytes must be positive")
	}
	return nil
}

func (c ConnectorConfig) validate(name string) error {
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("%s.path is required", name)
	}
	if !filepath.IsAbs(c.Path) {
		return fmt.Errorf("%s.path must be absolute", name)
	}
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("%s.version is required", name)
	}
	return nil
}

func (c Config) timeout(value string) (time.Duration, error) {
	if value == "" {
		return 5 * time.Minute, nil
	}
	return time.ParseDuration(value)
}

func (c Config) ValidateExecutables() error {
	paths := map[string]string{"workload": c.Workload.Path}
	if c.Prepare != nil {
		paths["prepare"] = c.Prepare.Path
	}
	if c.Postmortem != nil {
		paths["postmortem"] = c.Postmortem.Path
	}
	for name, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s executable: %w", name, err)
		}
	}
	return nil
}
