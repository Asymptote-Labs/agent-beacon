package codexsession

import (
	"strings"
	"testing"
)

func turnStartedLine(turnID string) string {
	return `{"timestamp":"2026-09-19T22:00:01.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":` + quote(turnID) + `}}`
}

// Codex brackets every turn with TurnStarted and TurnComplete and records the user's message inside
// the bracket. Every event of a turn carries its turn_id as prompt.id, and the next turn starts a
// new one.
func TestMappedEventsCarryTheirTurnAsPromptID(t *testing.T) {
	ref := SessionRef{ID: "codex-1", Path: "/tmp/codex-1.jsonl", Workspace: "/tmp/repo"}
	records := decodeFixture(t, []string{
		sessionMetaLine("codex-1", "/tmp/repo"),
		turnStartedLine("turn-1"),
		turnContextLine("turn-1", "gpt-5.3-codex"),
		messageLine("user", "u1", "run the tests"),
		toolCallLine("call_1", "shell", `{"command":"go test ./..."}`),
		toolOutputLine("call_1", "ok", "completed"),
		messageLine("assistant", "a1", "Tests pass."),
		taskCompleteLine("turn-1"),
		turnStartedLine("turn-2"),
		turnContextLine("turn-2", "gpt-5.3-codex"),
		messageLine("user", "u2", "now commit"),
	})

	mapped := MapSession(ref, records, MapOptions{})
	var got []string
	for _, item := range mapped {
		id := ""
		if item.Event.Prompt != nil {
			id = item.Event.Prompt.ID
		}
		got = append(got, item.Event.Event.Action+"="+id)
	}
	for i, entry := range got {
		action, id, _ := strings.Cut(entry, "=")
		switch {
		case action == "session.started":
			if id != "" {
				t.Fatalf("session.started prompt.id = %q, want none: %v", id, got)
			}
		case i == len(got)-1:
			if action != "prompt.submitted" || id != "turn-2" {
				t.Fatalf("last event = %s, want the second prompt under turn-2: %v", entry, got)
			}
		default:
			if id != "turn-1" {
				t.Fatalf("%s prompt.id = %q, want turn-1: %v", action, id, got)
			}
		}
	}
	if len(got) < 5 {
		t.Fatalf("mapped events = %v, want the turn's prompt, tool, message and completion", got)
	}
	prompt := findAction(t, mapped, "prompt.submitted")
	if prompt.Prompt.Text != "run the tests" {
		t.Fatalf("prompt text = %q, want it beside the id", prompt.Prompt.Text)
	}
}
