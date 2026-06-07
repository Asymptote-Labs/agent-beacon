package ci

import (
	"fmt"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// ClaudeTelemetryVars returns the Claude Code OpenTelemetry environment
// variables Beacon configures for an OTLP endpoint, as ordered key/value pairs.
// OTEL_LOG_USER_PROMPTS is omitted in metadata retention mode. This is the
// single source of truth for both in-process injection (ci exec) and exporting
// to a downstream step environment (ci start).
func ClaudeTelemetryVars(endpoint string, retention endpointconfig.ContentRetention) [][2]string {
	vars := [][2]string{
		{"CLAUDE_CODE_ENABLE_TELEMETRY", "1"},
		{"OTEL_LOGS_EXPORTER", "otlp"},
		{"OTEL_METRICS_EXPORTER", "otlp"},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT", endpoint},
	}
	if retention != endpointconfig.ContentRetentionMetadata {
		vars = append(vars, [2]string{"OTEL_LOG_USER_PROMPTS", "1"})
	}
	return vars
}

func ClaudeEnv(base []string, endpoint string, retention endpointconfig.ContentRetention) []string {
	env := envMap(base)
	for _, kv := range ClaudeTelemetryVars(endpoint, retention) {
		env[kv[0]] = kv[1]
	}
	delete(env, "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	delete(env, "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	delete(env, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if retention == endpointconfig.ContentRetentionMetadata {
		delete(env, "OTEL_LOG_USER_PROMPTS")
	}
	return flattenEnv(env)
}

func envMap(values []string) map[string]string {
	out := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func flattenEnv(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, fmt.Sprintf("%s=%s", key, value))
	}
	return out
}
