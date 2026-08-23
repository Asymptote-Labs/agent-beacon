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

func TestEndpointEventCompactsOversizedRetainedContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	large := make([]interface{}, 40)
	for i := range large {
		large[i] = strings.Repeat("x", 4096)
	}
	fields := map[string]interface{}{
		"extra":   large,
		"session": map[string]interface{}{"id": "ses_large"},
		"gen_ai": map[string]interface{}{
			"tool": map[string]interface{}{
				"name": "read",
				"call": map[string]interface{}{"id": "call_large", "result": large},
			},
		},
		"content": map[string]interface{}{"retention": "full", "included": true, "bytes": 163840},
	}

	logger := NewLoggerForPlatform("opencode-event", "opencode")
	if err := logger.EndpointEvent("tool.completed", "tool", "info", "opencode tool completed", fields); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 64*1024 {
		t.Fatalf("event size = %d", len(data))
	}
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event["field_truncated"] != true {
		t.Fatalf("field_truncated = %#v", event["field_truncated"])
	}
	content := event["content"].(map[string]interface{})
	if content["included"] != false || content["truncated"] != true {
		t.Fatalf("content = %#v", content)
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

func TestEndpointEventDoesNotInventCloudRunID(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "claude_code_web")
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", "bucket")
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", "credentials")
	// cloudRunFields falls back to this env var; clear it so the test stays
	// hermetic when it runs inside a Claude Code remote session.
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "")

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
	run := event["run"].(map[string]interface{})
	if _, ok := run["run_id"]; ok {
		t.Fatalf("run_id should be omitted when provider did not expose a run id: %#v", run)
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

func TestAppendEndpointJSONLDedupesRuntimeEvents(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := []byte(`{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"},"message":"Tool execution observed"}` + "\n")
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"},"message":"Tool execution observed"}` + "\n")

	if err := appendEndpointJSONL(logPath, existing, defaultEndpointRotateBytes, defaultEndpointRotateArchives); err != nil {
		t.Fatalf("first appendEndpointJSONL returned error: %v", err)
	}
	if err := appendEndpointJSONL(logPath, candidate, defaultEndpointRotateBytes, defaultEndpointRotateArchives); err != nil {
		t.Fatalf("second appendEndpointJSONL returned error: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected duplicate event to be suppressed, got %d lines: %s", len(lines), string(data))
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

// --- provenance: harness.collection_method and event.fidelity ---

func decodeSingleEndpointEvent(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("endpoint log has %d lines, want 1", len(lines))
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal endpoint event: %v", err)
	}
	return event
}

func nestedString(t *testing.T, event map[string]interface{}, block, key string) (string, bool) {
	t.Helper()
	inner, ok := event[block].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no %q block: %#v", block, event)
	}
	value, present := inner[key]
	if !present {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("%s.%s = %#v, want a string", block, key, value)
	}
	return text, true
}

func TestEndpointEventRecordsHookCollectionMethod(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "cursor")
	if err := logger.EndpointEvent("file.modified", "file", "info", "File edit observed", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	event := decodeSingleEndpointEvent(t, logPath)
	if method, ok := nestedString(t, event, "harness", "collection_method"); !ok || method != "hook" {
		t.Fatalf("harness.collection_method = %q (present=%v), want hook", method, ok)
	}
}

// One plugin platform, because the plugin case is the one a future integration gets wrong: the
// default is hook, so a plugin-shaped runtime that is not enumerated silently reports the wrong
// method.
func TestEndpointEventRecordsPluginCollectionMethod(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("cline-event", "cline")
	if err := logger.EndpointEvent("prompt.submitted", "prompt", "info", "Prompt observed", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	event := decodeSingleEndpointEvent(t, logPath)
	if method, ok := nestedString(t, event, "harness", "collection_method"); !ok || method != "plugin" {
		t.Fatalf("harness.collection_method = %q (present=%v), want plugin", method, ok)
	}
}

// This path builds raw maps rather than the shared Event struct, so it gets no `omitempty` for
// free. An empty string written into either field is worse than an absent one: a consumer would
// have to treat "" as a third case.
func TestEndpointEventOmitsProvenanceRatherThanWritingEmpty(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "")
	if err := logger.EndpointEventWithFidelity("tool.invoked", "tool", "info", "no platform", "", nil); err != nil {
		t.Fatalf("EndpointEventWithFidelity returned error: %v", err)
	}

	event := decodeSingleEndpointEvent(t, logPath)
	if _, present := nestedString(t, event, "harness", "collection_method"); present {
		t.Fatal("harness.collection_method written for an empty platform, want the key omitted")
	}
	if _, present := nestedString(t, event, "event", "fidelity"); present {
		t.Fatal("event.fidelity written when unset, want the key omitted")
	}
}

// EndpointEvent is the ordinary path and means "the runtime named this action", so it must not
// require every existing caller to opt in to being trusted.
func TestEndpointEventDefaultsToObservedFidelity(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("post-tool", "claude")
	if err := logger.EndpointEvent("command.executed", "command", "info", "Command observed", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	event := decodeSingleEndpointEvent(t, logPath)
	if fidelity, ok := nestedString(t, event, "event", "fidelity"); !ok || fidelity != "observed" {
		t.Fatalf("event.fidelity = %q (present=%v), want observed", fidelity, ok)
	}
}

func TestEndpointEventCarriesExplicitInferredFidelity(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "cursor")
	if err := logger.EndpointEventWithFidelity("approval.allowed", "approval", "info", "Pre-tool observed", "inferred", nil); err != nil {
		t.Fatalf("EndpointEventWithFidelity returned error: %v", err)
	}

	event := decodeSingleEndpointEvent(t, logPath)
	if fidelity, ok := nestedString(t, event, "event", "fidelity"); !ok || fidelity != "inferred" {
		t.Fatalf("event.fidelity = %q (present=%v), want inferred", fidelity, ok)
	}
}

// The size-limit fallback rewrites the event down to a key allowlist. Both provenance fields live
// inside blocks that survive wholesale, but a future edit to that list could drop them -- and
// losing the marker silently turns an inferred event into an unmarked one, which reads as trusted.
func TestMinimalEndpointEventKeepsProvenance(t *testing.T) {
	minimal := minimalEndpointEvent(map[string]interface{}{
		"event":   map[string]interface{}{"action": "approval.allowed", "fidelity": "inferred"},
		"harness": map[string]interface{}{"name": "cursor", "collection_method": "hook"},
	})
	if fidelity, ok := nestedString(t, minimal, "event", "fidelity"); !ok || fidelity != "inferred" {
		t.Fatalf("compacted event.fidelity = %q (present=%v), want inferred", fidelity, ok)
	}
	if method, ok := nestedString(t, minimal, "harness", "collection_method"); !ok || method != "hook" {
		t.Fatalf("compacted harness.collection_method = %q (present=%v), want hook", method, ok)
	}
}
