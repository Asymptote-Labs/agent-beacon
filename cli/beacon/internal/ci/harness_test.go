package ci

import (
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func TestClaudeEnvFullRetentionIncludesPromptLogging(t *testing.T) {
	env := strings.Join(ClaudeEnv([]string{"PATH=/bin", "OTEL_LOG_USER_PROMPTS=0"}, "http://127.0.0.1:4317", endpointconfig.ContentRetentionFull), "\n")
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317",
		"OTEL_LOG_USER_PROMPTS=1",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("ClaudeEnv missing %q in:\n%s", want, env)
		}
	}
}

func TestClaudeEnvMetadataRetentionOmitsPromptLogging(t *testing.T) {
	env := strings.Join(ClaudeEnv([]string{"OTEL_LOG_USER_PROMPTS=1"}, "http://127.0.0.1:4317", endpointconfig.ContentRetentionMetadata), "\n")
	if strings.Contains(env, "OTEL_LOG_USER_PROMPTS=") {
		t.Fatalf("ClaudeEnv metadata retention should omit OTEL_LOG_USER_PROMPTS:\n%s", env)
	}
}

func TestClaudeTelemetryVarsRetention(t *testing.T) {
	full := ClaudeTelemetryVars("http://127.0.0.1:4317", endpointconfig.ContentRetentionFull)
	got := map[string]string{}
	for _, kv := range full {
		got[kv[0]] = kv[1]
	}
	if got["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:4317" {
		t.Fatalf("endpoint var = %q", got["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
	if got["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Fatal("missing CLAUDE_CODE_ENABLE_TELEMETRY")
	}
	if _, ok := got["OTEL_LOG_USER_PROMPTS"]; !ok {
		t.Fatal("full retention should include OTEL_LOG_USER_PROMPTS")
	}

	for _, kv := range ClaudeTelemetryVars("http://127.0.0.1:4317", endpointconfig.ContentRetentionMetadata) {
		if kv[0] == "OTEL_LOG_USER_PROMPTS" {
			t.Fatal("metadata retention should omit OTEL_LOG_USER_PROMPTS")
		}
	}
}
