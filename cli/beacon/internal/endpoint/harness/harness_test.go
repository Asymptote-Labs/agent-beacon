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
		"log_user_prompt = false",
		"[otel.exporter.\"otlp-grpc\"]",
		"[otel.trace_exporter.\"otlp-grpc\"]",
		"[otel.metrics_exporter.\"otlp-grpc\"]",
		"endpoint = \"http://127.0.0.1:4317\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("codex config missing %q:\n%s", want, text)
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
