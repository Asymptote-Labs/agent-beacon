package clinesession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreListsTaskHistoryAndMapsEvents(t *testing.T) {
	root := t.TempDir()
	clineDir := filepath.Join(root, ".cline")
	taskDir := filepath.Join(clineDir, "data", "tasks", "task-1")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(taskDir, "task_metadata.json"), map[string]interface{}{"cwd": "/repo/app"})
	writeJSON(t, filepath.Join(taskDir, historyFileName), []interface{}{
		map[string]interface{}{
			"role":      "user",
			"timestamp": 1700000000000,
			"content":   "<task>Update src/main.go</task>",
			"modelInfo": map[string]interface{}{"modelId": "claude-sonnet-4-5"},
		},
		map[string]interface{}{
			"role":      "assistant",
			"timestamp": 1700000001000,
			"modelInfo": map[string]interface{}{"modelId": "claude-sonnet-4-5"},
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Need to inspect the file."},
				map[string]interface{}{"type": "tool_use", "id": "call-1", "name": "write_to_file", "input": map[string]interface{}{"path": "src/main.go", "content": "package main\n"}},
			},
			"metrics": map[string]interface{}{"inputTokens": 10, "outputTokens": 5, "cacheReadTokens": 2, "thoughtsTokenCount": 1},
		},
		map[string]interface{}{
			"role":      "user",
			"timestamp": 1700000002000,
			"content":   []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": "call-1", "content": "File written successfully"}},
		},
	})

	store, err := NewStore(clineDir)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1", len(refs))
	}
	ref := refs[0]
	if ref.ID != "task-1" || ref.Kind != SourceHistory || ref.Directory != "/repo/app" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if ref.Title != "Update src/main.go" {
		t.Fatalf("title = %q", ref.Title)
	}
	records, err := store.Read(ref)
	if err != nil {
		t.Fatal(err)
	}
	mapped := MapTrace(ref, records, MapOptions{})
	wantActions := []string{"session.started", "prompt.submitted", "agent.reasoning", "tool.invoked", "file.modified"}
	if len(mapped) != len(wantActions) {
		t.Fatalf("mapped len = %d, want %d: %#v", len(mapped), len(wantActions), mapped)
	}
	for i, want := range wantActions {
		if got := mapped[i].Event.Event.Action; got != want {
			t.Fatalf("event %d action = %q, want %q", i, got, want)
		}
		if mapped[i].Event.Harness.CollectionMethod != "poll" {
			t.Fatalf("event %d collection method = %q", i, mapped[i].Event.Harness.CollectionMethod)
		}
	}
	fileEvent := mapped[len(mapped)-1].Event
	if fileEvent.File == nil || fileEvent.File.Path != "/repo/app/src/main.go" || fileEvent.File.Operation != "create" {
		t.Fatalf("unexpected file event: %+v", fileEvent.File)
	}
	toolInvoke := mapped[3].Event
	if toolInvoke.GenAI == nil || toolInvoke.GenAI.Usage == nil || toolInvoke.GenAI.Usage.InputTokens == nil || *toolInvoke.GenAI.Usage.InputTokens != 10 {
		t.Fatalf("usage not applied to tool invoke: %+v", toolInvoke.GenAI)
	}
}

func TestStoreListsSessionMessagesAndSubagents(t *testing.T) {
	root := t.TempDir()
	clineDir := filepath.Join(root, ".cline")
	sessionDir := filepath.Join(clineDir, "data", "sessions", "lead-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sessionDir, "lead-1.json"), map[string]interface{}{"task": "Lead task", "workspacePath": "/repo"})
	writeJSON(t, filepath.Join(sessionDir, "lead-1.messages.json"), map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "<environment_details>noise</environment_details>"},
		map[string]interface{}{"role": "user", "content": "Real prompt"},
	}})
	writeJSON(t, filepath.Join(sessionDir, "child-1.messages.json"), []interface{}{
		map[string]interface{}{"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "child answer"}}},
	})

	store, err := NewStore(clineDir)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs len = %d, want 2: %+v", len(refs), refs)
	}
	var lead, child TraceRef
	for _, ref := range refs {
		switch ref.ID {
		case "lead-1":
			lead = ref
		case "subagent:lead-1:child-1":
			child = ref
		}
	}
	if lead.ID == "" || lead.Directory != "/repo" {
		t.Fatalf("lead ref missing or wrong: %+v", lead)
	}
	if child.RelatedTo != "lead-1" || child.RelationshipType != "subagent" {
		t.Fatalf("child relationship wrong: %+v", child)
	}
	records, err := store.Read(lead)
	if err != nil {
		t.Fatal(err)
	}
	mapped := MapTrace(lead, records, MapOptions{})
	if len(mapped) != 2 {
		t.Fatalf("lead mapped len = %d, want session + prompt", len(mapped))
	}
	if mapped[1].Event.Prompt == nil || mapped[1].Event.Prompt.Text != "Real prompt" {
		t.Fatalf("unexpected prompt event: %+v", mapped[1].Event)
	}
}

func TestStoreListsKanbanSessions(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, ".cline", "kanban", "workspaces", "workspace-1")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(workspaceDir, "board.json"), map[string]interface{}{"columns": []interface{}{
		map[string]interface{}{"cards": []interface{}{map[string]interface{}{"id": "task-1", "prompt": "Build the dashboard", "createdAt": "2026-01-01T00:00:00Z"}}},
	}})
	writeJSON(t, filepath.Join(workspaceDir, "sessions.json"), map[string]interface{}{
		"task-1": map[string]interface{}{
			"workspacePath": "/repo/kanban",
			"modelId":       "claude-sonnet-4-5",
			"updatedAt":     "2026-01-01T00:01:00Z",
			"latestHookActivity": map[string]interface{}{
				"finalMessage": "Dashboard complete",
			},
		},
		"__home_agent__:ignored": map[string]interface{}{"state": "running"},
	})

	store, err := NewStore(filepath.Join(root, ".cline"))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.ID != "kanban:workspace-1:task-1" || ref.Kind != SourceKanban || ref.Directory != "/repo/kanban" {
		t.Fatalf("unexpected kanban ref: %+v", ref)
	}
	records, err := store.Read(ref)
	if err != nil {
		t.Fatal(err)
	}
	mapped := MapTrace(ref, records, MapOptions{})
	if len(mapped) != 3 {
		t.Fatalf("mapped len = %d, want session + prompt + summary", len(mapped))
	}
	if mapped[1].Event.Prompt == nil || mapped[1].Event.Prompt.Text != "Build the dashboard" {
		t.Fatalf("unexpected prompt: %+v", mapped[1].Event.Prompt)
	}
	if mapped[2].Event.Event.Action != "agent.message" || mapped[2].Event.Model != "claude-sonnet-4-5" {
		t.Fatalf("unexpected summary event: %+v", mapped[2].Event)
	}
}

func writeJSON(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
