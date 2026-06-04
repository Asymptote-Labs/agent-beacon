package ci

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func TestProvisionUsesRunnerTempAndWritesCollectorConfig(t *testing.T) {
	runnerTemp := t.TempDir()
	t.Setenv("RUNNER_TEMP", runnerTemp)
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nsleep 60\n")
	oldResolve := resolveCollectorBinary
	resolveCollectorBinary = func(configured string) (string, error) {
		if configured != collector {
			t.Fatalf("configured collector = %q, want %q", configured, collector)
		}
		return collector, nil
	}
	t.Cleanup(func() { resolveCollectorBinary = oldResolve })

	session, err := Provision(Options{CollectorPath: collector, Harness: "claude"})
	if err != nil {
		t.Fatalf("Provision returned error: %v", err)
	}
	if want := filepath.Join(runnerTemp, "beacon"); session.BaseDir != want {
		t.Fatalf("BaseDir = %q, want %q", session.BaseDir, want)
	}
	if session.LogPath != filepath.Join(runnerTemp, "beacon", "runtime.jsonl") {
		t.Fatalf("LogPath = %q", session.LogPath)
	}
	data, err := os.ReadFile(session.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), session.LogPath) {
		t.Fatalf("collector config does not reference log path:\n%s", data)
	}
}

func TestProvisionUsesLogPathDirectoryAsBaseDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "artifacts", "runtime.jsonl")
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nsleep 60\n")
	oldResolve := resolveCollectorBinary
	resolveCollectorBinary = func(string) (string, error) { return collector, nil }
	t.Cleanup(func() { resolveCollectorBinary = oldResolve })

	session, err := Provision(Options{CollectorPath: collector, LogPath: logPath, Harness: "claude"})
	if err != nil {
		t.Fatalf("Provision returned error: %v", err)
	}
	if want := filepath.Dir(logPath); session.BaseDir != want {
		t.Fatalf("BaseDir = %q, want %q", session.BaseDir, want)
	}
	if session.ConfigPath != filepath.Join(filepath.Dir(logPath), "otelcol.yaml") {
		t.Fatalf("ConfigPath = %q", session.ConfigPath)
	}
}

func TestProvisionRejectsUnsupportedHarness(t *testing.T) {
	if _, err := Provision(Options{Harness: "codex"}); err == nil {
		t.Fatal("Provision accepted unsupported harness")
	}
}

func TestStartStopCollectorProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX signal semantics")
	}
	collector := fakeExecutable(t, "collector", "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n")
	oldWait := waitCollectorReady
	waitCollectorReady = func(endpointconfig.Config, time.Duration) error { return nil }
	t.Cleanup(func() { waitCollectorReady = oldWait })

	session := &Session{
		CollectorBinary: collector,
		ConfigPath:      filepath.Join(t.TempDir(), "otelcol.yaml"),
		cfg:             endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl")),
	}
	if err := os.WriteFile(session.ConfigPath, []byte("receivers: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := session.Start(context.Background(), nil, nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := session.Stop(ctx); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
}

func TestRunChildInjectsClaudeEnvAndBeaconPaths(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "env.txt")
	child := fakeExecutable(t, "child", "#!/bin/sh\nenv > \"$1\"\n")
	session := &Session{
		BaseDir:      dir,
		ConfigPath:   filepath.Join(dir, "otelcol.yaml"),
		LogPath:      filepath.Join(dir, "runtime.jsonl"),
		GRPCEndpoint: "http://127.0.0.1:4317",
		cfg:          endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
	}
	exitCode, err := session.RunChild(context.Background(), []string{child, output}, nil, nil)
	if err != nil {
		t.Fatalf("RunChild returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=grpc",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317",
		"OTEL_LOG_USER_PROMPTS=1",
		"BEACON_CI_LOG_PATH=" + session.LogPath,
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("child env missing %q:\n%s", want, env)
		}
	}
}

func TestDetectRunInfoPullRequest(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	t.Setenv("GITHUB_WORKFLOW", "ci")
	t.Setenv("GITHUB_JOB", "telemetry")
	t.Setenv("GITHUB_SHA", "deadbeef")
	t.Setenv("GITHUB_REPOSITORY", "asymptote-labs/agent-beacon")
	t.Setenv("GITHUB_ACTOR", "octocat")
	t.Setenv("GITHUB_HEAD_REF", "feature/telemetry")
	t.Setenv("GITHUB_REF_NAME", "12/merge")
	t.Setenv("GITHUB_REF", "refs/pull/12/merge")

	info := detectRunInfo()
	if info == nil {
		t.Fatal("detectRunInfo returned nil in GitHub Actions")
	}
	if info.Provider != "github_actions" {
		t.Fatalf("Provider = %q, want github_actions", info.Provider)
	}
	if info.RunAttempt != "2" || info.Job != "telemetry" || info.EventName != "pull_request" {
		t.Fatalf("unexpected run context: %+v", info)
	}
	if info.Repository != "asymptote-labs/agent-beacon" {
		t.Fatalf("Repository = %q", info.Repository)
	}
	if info.Branch != "feature/telemetry" {
		t.Fatalf("Branch = %q, want PR head ref", info.Branch)
	}
	if info.PR != "refs/pull/12/merge" || info.PRNumber != "12" {
		t.Fatalf("PR = %q, PRNumber = %q", info.PR, info.PRNumber)
	}
}

func TestDetectRunInfoPullRequestTargetOmitsPR(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request_target")
	t.Setenv("GITHUB_HEAD_REF", "feature/telemetry")
	t.Setenv("GITHUB_REF_NAME", "main")
	t.Setenv("GITHUB_REF", "refs/heads/main")

	info := detectRunInfo()
	if info == nil {
		t.Fatal("detectRunInfo returned nil in GitHub Actions")
	}
	if info.Branch != "feature/telemetry" {
		t.Fatalf("Branch = %q, want PR head ref", info.Branch)
	}
	if info.PR != "" || info.PRNumber != "" {
		t.Fatalf("pull_request_target with base-branch ref should not record PR fields: PR=%q PRNumber=%q", info.PR, info.PRNumber)
	}
}

func TestDetectRunInfoPushOmitsPR(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("GITHUB_HEAD_REF", "")
	t.Setenv("GITHUB_REF_NAME", "main")
	t.Setenv("GITHUB_REF", "refs/heads/main")

	info := detectRunInfo()
	if info == nil {
		t.Fatal("detectRunInfo returned nil in GitHub Actions")
	}
	if info.Branch != "main" {
		t.Fatalf("Branch = %q, want ref name on push", info.Branch)
	}
	if info.PR != "" || info.PRNumber != "" {
		t.Fatalf("push build should not record PR fields: PR=%q PRNumber=%q", info.PR, info.PRNumber)
	}
}

func TestDetectRunInfoGenericCIFallback(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CI", "true")

	info := detectRunInfo()
	if info == nil {
		t.Fatal("detectRunInfo returned nil for generic CI")
	}
	if info.Provider != "ci" || !info.Ephemeral {
		t.Fatalf("unexpected generic CI run info: %+v", info)
	}
}

func TestDetectRunInfoNotCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CI", "")

	if info := detectRunInfo(); info != nil {
		t.Fatalf("detectRunInfo = %+v, want nil outside CI", info)
	}
}

func TestParsePRNumber(t *testing.T) {
	cases := map[string]string{
		"refs/pull/12/merge": "12",
		"refs/pull/7/head":   "7",
		"refs/heads/main":    "",
		"refs/pull//merge":   "",
		"refs/pull/abc/head": "",
		"":                   "",
	}
	for ref, want := range cases {
		if got := parsePRNumber(ref); got != want {
			t.Fatalf("parsePRNumber(%q) = %q, want %q", ref, got, want)
		}
	}
}

func fakeExecutable(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}
