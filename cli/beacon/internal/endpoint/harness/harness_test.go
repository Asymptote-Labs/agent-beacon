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
		"OTEL_EXPORTER_OTLP_PROTOCOL":  "grpc",
		"OTEL_EXPORTER_OTLP_ENDPOINT":  "http://127.0.0.1:4317",
		"OTEL_LOG_USER_PROMPTS":        "1",
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

func TestConfigureClaudeDisablesPromptLoggingForMetadataRetention(t *testing.T) {
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
		Endpoint:         "http://127.0.0.1:4317",
		UserMode:         true,
		ContentRetention: "metadata",
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
	if _, ok := settings["env"]["OTEL_LOG_USER_PROMPTS"]; ok {
		t.Fatalf("metadata retention should remove OTEL_LOG_USER_PROMPTS: %#v", settings["env"])
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
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("codex config missing %q:\n%s", want, text)
		}
	}
	for _, noisy := range []string{
		"[otel.trace_exporter.\"otlp-grpc\"]",
		"[otel.metrics_exporter.\"otlp-grpc\"]",
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

func TestConfigureCodexDisablesPromptLoggingForMetadataRetention(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := ConfigureCodex(ConfigureOptions{
		Endpoint:         "http://127.0.0.1:4317",
		UserMode:         true,
		ContentRetention: "metadata",
	})
	if err != nil {
		t.Fatalf("ConfigureCodex returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	if !strings.Contains(string(data), "log_user_prompt = false") {
		t.Fatalf("metadata retention should disable Codex prompt logging:\n%s", string(data))
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

	written, err := ConfigureGemini(ConfigureOptions{Endpoint: "http://127.0.0.1:4317", UserMode: true, ContentRetention: "full"})
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

func TestConfigureGeminiDisablesPromptLoggingForMetadataRetention(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := ConfigureGemini(ConfigureOptions{
		Endpoint:         "http://127.0.0.1:4317",
		UserMode:         true,
		ContentRetention: "metadata",
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
	if telemetry["logPrompts"] != false {
		t.Fatalf("metadata retention should disable prompt logging: %#v", telemetry)
	}
	if telemetry["traces"] != false {
		t.Fatalf("metadata retention should disable detailed traces: %#v", telemetry)
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
		{name: "beacon string outside hooks", body: `{"note":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin pre-tool"}`, status: TelemetryDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			status, _ := devinStatus(path)
			if status != tt.status {
				t.Fatalf("devinStatus = %q, want %q", status, tt.status)
			}
		})
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
