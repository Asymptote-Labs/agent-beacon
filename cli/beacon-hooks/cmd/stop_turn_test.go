package cmd

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// emitStop writes the turn-end event the way runStop does. runStop itself ends with os.Exit, so the
// test calls the emitter with the arguments runStop passes it, as the Kimi, Qwen and Kiro stop tests
// do.
func emitStop(t *testing.T, platform string, input map[string]interface{}) map[string]interface{} {
	t.Helper()
	setupHookConfigDirs(t)
	original := platformFlag
	t.Cleanup(func() { platformFlag = original })
	platformFlag = platform
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	sessionID := resolveSessionID(input, platform)
	emitHookEvent(newHookLogger("stop", platform, sessionID), "tool.completed", "tool", "info",
		"Agent response completed", input, stopFields(sessionID, input))
	return lastEndpointEvent(t, logPath)
}

func cursorStopInput(status string) map[string]interface{} {
	return map[string]interface{}{
		"conversation_id": "conv-1",
		"generation_id":   "gen-1",
		"hook_event_name": "stop",
		"cursor_version":  "1.7.2",
		"status":          status,
		"loop_count":      0,
	}
}

// Cursor's stop hook names how the turn ended. Each status is kept as Cursor wrote it, while the
// event keeps the shape the dashboard reads as a turn end.
func TestCursorStopRecordsTurnStatus(t *testing.T) {
	for _, status := range []string{"completed", "aborted", "error"} {
		t.Run(status, func(t *testing.T) {
			event := emitStop(t, "cursor", cursorStopInput(status))
			if got := leaf(event, "turn", "status"); got != status {
				t.Fatalf("turn.status = %q, want %q", got, status)
			}
			if got := leaf(event, "event", "action"); got != "tool.completed" {
				t.Fatalf("event.action = %q, want tool.completed", got)
			}
			if got, _ := event["message"].(string); got != "Agent response completed" {
				t.Fatalf("message = %q, want Agent response completed", got)
			}
			if got := promptIDOfEvent(event); got != "gen-1" {
				t.Fatalf("prompt.id = %q, want the turn's generation_id kept beside turn.status", got)
			}
		})
	}
}

// Claude Code's Stop payload names no status, so its event gains nothing: no turn block, and in
// particular no "completed" that Claude Code never said.
func TestStopWithoutStatusWritesNoTurn(t *testing.T) {
	event := emitStop(t, "claude", map[string]interface{}{
		"session_id":       "claude-session",
		"cwd":              "/repo",
		"hook_event_name":  "Stop",
		"stop_hook_active": false,
	})
	if _, ok := event["turn"]; ok {
		t.Fatalf("turn = %v, want no turn block when the runtime names no status", event["turn"])
	}
}

// Only Cursor's top-level status is known to be the turn's outcome. A status key on another
// runtime's Stop payload is not read as one.
func TestStopStatusIsReadOnCursorOnly(t *testing.T) {
	event := emitStop(t, "qwen", map[string]interface{}{
		"session_id":      "qwen-session",
		"cwd":             "/repo",
		"hook_event_name": "Stop",
		"status":          "running",
	})
	if _, ok := event["turn"]; ok {
		t.Fatalf("turn = %v, want no turn block for a status Qwen Code does not define as the turn's outcome", event["turn"])
	}
}

// The block the hook writes is the one the typed schema reads, so beacon scan and every typed reader
// see it.
func TestStopTurnRoundTripsThroughSchema(t *testing.T) {
	data, err := json.Marshal(emitStop(t, "cursor", cursorStopInput("aborted")))
	if err != nil {
		t.Fatal(err)
	}
	var event asymptoteobserve.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Turn == nil || event.Turn.Status != "aborted" {
		t.Fatalf("schema turn = %+v, want status aborted", event.Turn)
	}
}
