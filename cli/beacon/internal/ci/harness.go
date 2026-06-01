package ci

import (
	"fmt"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func ClaudeEnv(base []string, endpoint string, retention endpointconfig.ContentRetention) []string {
	env := envMap(base)
	env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	env["OTEL_LOGS_EXPORTER"] = "otlp"
	env["OTEL_METRICS_EXPORTER"] = "otlp"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = endpoint
	delete(env, "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	delete(env, "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	delete(env, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if retention == endpointconfig.ContentRetentionMetadata {
		delete(env, "OTEL_LOG_USER_PROMPTS")
	} else {
		env["OTEL_LOG_USER_PROMPTS"] = "1"
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
