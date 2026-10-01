package flowpilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	if err := c.validateBase(); err != nil {
		return err
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
	return c.validateExtensions()
}

// ValidateCommand validates configuration used with a command supplied on the
// command line. The command replaces the workload path, version, and args.
func (c Config) ValidateCommand() error {
	if err := c.validateBase(); err != nil {
		return err
	}
	return c.validateExtensions()
}

func (c Config) validateBase() error {
	if c.Version != 1 {
		return fmt.Errorf("config.version must be 1")
	}
	if strings.TrimSpace(c.Identity) == "" {
		return errors.New("config.identity is required")
	}
	if strings.TrimSpace(c.RunDirectory) == "" {
		return errors.New("config.run_directory is required")
	}
	return nil
}

func (c Config) validateExtensions() error {
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
	return c.validateExecutables(true)
}

func (c Config) validateExecutables(workload bool) error {
	paths := map[string]string{}
	if workload {
		paths["workload"] = c.Workload.Path
	}
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

// ConfigFileName is the optional configuration file discovered in the working
// directory when a command is run without -config.
const ConfigFileName = "flowpilot.json"

// DefaultConfig returns the configuration used when a command is run without a
// configuration file. Optional JSON configuration is decoded on top of it.
func DefaultConfig() Config {
	return Config{Version: 1, Identity: "local", Policy: PolicyConfig{MaxOutputBytes: 1 << 20}}
}

// LoadCommandConfig returns DefaultConfig extended by the JSON file at path.
// An empty path returns the defaults.
func LoadCommandConfig(path string) (Config, error) {
	config := DefaultConfig()
	if path == "" {
		return config, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return config, nil
}

// DiscoverConfig returns the optional configuration file for command mode:
// $FLOWPILOT_CONFIG, then flowpilot.json in workingDir, then
// flowpilot/config.json in the user configuration directory. It returns an
// empty path when no file exists.
func DiscoverConfig(getenv func(string) string, workingDir string) (string, error) {
	if path := getenv("FLOWPILOT_CONFIG"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("FLOWPILOT_CONFIG: %w", err)
		}
		return path, nil
	}
	candidates := []string{filepath.Join(workingDir, ConfigFileName)}
	if dir := userConfigDir(getenv); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "flowpilot", "config.json"))
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", nil
}

// ResolveRunDirectory selects the run directory for command mode: the flag
// value, $FLOWPILOT_RUN_DIR, the configured run_directory, and finally
// $XDG_STATE_HOME/flowpilot/runs or ~/.local/state/flowpilot/runs.
func ResolveRunDirectory(flagValue string, getenv func(string) string, configured string) string {
	for _, value := range []string{flagValue, getenv("FLOWPILOT_RUN_DIR"), configured} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "flowpilot", "runs")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", "flowpilot", "runs")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "flowpilot", "runs")
	}
	return filepath.Join(os.TempDir(), "flowpilot", "runs")
}

func userConfigDir(getenv func(string) string) string {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return dir
}
