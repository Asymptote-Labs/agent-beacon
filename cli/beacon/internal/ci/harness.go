package ci

import (
	"fmt"
	"net/url"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
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

func WithCIResourceAttributes(base []string, run *schema.RunInfo) []string {
	attrs := RunResourceAttributes(run)
	if attrs == "" {
		return base
	}
	env := envMap(base)
	if existing := strings.TrimSpace(env["OTEL_RESOURCE_ATTRIBUTES"]); existing != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = existing + "," + attrs
	} else {
		env["OTEL_RESOURCE_ATTRIBUTES"] = attrs
	}
	return flattenEnv(env)
}

func RunResourceAttributes(run *schema.RunInfo) string {
	if run == nil {
		return ""
	}
	fields := []struct {
		key   string
		value string
	}{
		{schema.AttributeOrigin, string(schema.OriginCI)},
		{schema.AttributeRunProvider, run.Provider},
		{schema.AttributeRunID, run.RunID},
		{schema.AttributeRunAttempt, run.RunAttempt},
		{schema.AttributeRunWorkflow, run.Workflow},
		{schema.AttributeRunJob, run.Job},
		{schema.AttributeRunEventName, run.EventName},
		{schema.AttributeRunCommit, run.Commit},
		{schema.AttributeRunRepository, run.Repository},
		{schema.AttributeRunBranch, run.Branch},
		{schema.AttributeRunPR, run.PR},
		{schema.AttributeRunPRNumber, run.PRNumber},
		{schema.AttributeRunActor, run.Actor},
	}
	attrs := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		attrs = append(attrs, field.key+"="+percentEncodeResourceValue(field.value))
	}
	if run.Ephemeral {
		attrs = append(attrs, schema.AttributeRunEphemeral+"=true")
	}
	return strings.Join(attrs, ",")
}

func percentEncodeResourceValue(value string) string {
	// OTEL_RESOURCE_ATTRIBUTES uses W3C Baggage-style percent encoding. QueryEscape
	// escapes delimiters such as comma and equals; spaces must remain percent-encoded.
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
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
