package beaconevent

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
)

// Claude Code stamps prompt.id on every event of a turn. Each one lands in prompt.id, the prompt's
// own text stays beside it, and an event Claude Code did not stamp gets no prompt object -- an
// empty one would read as a prompt to anything that checks for its presence.
func TestCapturedClaudeCoworkPromptIDOnEveryEventOfTheTurn(t *testing.T) {
	t.Setenv("UID", "501")
	fixture, events := capturedLogEvents(t, "claude-cowork-2.1.251.json")
	stamped := 0
	for i, record := range fixture.Records {
		want, _ := record.Attributes["prompt.id"].(string)
		event := events[i]
		if want == "" {
			if event.Prompt != nil && event.Prompt.ID != "" {
				t.Fatalf("%s prompt.id = %q, want none", record.Name, event.Prompt.ID)
			}
			continue
		}
		stamped++
		if event.Prompt == nil || event.Prompt.ID != want {
			t.Fatalf("%s prompt = %#v, want id %q", record.Name, event.Prompt, want)
		}
		if record.Name != "user_prompt" && event.Prompt.Text != "" {
			t.Fatalf("%s prompt.text = %q, want text only on the prompt itself", record.Name, event.Prompt.Text)
		}
	}
	if stamped == 0 {
		t.Fatal("fixture has no prompt.id records; the test asserts nothing")
	}
	for i, record := range fixture.Records {
		if record.Name == "user_prompt" && events[i].Prompt.Text != "COWORK_PROMPT_MARKER" {
			t.Fatalf("user_prompt text = %q, want it kept beside the id", events[i].Prompt.Text)
		}
	}
}

// Each runtime's OTLP spelling lands in the same field.
func TestOTLPPromptIDSpellingsNormalize(t *testing.T) {
	for name, tc := range map[string]struct {
		service string
		attrs   map[string]interface{}
		want    string
	}{
		"gemini cli prompt_id": {"gemini-cli", map[string]interface{}{"event.name": "gemini_cli.tool_call", "session.id": "g", "prompt_id": "g########2"}, "g########2"},
		"codex turn.id":        {"codex_cli_rs", map[string]interface{}{"event.name": "codex.tool_result", "conversation.id": "c", "turn.id": "turn-9"}, "turn-9"},
		"beacon canonical":     {"agent", map[string]interface{}{"beacon.event.action": "tool.invoked", "beacon.session.id": "s", "beacon.prompt.id": "p-1"}, "p-1"},
		"none":                 {"agent", map[string]interface{}{"beacon.event.action": "tool.invoked", "beacon.session.id": "s"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			logs := plog.NewLogs()
			rl := logs.ResourceLogs().AppendEmpty()
			rl.Resource().Attributes().PutStr("service.name", tc.service)
			record := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
			if err := record.Attributes().FromRaw(tc.attrs); err != nil {
				t.Fatal(err)
			}
			events := NewConverter(Options{}).EventsFromLogs(logs)
			if len(events) != 1 {
				t.Fatalf("events = %d, want 1", len(events))
			}
			got := ""
			if events[0].Prompt != nil {
				got = events[0].Prompt.ID
			}
			if got != tc.want {
				t.Fatalf("prompt.id = %q, want %q", got, tc.want)
			}
		})
	}
}
