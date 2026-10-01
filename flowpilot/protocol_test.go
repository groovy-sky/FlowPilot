package flowpilot

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConnectorProtocolKeepsStderrSeparate(t *testing.T) {
	path := executable(t, `echo connector-log >&2; printf '{"version":1,"ok":true,"output":{"ok":true}}'`)
	result, diagnostics, err := (ConnectorRunner{MaxOutputBytes: 100}).Run(context.Background(),
		ConnectorConfig{Path: path, Version: "1"}, ConnectorInput{Version: 1, Phase: "prepare", RunID: "r"}, time.Second)
	if err != nil || !result.OK || len(diagnostics) != 1 || diagnostics[0] != "connector-log" {
		t.Fatalf("unexpected protocol result: %#v %#v %v", result, diagnostics, err)
	}
}

func TestConnectorOutputLimit(t *testing.T) {
	path := executable(t, `printf '{"version":1,"ok":true,"output":"123456789"}'`)
	_, _, err := (ConnectorRunner{MaxOutputBytes: 8}).Run(context.Background(),
		ConnectorConfig{Path: path, Version: "1"}, ConnectorInput{Version: 1}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("expected output limit error, got %v", err)
	}
}
