package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndpointRedaction(t *testing.T) {
	got := redactEndpointString("token=super-secret")
	if got == "token=super-secret" {
		t.Fatal("expected token to be redacted")
	}
}

func TestRegularLogDoesNotWriteEndpointEventByDefault(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "test")
	logger.Info("diagnostic only")

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("generic logger wrote endpoint event by default, stat err=%v", err)
	}
}

func TestEndpointEventStillWritesStructuredTelemetry(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "test")
	if err := logger.EndpointEvent("approval.allowed", "approval", "info", "Pre-tool observed", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	if data, err := os.ReadFile(logPath); err != nil || len(data) == 0 {
		t.Fatalf("expected structured endpoint event, len=%d err=%v", len(data), err)
	}
}

func TestEndpointEventAddsCloudRunMetadataFromEnvironment(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "claude_code_web")
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "cse_123")
	t.Setenv("BEACON_RUN_REPOSITORY", "asymptote-labs/agent-beacon")
	t.Setenv("BEACON_RUN_BRANCH", "main")
	t.Setenv("BEACON_RUN_ACTOR", "alice@example.com")
	t.Setenv("BEACON_RUN_EPHEMERAL", "true")
	t.Setenv("BEACON_CLOUD_USER_ID_HASH", "user-hash")

	logger := NewLoggerForPlatform("session-start", "claude")
	if err := logger.EndpointEvent("session.started", "session", "info", "Session started", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got := event["origin"]; got != "cloud" {
		t.Fatalf("origin = %q, want cloud", got)
	}
	run := event["run"].(map[string]interface{})
	if run["provider"] != "claude_code_web" || run["run_id"] != "cse_123" || run["repository"] != "asymptote-labs/agent-beacon" || run["branch"] != "main" || run["actor"] != "alice@example.com" || run["ephemeral"] != true {
		t.Fatalf("run metadata = %#v", run)
	}
	user := event["user"].(map[string]interface{})
	if user["uid"] != "user-hash" {
		t.Fatalf("user uid = %q, want user-hash", user["uid"])
	}
}

func TestEndpointEventPrefersCloudUserHash(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CLOUD_USER_ID", "raw-user")
	t.Setenv("BEACON_CLOUD_USER_ID_HASH", "hashed-user")

	logger := NewLoggerForPlatform("session-start", "claude")
	if err := logger.EndpointEvent("session.started", "session", "info", "Session started", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	user := event["user"].(map[string]interface{})
	if user["uid"] != "hashed-user" {
		t.Fatalf("user uid = %q, want hashed-user", user["uid"])
	}
}

func TestEndpointEventUsesStableFallbackCloudRunID(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "shuttle-state.json")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "claude_code_web")
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", "bucket")
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", "credentials")
	t.Setenv("BEACON_CLOUD_SHUTTLE_STATE", statePath)

	logger := NewLoggerForPlatform("session-start", "claude")
	if err := logger.EndpointEvent("session.started", "session", "info", "Session started", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}
	if err := logger.EndpointEvent("tool.completed", "tool", "info", "Done", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	var first, second map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal first event: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal second event: %v", err)
	}
	firstRunID := first["run"].(map[string]interface{})["run_id"]
	secondRunID := second["run"].(map[string]interface{})["run_id"]
	if firstRunID == "" || firstRunID != secondRunID {
		t.Fatalf("fallback run IDs = %q and %q, want same non-empty value", firstRunID, secondRunID)
	}
}

func TestEndpointEventRotatesRuntimeLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("old log contents"), 0644); err != nil {
		t.Fatalf("write existing log: %v", err)
	}

	if err := appendEndpointJSONL(logPath, []byte("{\"message\":\"new event\"}\n"), 1, 2); err != nil {
		t.Fatalf("appendEndpointJSONL returned error: %v", err)
	}

	if rotated, err := os.ReadFile(logPath + ".1"); err != nil || string(rotated) != "old log contents" {
		t.Fatalf("expected rotated archive, data=%q err=%v", string(rotated), err)
	}
	if current, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(current), "new event") {
		t.Fatalf("expected current log to contain new event, data=%q err=%v", string(current), err)
	}
}

func TestEndpointEventSurfacesWriteFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	if err := os.Mkdir(logPath, 0755); err != nil {
		t.Fatalf("mkdir log path: %v", err)
	}
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "test")
	if err := logger.EndpointEvent("approval.allowed", "approval", "info", "Pre-tool observed", nil); err == nil {
		t.Fatal("EndpointEvent returned nil, want write failure")
	}
}
