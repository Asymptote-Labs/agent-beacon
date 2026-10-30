package ci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

var signalRoutingKeys = []string{
	"OTEL_EXPORTER_OTLP_HEADERS",
	"BETA_TRACING_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "OTEL_EXPORTER_OTLP_LOGS_HEADERS",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_HEADERS",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_HEADERS",
}

func TestRunChildOverridesInheritedSignalRouting(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	for _, signal := range []string{"LOGS", "METRICS", "TRACES"} {
		t.Setenv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT", "https://old-collector.invalid/v1/"+signal)
		t.Setenv("OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL", "http/protobuf")
		t.Setenv("OTEL_EXPORTER_OTLP_"+signal+"_HEADERS", "x-signal-token=secret")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer secret")
	t.Setenv("BETA_TRACING_ENDPOINT", "https://old-collector.invalid/beta")
	t.Setenv("OTEL_LOG_RAW_API_BODIES", "1")
	output := filepath.Join(t.TempDir(), "child-env")
	child := fakeExecutable(t, "capture-env", "#!/bin/sh\nenv > \"$1\"\n")
	session := &Session{Harness: "claude", BaseDir: t.TempDir(), GRPCEndpoint: "http://127.0.0.1:54317"}
	code, err := session.RunChild(context.Background(), []string{child, output}, nil, nil)
	if err != nil || code != 0 {
		t.Fatalf("child: code=%d error=%v", code, err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(strings.Split(string(data), "\n"))
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != session.GRPCEndpoint {
		t.Fatal("missing CI collector endpoint")
	}
	for _, key := range signalRoutingKeys {
		want := ""
		if strings.HasSuffix(key, "_PROTOCOL") {
			want = "grpc"
		}
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if env["OTEL_LOG_RAW_API_BODIES"] != "1" {
		t.Fatal("lost caller's independent capture opt-in")
	}
}

func TestSavedSessionExportsNeutralizeSignalEndpointOverrides(t *testing.T) {
	base := []string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=https://old-collector.invalid/logs", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf"}
	cfg, err := BuildHarnessConfig(base, "claude,codex", "http://127.0.0.1:54317", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{BaseDir: t.TempDir(), Env: cfg.Env, Paths: cfg.Paths}
	state := filepath.Join(t.TempDir(), "session.json")
	if err := session.SaveState(state); err != nil {
		t.Fatal(err)
	}
	restored, err := LoadState(state)
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(base)
	for key, value := range restored.Exports() {
		env[key] = value
	}
	if env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] != "" || env["OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"] != "grpc" {
		t.Fatalf("saved exports preserve inherited signal routing: %v", env)
	}
}

// ci start writes the overlay to $GITHUB_ENV for every later step, so a key the job never set must
// not appear there, even as an empty value.
func TestClaudeConfigAddsNoRoutingOverridesTheCallerDidNotNeed(t *testing.T) {
	cfg, err := BuildHarnessConfig([]string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=  "}, "claude", "http://127.0.0.1:54317", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range signalRoutingKeys {
		if value, ok := cfg.Env[key]; ok {
			t.Errorf("config sets %s=%q though the caller did not route that signal", key, value)
		}
	}
}

// Codex takes its endpoint from the generated config.toml, which its OTLP exporter prefers to any
// per-signal environment variable, so a Codex-only session leaves the caller's routing alone.
func TestCodexOnlyConfigLeavesInheritedSignalRoutingAlone(t *testing.T) {
	base := []string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=https://old-collector.invalid/logs", "OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer secret"}
	cfg, err := BuildHarnessConfig(base, "codex", "http://127.0.0.1:54317", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range signalRoutingKeys {
		if value, ok := cfg.Env[key]; ok {
			t.Errorf("Codex-only config sets %s=%q", key, value)
		}
	}
}
