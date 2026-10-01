package flowpilot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func executable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseConfig(t *testing.T, workload string) Config {
	return Config{Version: 1, Identity: "test-config", RunDirectory: t.TempDir(),
		Workload: WorkloadConfig{Path: workload, Version: "1"},
		Policy:   PolicyConfig{MaxOutputBytes: 4096}}
}

func TestLifecycleAndPostmortemAfterWorkloadFailure(t *testing.T) {
	prepare := executable(t, `printf '{"version":1,"ok":true,"request_identity":"req-1","output":{"name":"demo"}}'`)
	workload := executable(t, `cat >/dev/null; echo workload-diagnostic >&2; exit 7`)
	post := executable(t, `cat >/dev/null; printf '{"version":1,"ok":true,"output":{"reported":true}}'`)
	cfg := baseConfig(t, workload)
	cfg.Prepare = &ConnectorConfig{Path: prepare, Version: "1"}
	cfg.Postmortem = &ConnectorConfig{Path: post, Version: "1"}
	record, err := (Runner{Config: cfg, Connect: ConnectorRunner{MaxOutputBytes: 4096}}).Run(context.Background(), map[string]any{"request": "business data"})
	if err == nil || record.Status != "failed" || record.RequestIdentity != "req-1" {
		t.Fatalf("expected failed run with request identity, record=%+v err=%v", record, err)
	}
	if record.Phases["postmortem"].Status != "succeeded" || record.Phases["execute"].Error.Kind != "workload" {
		t.Fatalf("expected postmortem and workload distinction: %+v", record.Phases)
	}
}

func TestValidationPrecedesWorkloadInvocation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	workload := executable(t, "touch "+marker)
	cfg := baseConfig(t, workload)
	_, err := (Runner{Config: cfg}).Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "prepared inputs") {
		t.Fatalf("expected input validation error, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("workload was invoked before validation")
	}
}

func TestRunRecordRedactsSensitiveOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	record := &RunRecord{Version: 1, RunID: "run-1", ConfigurationID: "cfg", Phases: map[string]Outcome{
		"prepare": {Output: map[string]any{"token": "secret-value", "safe": "value"}},
	}}
	if err := record.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(path, "run-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) != nil || strings.Contains(string(data), "secret-value") {
		t.Fatalf("record leaked secret: %s", data)
	}
}
