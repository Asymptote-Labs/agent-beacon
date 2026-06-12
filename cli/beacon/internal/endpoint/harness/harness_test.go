package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeCodexOTELAddsBlock(t *testing.T) {
	got := mergeCodexOTEL("model = \"gpt-5\"\n", "http://127.0.0.1:4317")
	if !strings.Contains(got, "[otel]") {
		t.Fatalf("expected otel block: %s", got)
	}
	if !strings.Contains(got, `[otel.exporter."otlp-grpc"]`) ||
		!strings.Contains(got, `endpoint = "http://127.0.0.1:4317"`) {
		t.Fatalf("expected endpoint: %s", got)
	}
	if !strings.Contains(got, "model = \"gpt-5\"") {
		t.Fatalf("expected existing config to be preserved: %s", got)
	}
}

func TestMergeCodexOTELReplacesExistingBlock(t *testing.T) {
	input := "[otel]\nenabled = false\nendpoint = \"https://example.com\"\n\n[otel.exporter.\"otlp-http\"]\nendpoint = \"https://old.example.com/v1/logs\"\n\n[profile]\nname = \"default\"\n"
	got := mergeCodexOTEL(input, "http://127.0.0.1:4317")
	if strings.Contains(got, "https://example.com") || strings.Contains(got, "old.example.com") || strings.Contains(got, "enabled = false") {
		t.Fatalf("old otel config was not replaced: %s", got)
	}
	if !strings.Contains(got, "[profile]\nname = \"default\"") {
		t.Fatalf("following section was not preserved: %s", got)
	}
}

func TestConfigureClaudeWritesTelemetryEnvAndBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir claude config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"env":{"EXISTING":"1"}}`), 0600); err != nil {
		t.Fatalf("write existing claude config: %v", err)
	}

	written, err := ConfigureClaude(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureClaude returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureClaude path = %q, want %q", written, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude config: %v", err)
	}
	var settings map[string]map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal claude config: %v", err)
	}
	env := settings["env"]
	for key, want := range map[string]string{
		"EXISTING":                     "1",
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_LOGS_EXPORTER":           "otlp",
		"OTEL_METRICS_EXPORTER":        "otlp",
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
		"OTEL_EXPORTER_OTLP_PROTOCOL":                       "grpc",
		"OTEL_EXPORTER_OTLP_ENDPOINT":                       "http://127.0.0.1:4317",
		"OTEL_LOG_USER_PROMPTS":                             "1",
	} {
		if got := env[key]; got != want {
			t.Fatalf("env[%s] = %q, want %q; env=%#v", key, got, want, env)
		}
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
}

func TestConfigureClaudeEnablesPromptLogging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir claude config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"env":{"OTEL_LOG_USER_PROMPTS":"1"}}`), 0600); err != nil {
		t.Fatalf("write existing claude config: %v", err)
	}

	written, err := ConfigureClaude(ConfigureOptions{
		Endpoint: "http://127.0.0.1:4317",
		UserMode: true,
	})
	if err != nil {
		t.Fatalf("ConfigureClaude returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureClaude path = %q, want %q", written, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude config: %v", err)
	}
	var settings map[string]map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal claude config: %v", err)
	}
	if got := settings["env"]["OTEL_LOG_USER_PROMPTS"]; got != "1" {
		t.Fatalf("OTEL_LOG_USER_PROMPTS = %q, want 1; env=%#v", got, settings["env"])
	}
}

func TestClaudeStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "invalid json", body: `{`, status: TelemetryMisconfigured},
		{name: "missing env", body: `{}`, status: TelemetryDisabled},
		{name: "disabled", body: `{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"0"}}`, status: TelemetryDisabled},
		{name: "remote endpoint", body: `{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"https://example.com"}}`, status: TelemetryMisconfigured},
		{name: "enabled", body: `{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:4317"}}`, status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := claudeStatus(path)
			if status != tt.status {
				t.Fatalf("claudeStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestConfigureCodexWritesTelemetryBlockAndBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir codex config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0600); err != nil {
		t.Fatalf("write existing codex config: %v", err)
	}

	written, err := ConfigureCodex(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureCodex returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureCodex path = %q, want %q", written, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"model = \"gpt-5\"",
		"[otel]",
		"environment = \"dev\"",
		"log_user_prompt = true",
		"[otel.exporter.\"otlp-grpc\"]",
		"endpoint = \"http://127.0.0.1:4317\"",
		// metrics_exporter is required for the codex.turn.token_usage metric that
		// backs Codex token/cost attribution.
		"[otel.metrics_exporter.\"otlp-grpc\"]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("codex config missing %q:\n%s", want, text)
		}
	}
	// Traces stay disabled: Codex spans are high-volume and opt-in via
	// --include-codex-spans, and are not needed for token usage.
	for _, noisy := range []string{
		"[otel.trace_exporter.\"otlp-grpc\"]",
	} {
		if strings.Contains(text, noisy) {
			t.Fatalf("codex config should not enable noisy exporter %q:\n%s", noisy, text)
		}
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
}

func TestConfigureCodexEnablesPromptLogging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := ConfigureCodex(ConfigureOptions{
		Endpoint: "http://127.0.0.1:4317",
		UserMode: true,
	})
	if err != nil {
		t.Fatalf("ConfigureCodex returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	if !strings.Contains(string(data), "log_user_prompt = true") {
		t.Fatalf("ConfigureCodex should enable prompt logging:\n%s", string(data))
	}
}

func TestDiscoverCodexDoesNotDetectConfigDirectoryOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatalf("mkdir codex config dir: %v", err)
	}

	h := DiscoverCodex()
	if h.Detected {
		t.Fatalf("DiscoverCodex detected Codex from config directory only: %#v", h)
	}
	if h.ConfigPath != filepath.Join(home, ".codex", "config.toml") {
		t.Fatalf("ConfigPath = %q, want home config path", h.ConfigPath)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
}

func TestDiscoverCodexDetectsExecutableOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\necho codex 1.2.3\n"), 0755); err != nil {
		t.Fatalf("write fake codex executable: %v", err)
	}

	h := DiscoverCodex()
	if !h.Detected {
		t.Fatalf("DiscoverCodex did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != codexPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, codexPath)
	}
	if h.Version != "codex 1.2.3" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
}

func TestHermesStatusDetectsBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`hooks:
  pre_tool_call:
    - matcher: .*
      command: env BEACON_ENDPOINT_MODE=1 beacon-hooks --platform hermes pre-tool
  post_tool_call:
    - command: echo keep
`), 0600); err != nil {
		t.Fatal(err)
	}

	status, message := hermesStatus(path)
	if status != TelemetryEnabled {
		t.Fatalf("hermesStatus = %q (%s), want enabled", status, message)
	}
}

func TestHermesStatusReportsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  pre_tool_call: ["), 0600); err != nil {
		t.Fatal(err)
	}

	status, _ := hermesStatus(path)
	if status != TelemetryMisconfigured {
		t.Fatalf("hermesStatus = %q, want misconfigured", status)
	}
}

func TestCodexStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "missing block", body: `model = "gpt-5"`, status: TelemetryDisabled},
		{name: "old shape", body: "[otel]\nenabled = true\nendpoint = \"http://127.0.0.1:4317\"\n", status: TelemetryDisabled},
		{name: "exporter none", body: "[otel]\nexporter = \"none\"\n", status: TelemetryDisabled},
		{name: "remote endpoint", body: "[otel]\nexporter = { otlp-grpc = { endpoint = \"https://example.com:4317\" } }\n", status: TelemetryMisconfigured},
		{name: "grpc enabled", body: "[otel]\nexporter = { otlp-grpc = { endpoint = \"http://localhost:4317\" } }\n", status: TelemetryEnabled},
		{name: "grpc table enabled", body: "[otel]\nlog_user_prompt = false\n\n[otel.exporter.\"otlp-grpc\"]\nendpoint = \"http://localhost:4317\"\n", status: TelemetryEnabled},
		{name: "http enabled", body: "[otel]\nexporter = { otlp-http = { endpoint = \"http://127.0.0.1:4318/v1/logs\" } }\n", status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".toml")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := codexStatus(path)
			if status != tt.status {
				t.Fatalf("codexStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestConfigureGeminiWritesTelemetryAndBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir gemini config dir: %v", err)
	}
	existing := `{"general":{"vimMode":true},"telemetry":{"enabled":false,"outfile":".gemini/telemetry.log"}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatalf("write existing gemini config: %v", err)
	}

	written, err := ConfigureGemini(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureGemini returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureGemini path = %q, want %q", written, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read gemini config: %v", err)
	}
	var settings map[string]map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal gemini config: %v", err)
	}
	if settings["general"]["vimMode"] != true {
		t.Fatalf("unrelated settings were not preserved: %#v", settings)
	}
	telemetry := settings["telemetry"]
	for key, want := range map[string]interface{}{
		"enabled":      true,
		"target":       "local",
		"otlpEndpoint": "http://127.0.0.1:4317",
		"otlpProtocol": "grpc",
		"useCollector": true,
		"logPrompts":   true,
		"traces":       true,
	} {
		if got := telemetry[key]; got != want {
			t.Fatalf("telemetry[%s] = %#v, want %#v; telemetry=%#v", key, got, want, telemetry)
		}
	}
	if _, ok := telemetry["outfile"]; ok {
		t.Fatalf("outfile should be removed so OTLP is used: %#v", telemetry)
	}
	backups, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v", backups)
	}
}

func TestConfigureGeminiEnablesPromptLoggingAndTraces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := ConfigureGemini(ConfigureOptions{
		Endpoint: "http://127.0.0.1:4317",
		UserMode: true,
	})
	if err != nil {
		t.Fatalf("ConfigureGemini returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read gemini config: %v", err)
	}
	var settings map[string]map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal gemini config: %v", err)
	}
	telemetry := settings["telemetry"]
	if telemetry["logPrompts"] != true {
		t.Fatalf("ConfigureGemini should enable prompt logging: %#v", telemetry)
	}
	if telemetry["traces"] != true {
		t.Fatalf("ConfigureGemini should enable detailed traces: %#v", telemetry)
	}
}

func TestDiscoverGeminiDoesNotDetectConfigDirectoryOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0755); err != nil {
		t.Fatalf("mkdir gemini config dir: %v", err)
	}

	h := DiscoverGemini()
	if h.Detected {
		t.Fatalf("DiscoverGemini detected Gemini from config directory only: %#v", h)
	}
	if h.ConfigPath != filepath.Join(home, ".gemini", "settings.json") {
		t.Fatalf("ConfigPath = %q, want home config path", h.ConfigPath)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
}

func TestDiscoverGeminiDetectsExecutableOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	geminiPath := filepath.Join(binDir, "gemini")
	if err := os.WriteFile(geminiPath, []byte("#!/bin/sh\necho gemini 0.34.0\n"), 0755); err != nil {
		t.Fatalf("write fake gemini executable: %v", err)
	}

	h := DiscoverGemini()
	if !h.Detected {
		t.Fatalf("DiscoverGemini did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != geminiPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, geminiPath)
	}
	if h.Version != "gemini 0.34.0" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
}

func TestGeminiStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "invalid json", body: `{`, status: TelemetryMisconfigured},
		{name: "missing telemetry", body: `{}`, status: TelemetryDisabled},
		{name: "disabled", body: `{"telemetry":{"enabled":false}}`, status: TelemetryDisabled},
		{name: "remote target", body: `{"telemetry":{"enabled":true,"target":"gcp","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "outfile", body: `{"telemetry":{"enabled":true,"target":"local","outfile":".gemini/telemetry.log","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "remote endpoint", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"https://example.com:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "http protocol", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"http","useCollector":true}}`, status: TelemetryMisconfigured},
		{name: "collector disabled", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"http://127.0.0.1:4317","otlpProtocol":"grpc","useCollector":false}}`, status: TelemetryDisabled},
		{name: "enabled", body: `{"telemetry":{"enabled":true,"target":"local","otlpEndpoint":"http://localhost:4317","otlpProtocol":"grpc","useCollector":true}}`, status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := geminiStatus(path)
			if status != tt.status {
				t.Fatalf("geminiStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestDiscoverFactoryDetectsExecutableOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	droidPath := filepath.Join(binDir, "droid")
	if err := os.WriteFile(droidPath, []byte("#!/bin/sh\necho 0.127.0\n"), 0755); err != nil {
		t.Fatalf("write fake droid executable: %v", err)
	}

	h := DiscoverFactory()
	if !h.Detected {
		t.Fatalf("DiscoverFactory did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != droidPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, droidPath)
	}
	if h.Version != "0.127.0" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
	if h.ConfigPath != filepath.Join(home, ".bash_profile") {
		t.Fatalf("ConfigPath = %q, want bash profile", h.ConfigPath)
	}
}

func TestDiscoverCopilotCLIDetectsExecutableOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	copilotPath := filepath.Join(binDir, "copilot")
	if err := os.WriteFile(copilotPath, []byte("#!/bin/sh\necho copilot 1.0.4\n"), 0755); err != nil {
		t.Fatalf("write fake copilot executable: %v", err)
	}

	h := DiscoverCopilotCLI()
	if !h.Detected {
		t.Fatalf("DiscoverCopilotCLI did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != copilotPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, copilotPath)
	}
	if h.Version != "copilot 1.0.4" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
	if h.ConfigPath != filepath.Join(home, ".bash_profile") {
		t.Fatalf("ConfigPath = %q, want bash profile", h.ConfigPath)
	}
}

func TestDiscoverCopilotCLIDetectsConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	configPath := filepath.Join(home, ".copilot", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir copilot config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatalf("write copilot config: %v", err)
	}

	h := DiscoverCopilotCLI()
	if !h.Detected {
		t.Fatalf("DiscoverCopilotCLI did not detect config file: %#v", h)
	}
}

func TestDiscoverDevinDetectsExecutableOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	devinPath := filepath.Join(binDir, "devin")
	if err := os.WriteFile(devinPath, []byte("#!/bin/sh\necho devin 1.2.3\n"), 0755); err != nil {
		t.Fatalf("write fake devin executable: %v", err)
	}

	h := DiscoverDevin()
	if !h.Detected {
		t.Fatalf("DiscoverDevin did not detect executable on PATH: %#v", h)
	}
	if h.Name != "devin-cli" || h.DisplayName != "Devin CLI" {
		t.Fatalf("Devin harness identity = %s/%s, want devin-cli/Devin CLI", h.Name, h.DisplayName)
	}
	if h.ExecutablePath != devinPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, devinPath)
	}
	if h.Version != "devin 1.2.3" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
	if h.ConfigPath != filepath.Join(home, ".config", "devin", "config.json") {
		t.Fatalf("ConfigPath = %q, want user config path", h.ConfigPath)
	}
}

func TestDiscoverDevinDesktopDetectsAppSupport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	appSupport := filepath.Join(home, "Library", "Application Support", "Devin")
	if err := os.MkdirAll(appSupport, 0755); err != nil {
		t.Fatalf("mkdir app support: %v", err)
	}

	h := DiscoverDevinDesktop()
	if !h.Detected {
		t.Fatalf("DiscoverDevinDesktop did not detect app support: %#v", h)
	}
	if h.Name != "devin-desktop" || h.DisplayName != "Devin Desktop" {
		t.Fatalf("Desktop harness identity = %s/%s, want devin-desktop/Devin Desktop", h.Name, h.DisplayName)
	}
	if h.ExecutablePath != appSupport && h.ExecutablePath != "/Applications/Devin.app" {
		t.Fatalf("ExecutablePath = %q, want app support or app path", h.ExecutablePath)
	}
	if h.ConfigPath != filepath.Join(home, ".codeium", "windsurf", "hooks.json") {
		t.Fatalf("ConfigPath = %q, want Windsurf user hooks path", h.ConfigPath)
	}
}

func TestDevinStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "invalid json", body: `{`, status: TelemetryMisconfigured},
		{name: "no beacon hooks", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`, status: TelemetryDisabled},
		{name: "enabled", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}]}]}}`, status: TelemetryEnabled},
		{name: "standalone enabled", body: `{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}]}]}`, status: TelemetryEnabled},
		{name: "devin cli enabled", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-cli pre-tool"}]}]}}`, status: TelemetryEnabled},
		{name: "desktop does not satisfy cli", body: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-desktop pre-tool"}]}]}}`, status: TelemetryDisabled},
		{name: "beacon string outside hooks", body: `{"note":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}`, status: TelemetryDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := devinStatus(path, "devin", "devin-cli")
			if status != tt.status {
				t.Fatalf("devinStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestDevinDesktopStatusVariants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.json")
	body := `{"hooks":{"post_write_code":[{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-desktop post-tool"}]}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	status, _ := windsurfCascadeStatus(path, "devin-desktop")
	if status != TelemetryEnabled {
		t.Fatalf("windsurfCascadeStatus desktop = %q, want enabled", status)
	}
}

func TestFactoryStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "missing export", body: `export OTHER=1`, status: TelemetryDisabled},
		{name: "remote endpoint", body: `export OTEL_TELEMETRY_ENDPOINT="https://example.com:4318"`, status: TelemetryMisconfigured},
		{name: "localhost enabled", body: `export OTEL_TELEMETRY_ENDPOINT="http://localhost:4318"`, status: TelemetryEnabled},
		{name: "loopback enabled", body: `export OTEL_TELEMETRY_ENDPOINT=http://127.0.0.1:4318`, status: TelemetryEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".profile")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := factoryStatus(path)
			if status != tt.status {
				t.Fatalf("factoryStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestCopilotStatusVariants(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status TelemetryStatus
	}{
		{name: "missing export", body: `export OTHER=1`, status: TelemetryDisabled},
		{name: "enabled default endpoint", body: `export COPILOT_OTEL_ENABLED=true`, status: TelemetryEnabled},
		{name: "endpoint without enable flag", body: `export OTEL_EXPORTER_OTLP_ENDPOINT="https://example.com:4318"`, status: TelemetryDisabled},
		{name: "copilot endpoint without enable flag", body: `export COPILOT_OTEL_ENDPOINT="http://localhost:4318"`, status: TelemetryDisabled},
		{name: "standard endpoint without enable flag", body: `export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318`, status: TelemetryDisabled},
		{name: "enabled remote endpoint", body: "export COPILOT_OTEL_ENABLED=true\nexport OTEL_EXPORTER_OTLP_ENDPOINT=\"https://example.com:4318\"", status: TelemetryMisconfigured},
		{name: "enabled copilot endpoint", body: "export COPILOT_OTEL_ENABLED=true\nexport COPILOT_OTEL_ENDPOINT=\"http://localhost:4318\"", status: TelemetryEnabled},
		{name: "enabled standard endpoint", body: "export COPILOT_OTEL_ENABLED=true\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318", status: TelemetryEnabled},
		{name: "file exporter bypass", body: "export COPILOT_OTEL_ENABLED=true\nexport COPILOT_OTEL_FILE_EXPORTER_PATH=/tmp/copilot.jsonl", status: TelemetryMisconfigured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCopilotEnv(t)
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".profile")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := copilotStatus(path)
			if status != tt.status {
				t.Fatalf("copilotStatus = %q, want %q", status, tt.status)
			}
		})
	}
}

func TestCopilotStatusReadsProcessEnvironment(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("COPILOT_OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:54318")

	status, msg := copilotStatus(filepath.Join(t.TempDir(), "missing.profile"), "http://127.0.0.1:54318")
	if status != TelemetryEnabled {
		t.Fatalf("copilotStatus = %q (%s), want %q", status, msg, TelemetryEnabled)
	}
}

func TestCopilotStatusValidatesExpectedHTTPPort(t *testing.T) {
	clearCopilotEnv(t)
	path := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(path, []byte(`export COPILOT_OTEL_ENABLED=true`), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	status, _ := copilotStatus(path, "http://127.0.0.1:54318")
	if status != TelemetryMisconfigured {
		t.Fatalf("copilotStatus = %q, want %q for default 4318 endpoint with expected custom port", status, TelemetryMisconfigured)
	}
}

func TestCopilotStatusMergesProfileWithGenericProcessEndpoint(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")

	path := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(path, []byte("export COPILOT_OTEL_ENABLED=true\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	status, msg := copilotStatus(path)
	if status != TelemetryEnabled {
		t.Fatalf("copilotStatus = %q (%s), want %q; generic OTEL_EXPORTER_OTLP_ENDPOINT should not suppress profile COPILOT_OTEL_ENABLED", status, msg, TelemetryEnabled)
	}
}

func clearCopilotEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"COPILOT_OTEL_ENABLED",
		"COPILOT_OTEL_ENDPOINT",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"COPILOT_OTEL_FILE_EXPORTER_PATH",
	} {
		t.Setenv(key, "")
	}
}
