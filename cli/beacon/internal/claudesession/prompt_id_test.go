package claudesession

import (
	"strings"
	"testing"
)

// withPromptID stamps promptId on a transcript line the way Claude Code does on prompts and tool
// results.
func withPromptID(line, promptID string) string {
	return strings.Replace(line, `{"parentUuid"`, `{"promptId":`+quote(promptID)+`,"parentUuid"`, 1)
}

// Claude Code stamps promptId on the prompt and its tool results but not on assistant entries.
// Every event a prompt led to -- the assistant's message, its tool call, the usage it reported,
// the command's result -- carries that prompt's id, and the next prompt starts a new one.
func TestMappedEventsCarryTheirPromptsID(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	records := decodeFixture(t, []string{
		withPromptID(userLine("sess-1", "u1", "add a health endpoint"), "p-1"),
		assistantLine("sess-1", "a1"),
		withPromptID(toolResultLine("sess-1", "r1", "toolu_bash", "ok", false), "p-1"),
		withPromptID(userLine("sess-1", "u2", "now document it"), "p-2"),
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
	want := []string{
		"session.started=",
		"prompt.submitted=p-1",
		"tool.invoked=p-1",
		"agent.message=p-1",
		"token.usage=p-1",
		"command.executed=p-1",
		"prompt.submitted=p-2",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt ids = %v, want %v", got, want)
	}
	prompt := findAction(t, mapped, "prompt.submitted")
	if prompt.Prompt.Text != "add a health endpoint" {
		t.Fatalf("prompt text = %q, want it beside the id", prompt.Prompt.Text)
	}
}

// A sync reads the whole file and emits only past its cursor, so the first records past the cursor
// still learn which prompt they belong to from the records before it.
func TestIncrementalMapKeepsThePromptFromBeforeTheCursor(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	records := decodeFixture(t, []string{
		withPromptID(userLine("sess-1", "u1", "add a health endpoint"), "p-1"),
		assistantLine("sess-1", "a1"),
	})

	mapped := MapSession(ref, records, MapOptions{MinLine: 1, SkipSessionStarted: true})
	if len(mapped) == 0 {
		t.Fatal("no events mapped past the cursor")
	}
	for _, item := range mapped {
		if item.Event.Prompt == nil || item.Event.Prompt.ID != "p-1" {
			t.Fatalf("%s prompt = %#v, want p-1", item.Event.Event.Action, item.Event.Prompt)
		}
	}
}
