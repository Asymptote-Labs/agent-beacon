package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunCodexSessionContextWritesAttributableSessionEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("USER", "alice")
	originalPlatform := platformFlag
	platformFlag = "codex"
	t.Cleanup(func() { platformFlag = originalPlatform })

	runHookWithInput(t, runCodexSessionContext, map[string]interface{}{
		"hook_event_name": "SessionStart",
		"session_id":      "codex-session",
		"model":           "gpt-5.6-sol",
		"cwd":             "/repo",
	})

	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("events = %d, want one session context", len(events))
	}
	event := events[0]
	eventInfo := event["event"].(map[string]interface{})
	if eventInfo["action"] != "session.context" || eventInfo["fidelity"] != "observed" {
		t.Fatalf("event = %#v, want observed session.context", eventInfo)
	}
	if event["model"] != "gpt-5.6-sol" || event["repository"] != "/repo" {
		t.Fatalf("model/repository = %#v/%#v", event["model"], event["repository"])
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "codex-session" || session["working_directory"] != "/repo" {
		t.Fatalf("session = %#v", session)
	}
	user := event["user"].(map[string]interface{})
	if user["name"] != "alice" || user["uid"] == "" {
		t.Fatalf("user = %#v, want local name and UID", user)
	}
	raw := event["raw"].(map[string]interface{})
	if raw["source"] != "codex_session_start_hook" {
		t.Fatalf("raw = %#v", raw)
	}
}

func TestRunCodexSessionContextSkipsMissingSession(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	originalPlatform := platformFlag
	platformFlag = "codex"
	t.Cleanup(func() { platformFlag = originalPlatform })

	runHookWithInput(t, runCodexSessionContext, map[string]interface{}{
		"hook_event_name": "SessionStart",
		"model":           "gpt-5.6-sol",
	})

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("session context wrote an event without a session id: %v", err)
	}
}
