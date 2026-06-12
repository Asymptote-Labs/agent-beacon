package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeEnvIncludesPromptLogging(t *testing.T) {
	env := strings.Join(ClaudeEnv([]string{"PATH=/bin", "OTEL_LOG_USER_PROMPTS=0"}, "http://127.0.0.1:4317"), "\n")
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317",
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta",
		"OTEL_LOG_USER_PROMPTS=1",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("ClaudeEnv missing %q in:\n%s", want, env)
		}
	}
}

func TestBuildHarnessConfigWritesCodexHome(t *testing.T) {
	baseDir := t.TempDir()
	cfg, err := BuildHarnessConfig(nil, "codex", "http://127.0.0.1:4317", baseDir, nil)
	if err != nil {
		t.Fatalf("BuildHarnessConfig returned error: %v", err)
	}
	codexHome := filepath.Join(baseDir, "codex-home")
	if got := cfg.Env["CODEX_HOME"]; got != codexHome {
		t.Fatalf("CODEX_HOME = %q, want %q", got, codexHome)
	}
	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"[otel]",
		"environment = \"ci\"",
		"log_user_prompt = true",
		"[otel.exporter.\"otlp-grpc\"]",
		"endpoint = \"http://127.0.0.1:4317\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("codex config missing %q:\n%s", want, text)
		}
	}
}

func TestNormalizeHarnessesSupportsClaudeAndCodexAliases(t *testing.T) {
	got, err := NormalizeHarnesses("claude_code,codex_cli,claude", DefaultHarness)
	if err != nil {
		t.Fatalf("NormalizeHarnesses returned error: %v", err)
	}
	joined := strings.Join(got, ",")
	if joined != "claude,codex" {
		t.Fatalf("NormalizeHarnesses = %q, want claude,codex", joined)
	}
}
