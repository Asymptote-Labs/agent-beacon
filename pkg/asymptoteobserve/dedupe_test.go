package asymptoteobserve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDuplicateEndpointEvent(t *testing.T) {
	for _, tc := range []struct {
		name                string
		existing, candidate string
		want                bool
	}{
		{
			name:      "MCP call across harnesses",
			existing:  `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1","working_directory":"/repo"},"mcp":{"server":"clickhouse","tool":"execute_sql"},"message":"Tool execution observed"}`,
			candidate: `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"session":{"id":"s1","working_directory":"/repo"},"tool":{"name":"MCP:execute_sql"},"mcp":{"server":"clickhouse","tool":"execute_sql"},"message":"Tool execution observed"}`,
			want:      true,
		},
		{
			name:      "separate calls outside the window",
			existing:  `{"timestamp":"2026-06-18T21:11:19Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`,
			candidate: `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`,
		},
		{
			name:      "same harness without call IDs",
			existing:  `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`,
			candidate: `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"session":{"id":"s1"},"mcp":{"server":"clickhouse","tool":"execute_sql"}}`,
		},
		{
			name:      "same harness, different call IDs",
			existing:  `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"tool.completed"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"webfetch"},"gen_ai":{"tool":{"name":"webfetch","call":{"id":"call_1"}}},"message":"opencode tool completed"}`,
			candidate: `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.completed"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"webfetch"},"gen_ai":{"tool":{"name":"webfetch","call":{"id":"call_2"}}},"message":"opencode tool completed"}`,
		},
		{
			name:      "call ID differences across harnesses are ignored",
			existing:  `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"tool.invoked"},"harness":{"name":"opencode"},"session":{"id":"s1"},"tool":{"name":"read","path":"/repo/a.go"},"gen_ai":{"tool":{"name":"read","call":{"id":"opencode_1"}}},"message":"Tool execution observed"}`,
			candidate: `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.invoked"},"harness":{"name":"otel"},"session":{"id":"s1"},"tool":{"name":"read","path":"/repo/a.go"},"gen_ai":{"tool":{"name":"read","call":{"id":"span_9"}}},"message":"Tool execution observed"}`,
			want:      true,
		},
		{
			name:      "no session",
			existing:  `{"timestamp":"2026-06-18T21:11:24Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"cursor"},"mcp":{"tool":"list_tables"}}`,
			candidate: `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"mcp.tool_invoked"},"harness":{"name":"claude"},"mcp":{"tool":"list_tables"}}`,
		},
		{
			name:      "tool completion within its own window",
			existing:  `{"timestamp":"2026-06-18T21:11:25Z","event":{"action":"tool.completed"},"harness":{"name":"cursor"},"session":{"id":"s1"},"model":"gpt-5.5-medium","message":"Agent response completed"}`,
			candidate: `{"timestamp":"2026-06-18T21:11:34Z","event":{"action":"tool.completed"},"harness":{"name":"claude"},"session":{"id":"s1"},"model":"gpt-5.5-medium","message":"Agent response completed"}`,
			want:      true,
		},
		// The regression this exists to prevent. Duplicate suppression was fixed in July to collapse
		// the hook and OTLP reports of one action, keyed on the two paths reporting different harness
		// names. Harness normalization then landed in August, after which both paths reported
		// claude_code, every pair took the same-harness branch, and that branch needed a call ID
		// nothing populated. Six seconds apart is the ordinary case (the collector writes when its
		// batch flushes), which is why an equal call ID does not consult the window.
		{
			name:      "normalized harness collapses on call ID",
			existing:  `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo hi"},"tool":{"name":"Bash","command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}},"message":"Shell command executed"}`,
			candidate: `{"timestamp":"2026-08-21T18:00:07Z","event":{"action":"command.executed"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo hi"},"tool":{"name":"Bash","command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}},"message":"Shell command executed"}`,
			want:      true,
		},
		// A Claude Code Write reaches the hook as file.operation "modify" and the collector as
		// "create". While the target included the operation, every Write stayed in the log twice.
		{
			name:      "a Write described two ways",
			existing:  `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/notes.md","operation":"modify"},"tool":{"name":"Write","path":"/repo/notes.md"},"gen_ai":{"tool":{"call":{"id":"toolu_w"}}}}`,
			candidate: `{"timestamp":"2026-08-21T18:00:07Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/notes.md","operation":"create"},"tool":{"name":"Write","path":"/repo/notes.md"},"gen_ai":{"tool":{"call":{"id":"toolu_w"}}}}`,
			want:      true,
		},
		// Two edits to different files in one MultiEdit share a call ID; the path separates them,
		// so dropping the operation from the target must not have taken the path with it.
		{
			name:      "edits to different files under one call ID",
			existing:  `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/a.go","operation":"modify"},"gen_ai":{"tool":{"call":{"id":"toolu_m"}}}}`,
			candidate: `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/b.go","operation":"modify"},"gen_ai":{"tool":{"call":{"id":"toolu_m"}}}}`,
		},
		// "__" is ordinary text in a command or a path, not an MCP separator.
		{
			name:      "underscores in commands",
			existing:  `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo a__b"}}`,
			candidate: `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed"},"harness":{"name":"cursor"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo a:b"}}`,
		},
		{
			name:      "underscores in paths",
			existing:  `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/x__y.py"}}`,
			candidate: `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"cursor"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/x:y.py"}}`,
		},
		// Case folding is deliberately kept: the two capture paths of one call can spell the same
		// directory with different case.
		{
			name:      "command case folded",
			existing:  `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"Printf A"}}`,
			candidate: `{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed"},"harness":{"name":"cursor"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"printf a"}}`,
			want:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.jsonl")
			if err := os.WriteFile(path, []byte(tc.existing+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := IsDuplicateEndpointEvent(path, []byte(tc.candidate), EndpointDuplicateWindow); got != tc.want {
				t.Fatalf("IsDuplicateEndpointEvent = %v, want %v", got, tc.want)
			}
		})
	}
}

// The alias rules still have to apply where they came from, or an MCP call reported as
// "mcp__server__tool" by the hook stops matching the collector's separate server and tool fields.
func TestDedupeTargetStillAliasesMCPNames(t *testing.T) {
	hook := map[string]interface{}{
		"mcp":  map[string]interface{}{"server": "clickhouse"},
		"tool": map[string]interface{}{"name": "mcp__clickhouse__execute_sql"},
	}
	collector := map[string]interface{}{
		"mcp": map[string]interface{}{"server": "clickhouse", "tool": "clickhouse:execute_sql"},
	}
	if hookTarget, collectorTarget := dedupeTarget("mcp.tool_invoked", hook), dedupeTarget("mcp.tool_invoked", collector); hookTarget != collectorTarget {
		t.Fatalf("MCP targets diverged: hook %q, collector %q", hookTarget, collectorTarget)
	}
}
