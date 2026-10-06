package pisession

import "testing"

func TestMapSessionPiTranscript(t *testing.T) {
	ref := SessionRef{
		ID:   "session-1",
		Path: "/tmp/pi-session-1.jsonl",
		Header: map[string]interface{}{
			"type": "session",
			"id":   "session-1",
			"cwd":  "/work/project",
			"git": map[string]interface{}{
				"repoUrl": "https://example.test/repo.git",
				"branch":  "main",
			},
		},
	}
	entries := []Entry{
		{Line: 1, Data: ref.Header},
		{Line: 2, Data: map[string]interface{}{"type": "message", "timestamp": "2026-09-19T01:00:00Z", "message": map[string]interface{}{
			"role": "user", "content": []interface{}{map[string]interface{}{"text": "inspect the repo"}},
		}}},
		{Line: 3, Data: map[string]interface{}{"type": "message", "timestamp": "2026-09-19T01:00:01Z", "message": map[string]interface{}{
			"role": "assistant",
			"usage": map[string]interface{}{
				"input": 10.0, "output": 5.0, "cacheRead": 2.0, "reasoning": 1.0,
				"cost": map[string]interface{}{"total": 0.01},
			},
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Need to inspect files"},
				map[string]interface{}{"type": "text", "text": "I will look."},
				map[string]interface{}{"type": "tool_call", "id": "call-1", "name": "bash", "arguments": map[string]interface{}{"command": "ls"}},
			},
		}}},
		{Line: 4, Data: map[string]interface{}{"type": "message", "timestamp": "2026-09-19T01:00:02Z", "message": map[string]interface{}{
			"role": "tool_result", "toolCallId": "call-1", "toolName": "bash", "content": []interface{}{map[string]interface{}{"text": "README.md"}},
		}}},
		{Line: 5, Data: map[string]interface{}{"type": "compaction", "timestamp": "2026-09-19T01:00:03Z", "summary": "kept recent work"}},
		{Line: 6, Data: map[string]interface{}{"type": "custom", "customType": "context:skill_loaded", "timestamp": "2026-09-19T01:00:04Z", "data": map[string]interface{}{"name": "share-to-traces", "path": "/work/project/.pi/skills/share/SKILL.md"}}},
	}

	mapped := MapSession(ref, entries, MapOptions{})
	actions := actionsOf(mapped)
	want := []string{
		"session.started",
		"prompt.submitted",
		"agent.reasoning",
		"agent.message",
		"tool.invoked",
		"command.executed",
		"session.compacting",
		"tool.invoked",
		"tool.completed",
	}
	if len(actions) != len(want) {
		t.Fatalf("actions length = %d, want %d: %#v", len(actions), len(want), actions)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions[%d] = %q, want %q; all=%#v", i, actions[i], want[i], actions)
		}
	}
	if mapped[0].Event.Harness.Name != Harness || mapped[0].Event.Harness.CollectionMethod != "poll" {
		t.Fatalf("harness = %#v", mapped[0].Event.Harness)
	}
	if mapped[0].Event.Repository != "https://example.test/repo.git" || mapped[0].Event.Branch != "main" {
		t.Fatalf("repo fields = %q %q", mapped[0].Event.Repository, mapped[0].Event.Branch)
	}
	command := mapped[5].Event.Command
	if command == nil || command.Command != "ls" || command.Output != "README.md" {
		t.Fatalf("command event = %#v", command)
	}
	if usage := mapped[4].Event.GenAI.Usage; usage == nil || usage.InputTokens == nil || *usage.InputTokens != 10 {
		t.Fatalf("usage not attached to last assistant event: %#v", usage)
	}
	for _, item := range mapped {
		if item.Event.Event.ID == "" {
			t.Fatalf("event %s missing deterministic id", item.Event.Event.Action)
		}
	}
}

func TestMapSessionSupportsTopLevelToolRecords(t *testing.T) {
	ref := SessionRef{ID: "session-1", Path: "/tmp/pi-session-1.jsonl", Header: map[string]interface{}{"type": "session", "id": "session-1"}}
	entries := []Entry{
		{Line: 1, Data: ref.Header},
		{Line: 2, Data: map[string]interface{}{"type": "tool_call", "id": "call-1", "toolName": "edit", "input": map[string]interface{}{"path": "/work/file.go"}}},
		{Line: 3, Data: map[string]interface{}{"type": "tool_result", "toolCallId": "call-1", "details": map[string]interface{}{"patch": "--- a/file.go\n+++ b/file.go\n@@\n-old\n+new\n"}}},
	}
	mapped := MapSession(ref, entries, MapOptions{})
	if got := actionsOf(mapped); len(got) != 3 || got[1] != "tool.invoked" || got[2] != "file.modified" {
		t.Fatalf("actions = %#v", got)
	}
	file := mapped[2].Event.File
	if file == nil || file.Path != "/work/file.go" || file.Diff == "" || file.DiffHash == "" {
		t.Fatalf("file event = %#v", file)
	}
}

// The server and tool come from what the result states, never from splitting a name whose halves
// cannot be told apart. The live hook mapper reads the same way, and a hook event and a session-file
// event for one call are merged only when they agree.
func TestMapSessionAttributesMCPToTheServerTheResultNames(t *testing.T) {
	ref := SessionRef{ID: "session-1", Path: "/tmp/pi-session-1.jsonl", Header: map[string]interface{}{"type": "session", "id": "session-1"}}
	entries := []Entry{
		{Line: 1, Data: ref.Header},
		{Line: 2, Data: map[string]interface{}{"type": "tool_call", "id": "call-1", "toolName": "mcp__beacon_managed_beacon_lookup", "input": map[string]interface{}{"kind": "harness"}}},
		{Line: 3, Data: map[string]interface{}{"type": "tool_result", "toolCallId": "call-1",
			"details": map[string]interface{}{"serverName": "beacon-managed", "mcpToolName": "beacon_lookup"}}},
		{Line: 4, Data: map[string]interface{}{"type": "tool_call", "id": "call-2", "toolName": "mcp__github_create_issue", "input": map[string]interface{}{}}},
		{Line: 5, Data: map[string]interface{}{"type": "tool_result", "toolCallId": "call-2"}},
	}
	mapped := MapSession(ref, entries, MapOptions{})
	if got := actionsOf(mapped); len(got) != 5 || got[2] != "mcp.tool_invoked" || got[4] != "mcp.tool_invoked" {
		t.Fatalf("actions = %#v", got)
	}
	if mcp := mapped[2].Event.MCP; mcp == nil || mcp.Server != "beacon-managed" || mcp.Tool != "beacon_lookup" {
		t.Fatalf("stated result mcp = %#v, want beacon-managed/beacon_lookup", mcp)
	}
	for _, i := range []int{1, 3, 4} {
		if mcp := mapped[i].Event.MCP; mcp != nil {
			t.Fatalf("%s for %s guessed mcp = %#v from the name", mapped[i].Event.Event.Action, mapped[i].Event.Tool.Name, mcp)
		}
	}
}

func actionsOf(items []MappedEvent) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Event.Event.Action)
	}
	return out
}
