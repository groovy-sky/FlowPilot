package flowpilot

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func commandConfig(t *testing.T) Config {
	cfg := DefaultConfig()
	cfg.RunDirectory = t.TempDir()
	return cfg
}

func readRecord(t *testing.T, dir, id string) RunRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestExecWithoutConfigurationStoresRecordAndLogs(t *testing.T) {
	cfg := commandConfig(t)
	script := executable(t, `echo out; echo err >&2; exit 3`)
	var stdout, stderr bytes.Buffer
	record, err := (Runner{Config: cfg}).Exec(context.Background(), Command{Path: script, Args: []string{"--token", "abc"}, Name: "demo", Stdout: &stdout, Stderr: &stderr})
	if err == nil || record == nil || record.Status != "failed" || CommandExitCode(record, err) != 3 {
		t.Fatalf("expected failed run with exit 3, record=%+v err=%v", record, err)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("output was not mirrored: %q %q", stdout.String(), stderr.String())
	}
	stored := readRecord(t, cfg.RunDirectory, record.RunID)
	if stored.Name != "demo" || stored.FinishedAt == nil || *stored.WorkloadExitCode != 3 || stored.Command.ResolvedPath != script ||
		!reflect.DeepEqual(stored.Command.Args, []string{"--token", "[REDACTED]"}) || stored.Phases["execute"].Error.Kind != "workload" {
		t.Fatalf("unexpected stored record: %+v %+v", stored, stored.Command)
	}
	for path, want := range map[string]string{stored.Command.StdoutLog: "out\n", stored.Command.StderrLog: "err\n"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("log %s = %q, %v", path, data, err)
		}
	}
}

func TestExecSucceedsQuietly(t *testing.T) {
	cfg := commandConfig(t)
	record, err := (Runner{Config: cfg}).Exec(context.Background(), Command{Path: executable(t, `echo hidden`)})
	if err != nil || record.Status != "succeeded" || *record.WorkloadExitCode != 0 || CommandExitCode(record, err) != 0 {
		t.Fatalf("expected success, record=%+v err=%v", record, err)
	}
	if data, _ := os.ReadFile(record.Command.StdoutLog); string(data) != "hidden\n" {
		t.Fatalf("stdout log = %q", data)
	}
}

func TestExecRunsOptionalConnectors(t *testing.T) {
	cfg := commandConfig(t)
	received := filepath.Join(t.TempDir(), "postmortem.json")
	cfg.Prepare = &ConnectorConfig{Path: executable(t, `cat >/dev/null; printf '{"version":1,"ok":true,"request_identity":"req-9"}'`), Version: "1"}
	cfg.Postmortem = &ConnectorConfig{Path: executable(t, `cat >`+received+`; printf '{"version":1,"ok":true,"output":{"reported":true}}'`), Version: "1"}
	record, err := (Runner{Config: cfg, Connect: ConnectorRunner{MaxOutputBytes: 4096}}).Exec(context.Background(), Command{Path: executable(t, `exit 0`)})
	if err != nil || record.Status != "succeeded" || record.RequestIdentity != "req-9" || record.Phases["postmortem"].Status != "succeeded" {
		t.Fatalf("unexpected record: %+v err=%v", record, err)
	}
	var input struct {
		Phase   string    `json:"phase"`
		Request RunRecord `json:"request"`
	}
	data, _ := os.ReadFile(received)
	if json.Unmarshal(data, &input) != nil || input.Phase != "postmortem" || input.Request.RunID != record.RunID ||
		input.Request.WorkloadExitCode == nil || input.Request.Command.StdoutLog == "" {
		t.Fatalf("postmortem did not receive the run record: %s", data)
	}
}

func TestExecPrepareFailureSkipsCommand(t *testing.T) {
	cfg := commandConfig(t)
	marker := filepath.Join(t.TempDir(), "invoked")
	cfg.Prepare = &ConnectorConfig{Path: executable(t, `cat >/dev/null; printf '{"version":1,"ok":false}'`), Version: "1"}
	record, err := (Runner{Config: cfg, Connect: ConnectorRunner{MaxOutputBytes: 4096}}).Exec(context.Background(), Command{Path: executable(t, "touch "+marker)})
	if err == nil || record.Status != "failed" || CommandExitCode(record, err) != ExitInternal {
		t.Fatalf("expected prepare failure, record=%+v err=%v", record, err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("command ran after prepare failed")
	}
}

func TestExecLaunchFailures(t *testing.T) {
	cfg := commandConfig(t)
	notExecutable := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(notExecutable, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"flowpilot-missing-command": ExitNotFound, "/nonexistent/flowpilot": ExitNotFound, notExecutable: ExitCannotStart} {
		record, err := (Runner{Config: cfg}).Exec(context.Background(), Command{Path: path})
		if got := CommandExitCode(record, err); got != want || record.Phases["execute"].Error.Kind != "launch" {
			t.Fatalf("%s: exit %d, want %d (err=%v)", path, got, want, err)
		}
		if stored := readRecord(t, cfg.RunDirectory, record.RunID); stored.Status != "failed" {
			t.Fatalf("%s: stored status %q", path, stored.Status)
		}
	}
}

func TestExecTimeoutTerminatesCommand(t *testing.T) {
	cfg := commandConfig(t)
	cfg.Workload.Timeout = "100ms"
	record, err := (Runner{Config: cfg}).Exec(context.Background(), Command{Path: executable(t, `exec sleep 10`)})
	if err == nil || !strings.Contains(err.Error(), "timeout") || CommandExitCode(record, err) != 143 || record.Command.Signal == "" {
		t.Fatalf("expected timeout, record=%+v err=%v", record, err)
	}
}

func TestExecRejectsInvalidConfiguration(t *testing.T) {
	cfg := commandConfig(t)
	cfg.Version = 2
	if record, err := (Runner{Config: cfg}).Exec(context.Background(), Command{Path: "true"}); err == nil || record != nil {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestRedactArguments(t *testing.T) {
	got := redactArguments([]string{"--password", "p", "--api-key=k", "-token", "t", "--name", "x", "file-token.txt"})
	want := []string{"--password", "[REDACTED]", "--api-key=[REDACTED]", "-token", "[REDACTED]", "--name", "x", "file-token.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestLoadCommandConfigExtendsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowpilot.json")
	if err := os.WriteFile(path, []byte(`{"identity":"team","run_directory":"runs","policy":{"allow_retry":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadCommandConfig(path)
	if err != nil || cfg.Version != 1 || cfg.Identity != "team" || cfg.RunDirectory != "runs" || cfg.Policy.MaxOutputBytes != 1<<20 || !cfg.Policy.AllowRetry {
		t.Fatalf("unexpected config %+v %v", cfg, err)
	}
	if err := cfg.ValidateCommand(); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadCommandConfig(""); err != nil || !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Fatalf("expected defaults, got %+v %v", cfg, err)
	}
}

func TestDiscoverConfig(t *testing.T) {
	work, home := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": home}
	getenv := func(k string) string { return env[k] }
	if path, err := DiscoverConfig(getenv, work); err != nil || path != "" {
		t.Fatalf("expected no config, got %q %v", path, err)
	}
	user := filepath.Join(home, ".config", "flowpilot", "config.json")
	local := filepath.Join(work, ConfigFileName)
	for _, path := range []string{user, local} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if path, _ := DiscoverConfig(getenv, work); path != local {
		t.Fatalf("expected working directory config, got %q", path)
	}
	os.Remove(local)
	if path, _ := DiscoverConfig(getenv, work); path != user {
		t.Fatalf("expected user config, got %q", path)
	}
	env["FLOWPILOT_CONFIG"] = filepath.Join(work, "missing.json")
	if _, err := DiscoverConfig(getenv, work); err == nil {
		t.Fatal("expected error for missing FLOWPILOT_CONFIG")
	}
}

func TestResolveRunDirectory(t *testing.T) {
	env := map[string]string{"FLOWPILOT_RUN_DIR": "/env", "XDG_STATE_HOME": "/state", "HOME": "/home/u"}
	getenv := func(k string) string { return env[k] }
	cases := []struct{ flag, configured, want string }{
		{"/flag", "/cfg", "/flag"},
		{"", "/cfg", "/env"},
	}
	for _, c := range cases {
		if got := ResolveRunDirectory(c.flag, getenv, c.configured); got != c.want {
			t.Fatalf("got %q want %q", got, c.want)
		}
	}
	delete(env, "FLOWPILOT_RUN_DIR")
	if got := ResolveRunDirectory("", getenv, "/cfg"); got != "/cfg" {
		t.Fatalf("got %q", got)
	}
	if got := ResolveRunDirectory("", getenv, ""); got != filepath.Join("/state", "flowpilot", "runs") {
		t.Fatalf("got %q", got)
	}
	delete(env, "XDG_STATE_HOME")
	if got := ResolveRunDirectory("", getenv, ""); got != filepath.Join("/home/u", ".local", "state", "flowpilot", "runs") {
		t.Fatalf("got %q", got)
	}
}
