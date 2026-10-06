package cmd

import (
	"path/filepath"
	"testing"
)

func promptIDOfEvent(event map[string]interface{}) string {
	return leaf(event, "prompt", "id")
}

// Cursor sends generation_id on every hook and changes it with every user message, so it is the
// turn: a shell approval carries the id of the prompt that led to it.
func TestCursorGenerationIDBecomesPromptID(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPreTool, map[string]interface{}{
		"conversation_id": "conv-1",
		"generation_id":   "gen-7",
		"hook_event_name": "beforeShellExecution",
		"command":         "npm test",
		"cwd":             "/repo",
	})

	event := lastEndpointEvent(t, logPath)
	if got := promptIDOfEvent(event); got != "gen-7" {
		t.Fatalf("prompt.id = %q, want Cursor's generation_id", got)
	}
	if got := leaf(event, "session", "id"); got != "conv-1" {
		t.Fatalf("session.id = %q, want conversation_id -- the session, not the turn", got)
	}
}

// Codex puts turn_id on every turn-scoped hook, the prompt submission included, so the prompt and
// the tool calls it led to share one id.
func TestCodexTurnIDBecomesPromptIDOnPromptAndTools(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	base := func(event string, extra map[string]interface{}) map[string]interface{} {
		input := map[string]interface{}{
			"hook_event_name": event,
			"session_id":      "codex-session",
			"cwd":             "/repo",
			"turn_id":         "turn-42",
		}
		for key, value := range extra {
			input[key] = value
		}
		return input
	}
	runHookWithInput(t, runPromptSubmit, base("UserPromptSubmit", map[string]interface{}{"prompt": "run the tests"}))
	runHookWithInput(t, runPostTool, base("PostToolUse", map[string]interface{}{
		"tool_name":     "Bash",
		"tool_use_id":   "call_1",
		"tool_input":    map[string]interface{}{"command": "go test ./..."},
		"tool_response": map[string]interface{}{"stdout": "ok"},
	}))

	events := endpointEvents(t, logPath)
	if len(events) < 2 {
		t.Fatalf("event count = %d, want the prompt and the command: %#v", len(events), events)
	}
	for _, event := range events {
		if got := promptIDOfEvent(event); got != "turn-42" {
			t.Fatalf("%s prompt.id = %q, want turn-42", leaf(event, "event", "action"), got)
		}
	}
	if got := leaf(events[0], "prompt", "text"); got != "run the tests" {
		t.Fatalf("prompt.text = %q, want the prompt kept beside its id", got)
	}
}

// Kimi Code numbers its turns. A decoded payload holds that as a float64, and the id is its
// integer rendering rather than "3e+00" or nothing.
func TestKimiNumberedTurnBecomesPromptID(t *testing.T) {
	logPath := kimiTestSetup(t)

	runHookWithInput(t, runPermissionRequest, kimiEvent("PermissionResult", "k-turn", map[string]interface{}{
		"turn_id":      float64(3),
		"tool_call_id": "call_z",
		"tool_name":    "Bash",
		"tool_input":   map[string]interface{}{"command": "ls"},
		"decision":     "approved",
	}))

	if got := promptIDOfEvent(lastEndpointEvent(t, logPath)); got != "3" {
		t.Fatalf("prompt.id = %q, want 3", got)
	}
}

// The id is read from the runtime's envelope only. A tool argument is model-authored, and an
// argument that happens to be called turn_id is not the runtime naming its turn.
func TestPromptIDIgnoresToolArguments(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "PostToolUse",
		"session_id":      "codex-session",
		"cwd":             "/repo",
		"tool_name":       "Bash",
		"tool_input":      map[string]interface{}{"command": "echo", "turn_id": "forged"},
		"tool_response":   map[string]interface{}{"stdout": "", "prompt_id": "forged"},
	})

	if got := promptIDOfEvent(lastEndpointEvent(t, logPath)); got != "" {
		t.Fatalf("prompt.id = %q, want none -- tool arguments are not the envelope", got)
	}
}

// One Cursor turn seen through every hook family it fires: the prompt, a file edit (written by the
// local-edit emitter, which does not go through emitHookEvent), and the stop that ends the turn.
// All of them carry the generation's id, and a later generation starts a new one.
func TestCursorTurnCarriesOneIDAcrossHookFamilies(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	envelope := func(event, generation string, extra map[string]interface{}) map[string]interface{} {
		input := map[string]interface{}{
			"conversation_id": "conv-1",
			"generation_id":   generation,
			"hook_event_name": event,
			"workspace_roots": []interface{}{dir},
			"cursor_version":  "1.7.2",
		}
		for key, value := range extra {
			input[key] = value
		}
		return input
	}
	runHookWithInput(t, runPromptSubmit, envelope("beforeSubmitPrompt", "gen-1", map[string]interface{}{"prompt": "add a main package"}))
	runHookWithInput(t, runPostTool, envelope("afterFileEdit", "gen-1", map[string]interface{}{
		"file_path": filepath.Join(dir, "main.go"),
		"edits":     []interface{}{map[string]interface{}{"old_string": "old line", "new_string": "package main"}},
	}))
	// Stop goes through its emitter rather than runStop, which ends with os.Exit; these are the
	// arguments runStop passes it, as in the Kimi, Qwen and Kiro stop tests.
	stopInput := envelope("stop", "gen-1", map[string]interface{}{"status": "completed"})
	stopSession := resolveSessionID(stopInput, "cursor")
	emitHookEvent(newHookLogger("stop", "cursor", stopSession), "tool.completed", "tool", "info",
		"Agent response completed", stopInput, sessionFields(stopSession, stopInput))
	runHookWithInput(t, runPromptSubmit, envelope("beforeSubmitPrompt", "gen-2", map[string]interface{}{"prompt": "now a license"}))

	events := endpointEvents(t, logPath)
	seen := map[string]string{}
	for _, event := range events {
		seen[leaf(event, "event", "action")+"@"+promptIDOfEvent(event)] = promptIDOfEvent(event)
	}
	for _, want := range []string{"prompt.submitted@gen-1", "file.modified@gen-1", "tool.completed@gen-1", "prompt.submitted@gen-2"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing %s; events: %v", want, seen)
		}
	}
	for key, id := range seen {
		if id == "" {
			t.Fatalf("%s has no prompt.id; events: %v", key, seen)
		}
	}
}
