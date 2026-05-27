package vscode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
)

func TestGetStatusForConfigUsesWorkspaceSettings(t *testing.T) {
	workspace := t.TempDir()
	settingsPath, err := harness.VSCodeWorkspaceSettingsPath(workspace)
	if err != nil {
		t.Fatalf("workspace settings path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"github.copilot.chat.otel.enabled":true,"github.copilot.chat.otel.otlpEndpoint":"http://127.0.0.1:4318"}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := GetStatusForConfig("", "http://127.0.0.1:4318", Config{WorkspacePath: workspace})
	if status.SettingsPath != settingsPath {
		t.Fatalf("SettingsPath = %q, want %q", status.SettingsPath, settingsPath)
	}
	if status.TelemetryStatus != harness.TelemetryEnabled {
		t.Fatalf("TelemetryStatus = %q, want enabled; message=%s", status.TelemetryStatus, status.Message)
	}
}
