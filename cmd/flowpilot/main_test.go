package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	flowpilot "github.com/groovy-sky/FlowPilot/flowpilot"
)

func TestRunCommandWithoutConfiguration(t *testing.T) {
	runDir := t.TempDir()
	getenv := func(k string) string { return map[string]string{"FLOWPILOT_RUN_DIR": runDir}[k] }
	var stdout, stderr bytes.Buffer
	code := run([]string{"-no-config", "--", "sh", "-c", "echo hello; echo warning >&2; exit 4"}, getenv, strings.NewReader(""), &stdout, &stderr)
	if code != 4 || stdout.String() != "hello\n" || !strings.HasPrefix(stderr.String(), "warning\n") || !strings.Contains(stderr.String(), "record: "+runDir) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	records, _ := filepath.Glob(filepath.Join(runDir, "*.json"))
	logs, _ := filepath.Glob(filepath.Join(runDir, "*.log"))
	if len(records) != 1 || len(logs) != 2 {
		t.Fatalf("expected one record and two logs, got %v %v", records, logs)
	}
}

func TestRunCommandJSON(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command []string
		code    int
		stdout  string
		stderr  string
	}{
		{"success", []string{"sh", "-c", "printf 'hello\\n'; printf 'oops\\n' >&2"}, 0, "hello\n", "oops\n"},
		{"failure", []string{"sh", "-c", "printf 'out\\n'; printf 'err\\n' >&2; exit 7"}, 7, "out\n", "err\n"},
		{"binary", []string{"sh", "-c", `printf '\000\377\376\n'; printf '\200\000\r\n' >&2`}, 0, "\x00\xff\xfe\n", "\x80\x00\r\n"},
		{"multiline", []string{"sh", "-c", "i=0; while [ $i -lt 100 ]; do echo line; i=$((i+1)); done"}, 0, strings.Repeat("line\n", 100), ""},
		{"empty", []string{"true"}, 0, "", ""},
		{"launch failure", []string{filepath.Join(t.TempDir(), "missing")}, 127, "", ""},
	} {
		for _, quiet := range []bool{false, true} {
			t.Run(tt.name+"/quiet="+strconv.FormatBool(quiet), func(t *testing.T) {
				runDir := t.TempDir()
				args := []string{"-no-config", "-run-dir", runDir, "-result-json"}
				if quiet {
					args = append(args, "-quiet")
				}
				args = append(append(args, "--"), tt.command...)
				var stdout, stderr bytes.Buffer
				code := run(args, func(string) string { return "" }, nil, &stdout, &stderr)
				if code != tt.code {
					t.Fatalf("code=%d want=%d stderr=%q", code, tt.code, stderr.String())
				}
				var fields map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil || len(fields) != 5 ||
					fields["result"] != base64.StdEncoding.EncodeToString([]byte(tt.stdout)) ||
					fields["error_logs"] != base64.StdEncoding.EncodeToString([]byte(tt.stderr)) {
					t.Fatalf("unexpected JSON schema: %q err=%v", stdout.String(), err)
				}
				var result struct {
					RunID     string `json:"run_id"`
					Status    string `json:"status"`
					ExitCode  int    `json:"exit_code"`
					Result    string `json:"result"`
					ErrorLogs string `json:"error_logs"`
				}
				decoder := json.NewDecoder(&stdout)
				if err := decoder.Decode(&result); err != nil {
					t.Fatalf("invalid JSON response: %v", err)
				}
				if err := decoder.Decode(new(any)); err != io.EOF {
					t.Fatalf("stdout contains more than one JSON object: %v", err)
				}
				wantStatus := "succeeded"
				if tt.code != 0 {
					wantStatus = "failed"
				}
				if result.RunID == "" || result.Status != wantStatus || result.ExitCode != tt.code {
					t.Fatalf("unexpected result: %+v", result)
				}
				for suffix, stream := range map[string]struct{ encoded, raw string }{
					".stdout.log": {result.Result, tt.stdout},
					".stderr.log": {result.ErrorLogs, tt.stderr},
				} {
					decoded, err := base64.StdEncoding.DecodeString(stream.encoded)
					if err != nil || !bytes.Equal(decoded, []byte(stream.raw)) {
						t.Fatalf("%s decoded=%q want=%q err=%v", suffix, decoded, stream.raw, err)
					}
					if stream.encoded != base64.StdEncoding.EncodeToString([]byte(stream.raw)) {
						t.Fatalf("%s is not canonical Base64: %q", suffix, stream.encoded)
					}
					log, err := os.ReadFile(filepath.Join(runDir, result.RunID+suffix))
					if err != nil || !bytes.Equal(log, []byte(stream.raw)) {
						t.Fatalf("%s log=%q want=%q err=%v", suffix, log, stream.raw, err)
					}
				}
				data, err := os.ReadFile(filepath.Join(runDir, result.RunID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var record flowpilot.RunRecord
				if err := json.Unmarshal(data, &record); err != nil || record.RunID != result.RunID || record.Status != result.Status || record.FinishedAt == nil {
					t.Fatalf("result does not match final record: %+v err=%v", record, err)
				}
				if tt.stderr != "" && strings.Contains(stderr.String(), tt.stderr) {
					t.Fatalf("child stderr was mirrored: %q", stderr.String())
				}
				if quiet && tt.code != 127 && stderr.Len() != 0 {
					t.Fatalf("quiet mode stderr=%q", stderr.String())
				}
			})
		}
	}
}

func TestRunCommandJSONPostmortemFailure(t *testing.T) {
	dir := t.TempDir()
	connector := filepath.Join(dir, "postmortem")
	if err := os.WriteFile(connector, []byte("#!/bin/sh\ncat >/dev/null\nprintf '{\"version\":1,\"ok\":false}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"postmortem": map[string]string{"path": connector, "version": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-config", configPath, "-run-dir", dir, "-result-json", "--", "sh", "-c", "echo hello"},
		func(string) string { return "" }, nil, &stdout, &stderr)
	var result struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
		Result   string `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if code != flowpilot.ExitInternal || result.Status != "failed" || result.ExitCode != 0 || result.Result != "aGVsbG8K" {
		t.Fatalf("code=%d result=%+v stderr=%q", code, result, stderr.String())
	}
}

type shortResultWriter struct {
	calls int
	err   error
}

func (w *shortResultWriter) Write(p []byte) (int, error) {
	w.calls++
	return 1, w.err
}

func TestRunCommandJSONOutputFailure(t *testing.T) {
	for _, writeErr := range []error{nil, errors.New("output unavailable")} {
		for _, commandCode := range []int{0, 7} {
			var stderr bytes.Buffer
			stdout := &shortResultWriter{err: writeErr}
			code := run([]string{"-no-config", "-run-dir", t.TempDir(), "-result-json", "--", "sh", "-c", "exit " + strconv.Itoa(commandCode)},
				func(string) string { return "" }, nil, stdout, &stderr)
			wantCode := commandCode
			if wantCode == 0 {
				wantCode = flowpilot.ExitInternal
			}
			if code != wantCode || stdout.calls != 1 || !strings.Contains(stderr.String(), "write JSON result:") {
				t.Fatalf("code=%d want=%d writes=%d stderr=%q", code, wantCode, stdout.calls, stderr.String())
			}
		}
	}
}

func TestRunCommandJSONBrokenPipe(t *testing.T) {
	for _, commandCode := range []int{0, 7} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		reader.Close()
		cmd := exec.Command(os.Args[0], "-test.run=^TestRunCommandJSONBrokenPipeHelper$")
		cmd.Env = append(os.Environ(), "FLOWPILOT_TEST_BROKEN_PIPE=1", "FLOWPILOT_TEST_RUN_DIR="+t.TempDir(),
			"FLOWPILOT_TEST_EXIT_CODE="+strconv.Itoa(commandCode))
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = writer, &stderr
		err = cmd.Run()
		writer.Close()
		wantCode := commandCode
		if wantCode == 0 {
			wantCode = flowpilot.ExitInternal
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != wantCode || !strings.Contains(stderr.String(), "write JSON result:") {
			t.Fatalf("err=%v want exit=%d stderr=%q", err, wantCode, stderr.String())
		}
	}
}

func TestRunCommandJSONBrokenPipeHelper(t *testing.T) {
	if os.Getenv("FLOWPILOT_TEST_BROKEN_PIPE") != "1" {
		return
	}
	os.Exit(run([]string{"-no-config", "-run-dir", os.Getenv("FLOWPILOT_TEST_RUN_DIR"), "-result-json", "--",
		"sh", "-c", "exit " + os.Getenv("FLOWPILOT_TEST_EXIT_CODE")}, os.Getenv, nil, os.Stdout, os.Stderr))
}

func TestRunJSONRequiresCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-result-json"}, func(string) string { return "" }, nil, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "-result-json requires a command") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
