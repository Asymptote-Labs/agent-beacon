package ci

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	DefaultHarness        = "claude"
	DefaultSessionHarness = "claude,codex"
	HarnessClaude         = "claude"
	HarnessClaudeCode     = "claude_code"
	HarnessCodex          = "codex"
	HarnessCodexCLI       = "codex_cli"
)

type HarnessConfig struct {
	Harnesses []string          `json:"harnesses"`
	Env       map[string]string `json:"env,omitempty"`
	Paths     map[string]string `json:"paths,omitempty"`
}

func ClaudeEnv(base []string, endpoint string) []string {
	env := envMap(base)
	env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	env["OTEL_LOGS_EXPORTER"] = "otlp"
	env["OTEL_METRICS_EXPORTER"] = "otlp"
	// Delta temporality keeps token usage counters per-interval so downstream
	// aggregation can sum datapoints without deduping cumulative series.
	env["OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"] = "delta"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = endpoint
	delete(env, "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	delete(env, "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	delete(env, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	env["OTEL_LOG_USER_PROMPTS"] = "1"
	return flattenEnv(env)
}

func BuildHarnessConfig(base []string, harnessList, grpcEndpoint, baseDir string, run *schema.RunInfo) (HarnessConfig, error) {
	harnesses, err := NormalizeHarnesses(harnessList, DefaultHarness)
	if err != nil {
		return HarnessConfig{}, err
	}
	baseEnv := envMap(base)
	env := map[string]string{}
	if existing := strings.TrimSpace(baseEnv["OTEL_RESOURCE_ATTRIBUTES"]); existing != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = existing
	}
	paths := map[string]string{}
	for _, harness := range harnesses {
		switch harness {
		case HarnessClaude:
			env = envMap(ClaudeEnv(flattenEnv(env), grpcEndpoint))
		case HarnessCodex:
			codexHome := filepath.Join(baseDir, "codex-home")
			if err := writeCodexConfig(codexHome, grpcEndpoint); err != nil {
				return HarnessConfig{}, err
			}
			env["CODEX_HOME"] = codexHome
			paths["codex_home"] = codexHome
		}
	}
	env = envMap(WithCIResourceAttributes(flattenEnv(env), run))
	env["BEACON_CI_BASE_DIR"] = baseDir
	return HarnessConfig{Harnesses: harnesses, Env: env, Paths: paths}, nil
}

func NormalizeHarnesses(value, fallback string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		value = fallback
	}
	seen := map[string]struct{}{}
	var out []string
	for _, part := range strings.Split(value, ",") {
		normalized, err := NormalizeHarness(part)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one CI harness is required")
	}
	return out, nil
}

func NormalizeHarness(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case HarnessClaude, HarnessClaudeCode:
		return HarnessClaude, nil
	case HarnessCodex, HarnessCodexCLI:
		return HarnessCodex, nil
	default:
		return "", fmt.Errorf("unsupported CI harness %q; supported values are claude and codex", value)
	}
}

func HarnessesString(harnesses []string) string {
	if len(harnesses) == 0 {
		return ""
	}
	return strings.Join(harnesses, ",")
}

func writeCodexConfig(codexHome, endpoint string) error {
	if err := os.MkdirAll(codexHome, 0755); err != nil {
		return err
	}
	body := strings.Join([]string{
		"[otel]",
		"environment = \"ci\"",
		"log_user_prompt = true",
		"",
		"[otel.exporter.\"otlp-grpc\"]",
		fmt.Sprintf("endpoint = %q", endpoint),
		"",
	}, "\n")
	return os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0600)
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
