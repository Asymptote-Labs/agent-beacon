package vscode

import (
	"fmt"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
)

const (
	Name        = harness.VSCodeName
	CopilotName = harness.VSCodeCopilotName
	DisplayName = "VS Code"
)

type Config struct {
	Endpoint       string `json:"endpoint"`
	CaptureContent bool   `json:"capture_content"`
	WorkspacePath  string `json:"workspace_path,omitempty"`
}

type Status struct {
	Name                string                  `json:"name"`
	DisplayName         string                  `json:"display_name"`
	Detected            bool                    `json:"detected"`
	ExecutablePath      string                  `json:"executable_path,omitempty"`
	Version             string                  `json:"version,omitempty"`
	SettingsPath        string                  `json:"settings_path,omitempty"`
	TelemetryStatus     harness.TelemetryStatus `json:"telemetry_status"`
	LastEventObserved   bool                    `json:"last_event_observed"`
	LastEventObservedAt string                  `json:"last_event_observed_at,omitempty"`
	Message             string                  `json:"message"`
}

func DefaultConfig(httpPort int) Config {
	return Config{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", httpPort),
	}
}

func PrintConfig(cfg Config) string {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultConfig(4318).Endpoint
	}
	captureContent := "false"
	if cfg.CaptureContent {
		captureContent = "true"
	}
	scope := "user settings"
	if strings.TrimSpace(cfg.WorkspacePath) != "" {
		scope = "workspace settings"
	}
	return fmt.Sprintf(`VS Code Copilot OpenTelemetry setup

Add this to your VS Code %s:

  {
    "github.copilot.chat.otel.enabled": true,
    "github.copilot.chat.otel.exporterType": "otlp-http",
    "github.copilot.chat.otel.otlpEndpoint": %q,
    "github.copilot.chat.otel.captureContent": %s,
    "github.copilot.chat.otel.maxAttributeSizeChars": 8192
  }

Notes:
- Beacon listens for OTLP/HTTP on the local endpoint collector and writes low-noise normalized events to the runtime JSONL log.
- Beacon drops standalone chat spans, token/duration histograms, TTFT, HTTP instrumentation, and runtime/process metrics by default.
- Full prompt, response, tool argument, and tool result capture is off unless captureContent is explicitly enabled.
- Use VS Code hooks for Cursor-like lifecycle, prompt, tool, file, command, MCP, approval, and subagent telemetry.
`, scope, cfg.Endpoint, captureContent)
}

func Setup(cfg Config) (string, error) {
	return harness.ConfigureVSCode(harness.VSCodeConfigOptions{
		Endpoint:       cfg.Endpoint,
		CaptureContent: cfg.CaptureContent,
		WorkspacePath:  cfg.WorkspacePath,
	})
}

func GetStatus(logPath, expectedEndpoint string) Status {
	return GetStatusForConfig(logPath, expectedEndpoint, Config{})
}

func GetStatusForConfig(logPath, expectedEndpoint string, cfg Config) Status {
	discovered := harness.DiscoverVSCode()
	settingsPath := discovered.ConfigPath
	if strings.TrimSpace(cfg.WorkspacePath) != "" {
		if path, err := harness.VSCodeWorkspaceSettingsPath(cfg.WorkspacePath); err == nil {
			settingsPath = path
		}
	}
	status := Status{
		Name:            Name,
		DisplayName:     DisplayName,
		Detected:        discovered.Detected,
		ExecutablePath:  discovered.ExecutablePath,
		Version:         discovered.Version,
		SettingsPath:    settingsPath,
		TelemetryStatus: discovered.TelemetryStatus,
		Message:         discovered.Message,
	}
	if settingsPath != "" {
		status.TelemetryStatus, status.Message = harness.VSCodeOTelStatus(settingsPath, expectedEndpoint)
	}
	if last, ok := LastVSCodeEvent(logPath); ok {
		status.LastEventObserved = true
		if !last.IsZero() {
			status.LastEventObservedAt = last.UTC().Format(time.RFC3339)
		}
	}
	if status.LastEventObserved {
		status.Message = "VS Code events have been observed in the endpoint runtime log"
	}
	return status
}

func HasVSCodeEventSince(logPath string, since time.Time) bool {
	if integrations.HasHarnessEventSince(logPath, Name, since) {
		return true
	}
	return integrations.HasHarnessEventSince(logPath, CopilotName, since)
}

func LastVSCodeEvent(logPath string) (time.Time, bool) {
	hookLast, hookOK := integrations.LastHarnessEvent(logPath, Name)
	copilotLast, copilotOK := integrations.LastHarnessEvent(logPath, CopilotName)
	if !hookOK {
		return copilotLast, copilotOK
	}
	if !copilotOK || hookLast.After(copilotLast) {
		return hookLast, true
	}
	return copilotLast, true
}
