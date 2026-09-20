package primesession

import "testing"

func TestMapSessionPrimeTranscript(t *testing.T) {
	ref := SessionRef{
		ID:   "session-1",
		Path: "/tmp/session-1.jsonl",
		Kind: "session",
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
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Need to inspect files"},
				map[string]interface{}{"type": "text", "text": "I will look."},
				map[string]interface{}{"type": "tool_call", "id": "call-1", "name": "ipython", "arguments": map[string]interface{}{"code": "bash('ls')"}},
			},
		}}},
		{Line: 4, Data: map[string]interface{}{"type": "message", "timestamp": "2026-09-19T01:00:02Z", "message": map[string]interface{}{
			"role": "tool_result", "toolCallId": "call-1", "toolName": "ipython", "content": []interface{}{map[string]interface{}{"text": "README.md"}},
			"details": map[string]interface{}{"attachments": []interface{}{map[string]interface{}{"path": "/work/project/README.md", "mimeType": "text/markdown"}}},
		}}},
		{Line: 5, Data: map[string]interface{}{"type": "compaction", "timestamp": "2026-09-19T01:00:03Z", "summary": "kept recent work"}},
		{Line: 6, Data: map[string]interface{}{"type": "custom", "customType": "prime-agent.refinement", "timestamp": "2026-09-19T01:00:04Z", "data": map[string]interface{}{"summary": "state updated"}}},
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
		"file.read",
		"session.compacting",
		"session.context",
	}
	if len(actions) != len(want) {
		t.Fatalf("actions length = %d, want %d: %#v", len(actions), len(want), actions)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions[%d] = %q, want %q; all=%#v", i, actions[i], want[i], actions)
		}
	}
	command := mapped[5].Event.Command
	if command == nil || command.Command != "bash('ls')" || command.Output != "README.md" {
		t.Fatalf("command event = %#v", command)
	}
	if mapped[0].Event.Harness.Name != Harness || mapped[0].Event.Harness.CollectionMethod != "poll" {
		t.Fatalf("harness = %#v", mapped[0].Event.Harness)
	}
	if mapped[0].Event.Repository != "https://example.test/repo.git" || mapped[0].Event.Branch != "main" {
		t.Fatalf("repo fields = %q %q", mapped[0].Event.Repository, mapped[0].Event.Branch)
	}
	for _, item := range mapped {
		if item.Event.Event.ID == "" {
			t.Fatalf("event %s missing deterministic id", item.Event.Event.Action)
		}
	}
}

func TestMapSessionUsesPriorToolCallStateForNewResult(t *testing.T) {
	ref := SessionRef{ID: "session-1", Path: "/tmp/session-1.jsonl", Header: map[string]interface{}{"type": "session", "id": "session-1"}}
	entries := []Entry{
		{Line: 1, Data: ref.Header},
		{Line: 2, Data: map[string]interface{}{"type": "message", "message": map[string]interface{}{
			"role":    "assistant",
			"content": []interface{}{map[string]interface{}{"type": "tool_call", "id": "call-1", "name": "ipython", "arguments": map[string]interface{}{"code": "print('hi')"}}},
		}}},
		{Line: 3, Data: map[string]interface{}{"type": "message", "message": map[string]interface{}{
			"role": "tool_result", "toolCallId": "call-1", "content": []interface{}{map[string]interface{}{"text": "hi"}},
		}}},
	}
	mapped := MapSession(ref, entries, MapOptions{MinLine: 2, SkipStarted: true})
	if len(mapped) != 1 {
		t.Fatalf("mapped %d events, want 1: %#v", len(mapped), actionsOf(mapped))
	}
	if mapped[0].Event.Event.Action != "command.executed" {
		t.Fatalf("action = %q", mapped[0].Event.Event.Action)
	}
	if mapped[0].Event.Command == nil || mapped[0].Event.Command.Command != "print('hi')" {
		t.Fatalf("command = %#v", mapped[0].Event.Command)
	}
}

func actionsOf(items []MappedEvent) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Event.Event.Action)
	}
	return out
}
