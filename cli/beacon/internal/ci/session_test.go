package ci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
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
	if _, err := Provision(Options{Harness: "unknown"}); err == nil {
		t.Fatal("Provision accepted unsupported harness")
	}
}

func TestStartStopCollectorProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX signal semantics")
	}
	t.Setenv("RUNNER_TRACKING_ID", "github-actions-tracker")
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nenv > \"$2.env\"\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n")
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
	var data []byte
	for i := 0; i < 20; i++ {
		var err error
		data, err = os.ReadFile(session.ConfigPath + ".env")
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatalf("collector did not write env file")
	}
	if strings.Contains(string(data), "RUNNER_TRACKING_ID=") {
		t.Fatalf("collector env should not inherit RUNNER_TRACKING_ID:\n%s", data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := session.Stop(ctx); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
}

func TestStartDetachedWritesStateAndExports(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX signal semantics")
	}
	dir := t.TempDir()
	collector := fakeExecutable(t, "collector", "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n")
	oldWait := waitCollectorReady
	waitCollectorReady = func(endpointconfig.Config, time.Duration) error { return nil }
	t.Cleanup(func() { waitCollectorReady = oldWait })

	session := &Session{
		BaseDir:         dir,
		LogPath:         filepath.Join(dir, "runtime.jsonl"),
		ConfigPath:      filepath.Join(dir, "otelcol.yaml"),
		CollectorBinary: collector,
		GRPCEndpoint:    "http://127.0.0.1:4317",
		Harness:         "claude,codex",
		cfg:             endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
	}
	if err := os.WriteFile(session.ConfigPath, []byte("receivers: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "session.json")
	result, err := session.StartDetached(context.Background(), statePath, nil, nil)
	if err != nil {
		t.Fatalf("StartDetached returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = session.Stop(ctx)
	})
	if result.Exports["BEACON_CI_STATE_PATH"] != statePath {
		t.Fatalf("BEACON_CI_STATE_PATH = %q, want %q", result.Exports["BEACON_CI_STATE_PATH"], statePath)
	}
	if result.Exports["CODEX_HOME"] != filepath.Join(dir, "codex-home") {
		t.Fatalf("CODEX_HOME = %q", result.Exports["CODEX_HOME"])
	}
	loaded, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	if loaded.CollectorPID == 0 || loaded.StatePath != statePath {
		t.Fatalf("loaded state missing pid/path: %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(dir, "codex-home", "config.toml")); err != nil {
		t.Fatalf("codex config missing: %v", err)
	}
}

func TestRunChildInjectsClaudeEnvAndBeaconPaths(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "env.txt")
	child := fakeExecutable(t, "child", "#!/bin/sh\nenv > \"$1\"\n")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=custom")
	session := &Session{
		BaseDir:      dir,
		ConfigPath:   filepath.Join(dir, "otelcol.yaml"),
		LogPath:      filepath.Join(dir, "runtime.jsonl"),
		GRPCEndpoint: "http://127.0.0.1:4317",
		cfg:          endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
		Run: &schema.RunInfo{
			Provider:   "github_actions",
			RunID:      "123",
			RunAttempt: "2",
			Workflow:   "CI / build, smoke",
			Job:        "telemetry",
			EventName:  "pull_request",
			Repository: "asymptote-labs/agent-beacon",
			Branch:     "feature/ci telemetry",
			PRNumber:   "12",
			Ephemeral:  true,
		},
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
	resourceAttrs := envVarValue(env, "OTEL_RESOURCE_ATTRIBUTES")
	for _, want := range []string{
		"service.name=custom",
		"beacon.origin=ci",
		"beacon.run.provider=github_actions",
		"beacon.run.run_id=123",
		"beacon.run.workflow=CI%20%2F%20build%2C%20smoke",
		"beacon.run.branch=feature%2Fci%20telemetry",
		"beacon.run.ephemeral=true",
	} {
		if !strings.Contains(resourceAttrs, want) {
			t.Fatalf("OTEL_RESOURCE_ATTRIBUTES missing %q: %q", want, resourceAttrs)
		}
	}
	if !strings.HasPrefix(resourceAttrs, "service.name=custom,beacon.origin=ci,") {
		t.Fatalf("OTEL_RESOURCE_ATTRIBUTES should preserve existing attributes first: %q", resourceAttrs)
	}
	if strings.Count(resourceAttrs, "service.name=custom") != 1 {
		t.Fatalf("OTEL_RESOURCE_ATTRIBUTES duplicated existing attributes: %q", resourceAttrs)
	}
}

func envVarValue(env, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
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

func TestProvisionForwardWritesSecureExporterConfig(t *testing.T) {
	runnerTemp := t.TempDir()
	t.Setenv("RUNNER_TEMP", runnerTemp)
	t.Setenv(EnvSplunkToken, "top-secret-token")
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nsleep 60\n")
	oldResolve := resolveCollectorBinary
	resolveCollectorBinary = func(string) (string, error) { return collector, nil }
	t.Cleanup(func() { resolveCollectorBinary = oldResolve })

	session, err := Provision(Options{
		CollectorPath:   collector,
		Harness:         "claude",
		Forward:         "splunk",
		ForwardEndpoint: "https://splunk.example/services/collector",
	})
	if err != nil {
		t.Fatalf("Provision returned error: %v", err)
	}
	if session.Forward != "splunk" || session.ForwardEndpoint != "https://splunk.example/services/collector" {
		t.Fatalf("session forward fields = %q / %q", session.Forward, session.ForwardEndpoint)
	}

	data, err := os.ReadFile(session.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	yaml := string(data)
	if !strings.Contains(yaml, "splunk_hec:") {
		t.Fatalf("collector config missing splunk_hec exporter:\n%s", yaml)
	}
	if strings.Contains(yaml, "top-secret-token") {
		t.Fatal("raw token must not appear in collector config on disk")
	}
	if !strings.Contains(yaml, "${env:"+EnvSplunkToken+"}") {
		t.Fatalf("collector config should use env-var reference for token:\n%s", yaml)
	}

	info, err := os.Stat(session.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token-bearing config mode = %o, want 600", perm)
	}

	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "top-secret-token") {
		t.Fatalf("token leaked into session JSON:\n%s", encoded)
	}
}

func TestProvisionForwardMissingTokenFails(t *testing.T) {
	t.Setenv("RUNNER_TEMP", t.TempDir())
	t.Setenv(EnvSplunkToken, "")
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nsleep 60\n")
	oldResolve := resolveCollectorBinary
	resolveCollectorBinary = func(string) (string, error) { return collector, nil }
	t.Cleanup(func() { resolveCollectorBinary = oldResolve })

	_, err := Provision(Options{
		CollectorPath:   collector,
		Harness:         "claude",
		Forward:         "splunk",
		ForwardEndpoint: "https://splunk.example",
	})
	if err == nil {
		t.Fatal("Provision should fail when the forwarding token is missing")
	}
}

func TestRunChildStripsForwardToken(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "env.txt")
	t.Setenv(EnvSplunkToken, "leak-me-not")
	child := fakeExecutable(t, "child", "#!/bin/sh\nenv > \"$1\"\n")
	session := &Session{
		BaseDir:      dir,
		ConfigPath:   filepath.Join(dir, "otelcol.yaml"),
		LogPath:      filepath.Join(dir, "runtime.jsonl"),
		GRPCEndpoint: "http://127.0.0.1:4317",
		cfg:          endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
	}
	if _, err := session.RunChild(context.Background(), []string{child, output}, nil, nil); err != nil {
		t.Fatalf("RunChild returned error: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	if strings.Contains(env, "leak-me-not") || strings.Contains(env, EnvSplunkToken+"=") {
		t.Fatalf("child env exposed the SIEM token:\n%s", env)
	}
	if !strings.Contains(env, "CLAUDE_CODE_ENABLE_TELEMETRY=1") {
		t.Fatalf("child env missing Claude telemetry vars:\n%s", env)
	}
}

func TestRunChildStripsUploadCredentialsWhenUploadConfigured(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "env.txt")
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("AWS_SESSION_TOKEN", "aws-session")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/tmp/google-creds.json")
	child := fakeExecutable(t, "child", "#!/bin/sh\nenv > \"$1\"\n")
	session := &Session{
		BaseDir:      dir,
		ConfigPath:   filepath.Join(dir, "otelcol.yaml"),
		LogPath:      filepath.Join(dir, "runtime.jsonl"),
		GRPCEndpoint: "http://127.0.0.1:4317",
		Uploads: []UploadDestination{
			{Provider: UploadS3, URI: "s3://bucket/key"},
		},
		cfg: endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
	}
	if _, err := session.RunChild(context.Background(), []string{child, output}, nil, nil); err != nil {
		t.Fatalf("RunChild returned error: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	for _, leaked := range []string{"AWS_ACCESS_KEY_ID=", "AWS_SECRET_ACCESS_KEY=", "AWS_SESSION_TOKEN=", "GOOGLE_APPLICATION_CREDENTIALS=", "aws-secret", "/tmp/google-creds.json"} {
		if strings.Contains(env, leaked) {
			t.Fatalf("child env exposed upload credential %q:\n%s", leaked, env)
		}
	}
	if !strings.Contains(env, "CLAUDE_CODE_ENABLE_TELEMETRY=1") {
		t.Fatalf("child env missing Claude telemetry vars:\n%s", env)
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
