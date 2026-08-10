package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeEnvIncludesDetailedToolAndPromptLogging(t *testing.T) {
	env := strings.Join(ClaudeEnv([]string{"PATH=/bin", "OTEL_LOG_TOOL_DETAILS=0", "OTEL_LOG_USER_PROMPTS=0"}, "http://127.0.0.1:4317"), "\n")
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317",
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta",
		"OTEL_LOG_TOOL_DETAILS=1",
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

// TestClaudeEnvExportsOnATimerShorterThanASession covers the intermittency in #320.
//
// The OpenTelemetry default metric interval is 60 seconds and `ci exec` wraps sessions that finish in
// five or ten, so without these nothing is exported on a timer and every event rides on the flush at
// shutdown -- a race that three dispatched runs of the same commit lost twice.
func TestClaudeEnvExportsOnATimerShorterThanASession(t *testing.T) {
	env := envMap(ClaudeEnv(nil, "http://127.0.0.1:4317"))

	for key, want := range map[string]string{
		"OTEL_METRIC_EXPORT_INTERVAL": "5000",
		"OTEL_BLRP_SCHEDULE_DELAY":    "1000",
		"OTEL_BSP_SCHEDULE_DELAY":     "1000",
	} {
		if env[key] != want {
			t.Fatalf("%s = %q, want %q; a session shorter than the export interval captures nothing "+
				"unless the shutdown flush wins a race", key, env[key], want)
		}
	}
}

// TestClaudeEnvDoesNotOverrideDeliberateExportTuning keeps this a wrapper rather than an owner.
//
// Someone who set these has a reason -- a long-running session, a slow collector, a cost concern --
// and `ci exec` runs their session rather than administering their telemetry.
func TestClaudeEnvDoesNotOverrideDeliberateExportTuning(t *testing.T) {
	base := flattenEnv(map[string]string{
		"OTEL_METRIC_EXPORT_INTERVAL": "30000",
		"OTEL_BSP_SCHEDULE_DELAY":     "250",
	})
	env := envMap(ClaudeEnv(base, "http://127.0.0.1:4317"))

	if env["OTEL_METRIC_EXPORT_INTERVAL"] != "30000" {
		t.Fatalf("OTEL_METRIC_EXPORT_INTERVAL = %q, want the caller's 30000", env["OTEL_METRIC_EXPORT_INTERVAL"])
	}
	if env["OTEL_BSP_SCHEDULE_DELAY"] != "250" {
		t.Fatalf("OTEL_BSP_SCHEDULE_DELAY = %q, want the caller's 250", env["OTEL_BSP_SCHEDULE_DELAY"])
	}
	// The one it did not set still gets the default, so a partially-tuned environment is completed
	// rather than left with a 60-second gap in it.
	if env["OTEL_BLRP_SCHEDULE_DELAY"] != "1000" {
		t.Fatalf("OTEL_BLRP_SCHEDULE_DELAY = %q, want it filled in", env["OTEL_BLRP_SCHEDULE_DELAY"])
	}
}
