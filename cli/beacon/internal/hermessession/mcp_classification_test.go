package hermessession

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Hermes registers MCP calls as mcp__<server>__<tool>. These names
// must take precedence over the generic shell/file name heuristics.
func TestMapSessionKeepsMCPToolsOutOfCommandAndFileEvents(t *testing.T) {
	for _, name := range []string{
		"mcp__github__list_issues", "mcp__github__search_repositories",
		"mcp__github__create_issue", "mcp__linear__read_document",
		"mcp__server__edit_record", "mcp__server__patch_record",
		"mcp__server__replace_record", "mcp__server__get_issue",
		"mcp__shell__run", "mcp__server__bash", "mcp__server__run",
	} {
		t.Run(name, func(t *testing.T) {
			arguments := `{"path":"record"}`
			if name == "mcp__server__run" {
				arguments = `{"command":"synthetic input, never executed"}`
			}
			calls, err := json.Marshal([]map[string]interface{}{{"id": "call_mcp", "function": map[string]interface{}{"name": name, "arguments": arguments}}})
			if err != nil {
				t.Fatal(err)
			}
			mapped, _ := MapSession(Session{ID: "mcp-session", StartedAtMS: 1000}, []Message{
				{ID: 1, SessionID: "mcp-session", Role: "assistant", TimestampMS: 2000, ToolCalls: string(calls)},
				{ID: 2, SessionID: "mcp-session", Role: "tool", TimestampMS: 3000, ToolCallID: "call_mcp", ToolName: name, Content: `{"content":"result"}`},
			}, &Cursor{Started: true})
			ev := findMappedAction(t, mapped, "mcp.tool_invoked")
			if ev.MCP == nil || ev.MCP.Tool != name {
				t.Fatalf("MCP = %#v, want tool %q", ev.MCP, name)
			}
			if ev.File != nil || ev.Command != nil {
				t.Fatalf("MCP event acquired file/command fields: %#v", ev)
			}
		})
	}
}

func TestClassifyToolKeepsNativeTools(t *testing.T) {
	for _, tc := range []struct{ name, action string }{
		{"terminal", "command.executed"}, {"read_file", "file.read"},
		{"write_file", "file.created"}, {"edit_file", "file.modified"},
		{"unknown", "tool.completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action, _, _, _ := classifyTool(tc.name, nil, nil)
			if action != tc.action {
				t.Fatalf("action = %q, want %q", action, tc.action)
			}
		})
	}
	action, _, _, _ := classifyTool("unknown", nil, map[string]interface{}{"failed": true})
	if action != "tool.failed" {
		t.Fatalf("failed tool action = %q", action)
	}
}

func TestCollectOnceEmitsMCPFromHermesStateDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := `[{"id":"call_terminal","function":{"name":"mcp__github__list_issues","arguments":"{\"state\":\"open\"}"}}]`
	_, err = db.Exec("UPDATE messages SET tool_calls = ? WHERE id = 2", calls)
	if err == nil {
		_, err = db.Exec("UPDATE messages SET tool_name = ?, content = ? WHERE id = 3", "mcp__github__list_issues", `{"content":"[]"}`)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "runtime.jsonl")
	summary, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: filepath.Join(dir, "state.json"), LogPath: logPath, Write: true, UserMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Errors != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	events := readEvents(t, logPath)
	ev := findAction(t, events, "mcp.tool_invoked")
	if ev.MCP == nil || ev.MCP.Tool != "mcp__github__list_issues" {
		t.Fatalf("MCP = %#v", ev.MCP)
	}
	for _, event := range events {
		if event.File != nil || event.Command != nil {
			t.Fatalf("unexpected file/command event: %#v", event)
		}
	}
}
