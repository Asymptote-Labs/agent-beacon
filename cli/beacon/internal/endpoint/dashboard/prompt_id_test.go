package dashboard

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// Every event of a turn carries prompt.id, so a prompt object is no longer the mark of a prompt.
// A session whose tool calls and responses name their prompt still has one prompt, and the prompt
// it shows is the prompt's text rather than whatever came first with an id.
func TestPromptIDOnEveryEventDoesNotMakeThemPrompts(t *testing.T) {
	agentMessage := testSchemaEvent("2026-05-13T01:00:00Z", "claude_code", "agent.message", "session", "repo-a")
	agentMessage.Prompt = &schema.PromptInfo{ID: "p-1"}
	if isPromptEvent(agentMessage) {
		t.Fatal("an agent message naming its prompt was classified as a prompt")
	}
	prompt := testSchemaEvent("2026-05-13T01:00:01Z", "claude_code", "prompt.submitted", "prompt", "repo-a")
	prompt.Prompt = &schema.PromptInfo{ID: "p-1", Text: "Investigate the dashboard"}
	if !isPromptEvent(prompt) {
		t.Fatal("the prompt itself was not classified as a prompt")
	}

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	events := []schema.Event{
		testSchemaEvent("2026-05-13T01:00:00Z", "claude_code", "session.activity", "session", "repo-a"),
		prompt,
		testSchemaEvent("2026-05-13T01:00:02Z", "claude_code", "command.executed", "command", "repo-a"),
		agentMessage,
	}
	events[0].Prompt = &schema.PromptInfo{ID: "p-0"}
	events[2].Command = &schema.CommandInfo{Command: "go test ./..."}
	events[2].Prompt = &schema.PromptInfo{ID: "p-1"}
	events[3].Timestamp = "2026-05-13T01:00:03Z"
	for i := range events {
		events[i].Session = &schema.SessionInfo{ID: "session-a"}
	}
	writeTestLog(t, path, marshalEvents(t, events...)...)

	result, err := ReadSessions(path, EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadSessions returned error: %v", err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(result.Sessions))
	}
	session := result.Sessions[0]
	if session.OriginalPrompt != "Investigate the dashboard" || session.PromptCount != 1 {
		t.Fatalf("session prompt = %q count %d, want the prompt's text once", session.OriginalPrompt, session.PromptCount)
	}
}
