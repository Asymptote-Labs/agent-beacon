package cmd

import (
	"strings"
	"testing"
)

// codexStopPayload is Codex's documented Stop input: the common fields plus turn_id,
// stop_hook_active and last_assistant_message (string or null).
func codexStopPayload(lastMessage interface{}) map[string]interface{} {
	return map[string]interface{}{
		"session_id":             "019a-codex-thread",
		"transcript_path":        "/nonexistent/rollout.jsonl",
		"cwd":                    "/repo",
		"hook_event_name":        "Stop",
		"model":                  "gpt-5.5-codex",
		"permission_mode":        "default",
		"turn_id":                "turn-3",
		"stop_hook_active":       false,
		"last_assistant_message": lastMessage,
	}
}

// Codex's OTLP export never carries what the agent said; the Stop payload does. The hook records it
// as the same agent.message the codex sync backfill writes, under the live turn's identity.
func TestCodexAssistantResponseRecordsTheFinalReply(t *testing.T) {
	reply := "Renamed the handler and updated both call sites."
	logPath, stdout := runHookProcess(t, codexStopPayload(reply), "--platform", "codex", "codex-assistant-response")

	if strings.TrimSpace(string(stdout)) != "{}" {
		t.Fatalf("stdout = %q, want {}: Codex reads a Stop hook's stdout as JSON", stdout)
	}
	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("wrote %d events, want exactly the reply (OTLP already reports the turn): %#v", len(events), events)
	}
	message := events[0]
	if got := leaf(message, "event", "action"); got != "agent.message" {
		t.Fatalf("event.action = %q, want agent.message", got)
	}
	if got := outputText(t, message); got != reply {
		t.Fatalf("reply = %q, want %q", got, reply)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{[]string{"harness", "name"}, "codex_cli"},
		{[]string{"harness", "collection_method"}, "hook"},
		{[]string{"session", "id"}, "019a-codex-thread"},
		{[]string{"prompt", "id"}, "turn-3"},
		{[]string{"model"}, "gpt-5.5-codex"},
	} {
		if got := leaf(message, check.path...); got != check.want {
			t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
		}
	}
	if _, ok := message["content"].(map[string]interface{}); !ok {
		t.Fatalf("no content marker: %#v", message)
	}
}

// A turn that ended without a reply -- Codex sends null -- writes nothing at all, and still answers
// with valid JSON so Codex ends the turn normally.
func TestCodexAssistantResponseWithoutAReplyWritesNothing(t *testing.T) {
	for name, value := range map[string]interface{}{"null": nil} {
		t.Run(name, func(t *testing.T) {
			logPath, stdout := runHookProcess(t, codexStopPayload(value), "--platform", "codex", "codex-assistant-response")
			if strings.TrimSpace(string(stdout)) != "{}" {
				t.Fatalf("stdout = %q, want {}", stdout)
			}
			if events := endpointEventsIfAny(t, logPath); len(events) != 0 {
				t.Fatalf("wrote %d events for a turn with no reply: %#v", len(events), events)
			}
		})
	}
}

// The command is Codex's alone: under another platform flag it records nothing, like
// codex-session-context.
func TestCodexAssistantResponseIgnoresOtherPlatforms(t *testing.T) {
	logPath, stdout := runHookProcess(t, codexStopPayload("not codex"), "--platform", "claude", "codex-assistant-response")
	if strings.TrimSpace(string(stdout)) != "{}" {
		t.Fatalf("stdout = %q, want {}", stdout)
	}
	if events := endpointEventsIfAny(t, logPath); len(events) != 0 {
		t.Fatalf("recorded %d events under --platform claude: %#v", len(events), events)
	}
}
