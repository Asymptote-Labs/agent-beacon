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

// The log is append-only, so when two reports of one call land in the "wrong" order the line
// written first cannot be completed. A live report carrying command output the earlier one lacks
// is therefore kept; a report that adds nothing, or a polled backfill copy, is still suppressed.
func TestIsDuplicateEndpointEventKeepsALaterReportThatAddsCommandOutput(t *testing.T) {
	line := func(ts, method, output string) string {
		command := `{"command":"echo hi"}`
		if output != "" {
			command = `{"command":"echo hi","output":"` + output + `"}`
		}
		return `{"timestamp":"` + ts + `","event":{"action":"command.executed"},"harness":{"name":"claude_code","collection_method":"` + method + `"},` +
			`"session":{"id":"s1","working_directory":"/repo"},"command":` + command + `,"tool":{"name":"Bash","command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}}}`
	}
	cases := []struct {
		name                string
		existing, candidate string
		duplicate           bool
	}{
		{"live output after a report without it", line("2026-08-21T18:00:01Z", "otlp", ""), line("2026-08-21T18:00:02Z", "hook", "hi"), false},
		{"report without output after one with it", line("2026-08-21T18:00:01Z", "hook", "hi"), line("2026-08-21T18:00:07Z", "otlp", ""), true},
		{"both carry output", line("2026-08-21T18:00:01Z", "hook", "hi"), line("2026-08-21T18:00:09Z", "poll", "hi"), true},
		{"polled output after a report without it", line("2026-08-21T18:00:01Z", "otlp", ""), line("2026-08-21T18:00:09Z", "poll", "hi"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.jsonl")
			if err := os.WriteFile(path, []byte(tc.existing+"\n"), 0644); err != nil {
				t.Fatalf("write existing event: %v", err)
			}
			if got := IsDuplicateEndpointEvent(path, []byte(tc.candidate), EndpointDuplicateWindow); got != tc.duplicate {
				t.Fatalf("duplicate = %t, want %t", got, tc.duplicate)
			}
		})
	}
}

// A Claude Code Write reaches the hook through diffFields, which records
// file.operation "modify", and reaches the collector as "create" -- two words
// for one action. While the target included the operation, the pair could not
// match itself however well the call ID identified it, so every Write stayed in
// the log twice. Reported by Cursor Bugbot.
func TestIsDuplicateEndpointEventCollapsesAWriteDescribedTwoWays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	hook := `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/notes.md","operation":"modify"},"tool":{"name":"Write","path":"/repo/notes.md"},"gen_ai":{"tool":{"call":{"id":"toolu_w"}}}}`
	collector := []byte(`{"timestamp":"2026-08-21T18:00:07Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/notes.md","operation":"create"},"tool":{"name":"Write","path":"/repo/notes.md"},"gen_ai":{"tool":{"call":{"id":"toolu_w"}}}}`)
	if err := os.WriteFile(path, []byte(hook+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if !IsDuplicateEndpointEvent(path, collector, EndpointDuplicateWindow) {
		t.Fatal("one Write reported by both paths should collapse despite create/modify disagreeing")
	}
}

// Two edits to different files in one MultiEdit share a call ID, and must not
// collapse into one: the path is what separates them, so dropping the operation
// from the target must not have taken the path with it.
func TestIsDuplicateEndpointEventKeepsEditsToDifferentFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	first := `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/a.go","operation":"modify"},"gen_ai":{"tool":{"call":{"id":"toolu_m"}}}}`
	second := []byte(`{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/b.go","operation":"modify"},"gen_ai":{"tool":{"call":{"id":"toolu_m"}}}}`)
	if err := os.WriteFile(path, []byte(first+"\n"), 0644); err != nil {
		t.Fatalf("write existing event: %v", err)
	}

	if IsDuplicateEndpointEvent(path, second, EndpointDuplicateWindow) {
		t.Fatal("two files edited by one MultiEdit are two edits and must both survive")
	}
}
