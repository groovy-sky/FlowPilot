package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandWithoutConfiguration(t *testing.T) {
	runDir := t.TempDir()
	getenv := func(k string) string { return map[string]string{"FLOWPILOT_RUN_DIR": runDir}[k] }
	var stdout, stderr bytes.Buffer
	code := run([]string{"-no-config", "--", "sh", "-c", "echo hello; exit 4"}, getenv, strings.NewReader(""), &stdout, &stderr)
	if code != 4 || stdout.String() != "hello\n" || !strings.Contains(stderr.String(), "record: "+runDir) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	records, _ := filepath.Glob(filepath.Join(runDir, "*.json"))
	logs, _ := filepath.Glob(filepath.Join(runDir, "*.log"))
	if len(records) != 1 || len(logs) != 2 {
		t.Fatalf("expected one record and two logs, got %v %v", records, logs)
	}
}

func TestRunCommandUsesConfigFile(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "configured-runs")
	config := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(config, []byte(`{"identity":"custom","run_directory":"`+runDir+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string { return map[string]string{"FLOWPILOT_CONFIG": config}[k] }
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-quiet", "--", "true"}, getenv, nil, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	data, _ := filepath.Glob(filepath.Join(runDir, "*.json"))
	if len(data) != 1 {
		t.Fatalf("expected a record in configured run directory, got %v", data)
	}
	if content, _ := os.ReadFile(data[0]); !strings.Contains(string(content), `"configuration_id": "custom"`) {
		t.Fatalf("record did not use configuration: %s", content)
	}
}

func TestRunUsageErrors(t *testing.T) {
	getenv := func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	if code := run(nil, getenv, strings.NewReader(""), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "-config is required") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"-request", "r.json", "--", "true"}, getenv, nil, &stdout, &stderr); code != 125 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
