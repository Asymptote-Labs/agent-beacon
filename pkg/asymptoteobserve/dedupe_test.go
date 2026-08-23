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

func TestIsDuplicateEndpointEventKeepsDifferentSameHarnessCallIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"tool.completed"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"webfetch"},"gen_ai":{"tool":{"name":"webfetch","call":{"id":"call_1"}}},"message":"opencode tool completed"}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.completed"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"webfetch"},"gen_ai":{"tool":{"name":"webfetch","call":{"id":"call_2"}}},"message":"opencode tool completed"}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("different same-harness call IDs should not dedupe")
	}
}

func TestIsDuplicateEndpointEventIgnoresCallIDDifferencesAcrossHarnesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	existing := `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"tool.invoked"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"read","path":"/repo/a.go"},"gen_ai":{"tool":{"name":"read","call":{"id":"opencode_1"}}},"message":"Tool execution observed"}`
	candidate := []byte(`{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.invoked"},"harness":{"name":"otel"},"session":{"id":"s1"},"tool":{"name":"read","path":"/repo/a.go"},"gen_ai":{"tool":{"name":"read","call":{"id":"span_9"}}},"message":"Tool execution observed"}`)
	if err := os.WriteFile(path, []byte(existing+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if !IsDuplicateEndpointEvent(path, candidate, EndpointDuplicateWindow) {
		t.Fatal("cross-harness duplicates should ignore call ID differences")
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

// The regression this exists to prevent. Duplicate suppression was fixed in July
// to collapse the hook and OTLP reports of one action, keyed on the two paths
// reporting different harness names. Harness normalization then landed in
// August -- correct on its own terms, and a real fix -- after which both paths
// reported claude_code, every pair took the same-harness branch, and that branch
// needed a call ID that nothing ever populated. Suppression was inert from
// that day on, and no test could have caught it because each change was right by
// itself.
func TestIsDuplicateEndpointEventCollapsesNormalizedHarnessOnCallID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	hook := `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo hi"},"tool":{"name":"Bash","command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}},"message":"Shell command executed"}`
	otlp := []byte(`{"timestamp":"2026-08-21T18:00:07Z","event":{"action":"command.executed"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo hi"},"tool":{"name":"Bash","command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}},"message":"Shell command executed"}`)
	if err := os.WriteFile(path, []byte(hook+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	// Six seconds apart, which is the ordinary case rather than an edge one: the
	// hook writes when the tool runs and the collector writes when its batch
	// flushes. The two-second window never stood a chance, which is why an equal
	// call ID does not consult it.
	if !IsDuplicateEndpointEvent(path, otlp, EndpointDuplicateWindow) {
		t.Fatal("the hook and OTLP reports of one Bash call should collapse on their shared call ID")
	}
}
