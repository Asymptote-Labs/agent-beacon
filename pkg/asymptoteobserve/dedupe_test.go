package asymptoteobserve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDuplicateEndpointEventMatchesAcrossHarnesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1","working_directory":"/repo"},"mcp":{"server":"clickhouse","tool":"execute_sql"},"message":"Tool execution observed"}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"session":{"id":"s1","working_directory":"/repo"},"tool":{"name":"MCP:execute_sql"},"mcp":{"server":"clickhouse","tool":"execute_sql"},"message":"Tool execution observed"}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if !IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("expected duplicate MCP event across harnesses")
	}
}

func TestIsDuplicateEndpointEventKeepsSeparateCallsOutsideWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:19Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("did not expect events five seconds apart to dedupe")
	}
}

func TestIsDuplicateEndpointEventKeepsSameHarnessCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("same-harness events should not dedupe")
	}
}

func TestIsDuplicateEndpointEventCollapsesSameHarnessCallID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"tool.completed"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"webfetch"},"gen_ai":{"tool":{"name":"webfetch","call":{"id":"call_1"}}},"message":"opencode tool completed"}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.completed"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"webfetch"},"gen_ai":{"tool":{"name":"webfetch","call":{"id":"call_1"}}},"message":"opencode tool completed"}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if !IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("same OpenCode call ID should dedupe")
	}
}

func TestIsDuplicateEndpointEventRequiresSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"mcp":{"tool":"list_tables"}}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"mcp":{"tool":"list_tables"}}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("events without session IDs should not dedupe")
	}
}

func TestIsDuplicateEndpointEventMatchesToolCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.completed"},"harness":{"name":"cursor"},"session":{"id":"s1"},"model":"gpt-5.5-medium","message":"Agent response completed"}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:34Z","event":{"action":"tool.completed"},"harness":{"name":"claude"},"session":{"id":"s1"},"model":"gpt-5.5-medium","message":"Agent response completed"}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		return
	}
	t.Fatal("expected duplicate tool completion within custom window")
}
