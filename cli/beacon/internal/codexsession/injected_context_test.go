package codexsession

import (
	"strings"
	"testing"
)

const (
	environmentContext = "<environment_context>\n  <cwd>/tmp/repo</cwd>\n  <shell>zsh</shell>\n</environment_context>"
	agentsInstructions = "# AGENTS.md instructions for /tmp/repo\n\n<INSTRUCTIONS>\nRun the tests before committing.\n</INSTRUCTIONS>"
	userInstructions   = "<user_instructions>\nPrefer small commits.\n</user_instructions>"
)

// userMessageLine writes a user-role message with one block per text and, when kinds is not nil,
// the content_item_kinds newer Codex builds attach.
func userMessageLine(id string, kinds []string, texts ...string) string {
	blocks := make([]string, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, `{"type":"input_text","text":`+quote(text)+`}`)
	}
	passthrough := ""
	if kinds != nil {
		quoted := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			quoted = append(quoted, quote(kind))
		}
		passthrough = `,"internal_chat_message_metadata_passthrough":{"turn_id":"turn-1","content_item_kinds":[` + strings.Join(quoted, ",") + `]}`
	}
	return `{"timestamp":"2026-09-19T22:00:02.000Z","type":"response_item","payload":{"type":"message","id":` + quote(id) + `,"role":"user","content":[` + strings.Join(blocks, ",") + `]` + passthrough + `}}`
}

func prompts(mapped []MappedEvent) []MappedEvent {
	var out []MappedEvent
	for _, item := range mapped {
		if item.Event.Event.Action == "prompt.submitted" {
			out = append(out, item)
		}
	}
	return out
}

// Codex writes the environment, AGENTS.md and the person's instructions as user-role messages.
// Recording them as prompts titled every backfilled session after them, so only what the person
// typed is a prompt.
func TestInjectedContextIsNotAPrompt(t *testing.T) {
	ref := SessionRef{ID: "codex-1", Path: "/tmp/codex-1.jsonl", Workspace: "/tmp/repo"}
	records := decodeFixture(t, []string{
		sessionMetaLine("codex-1", "/tmp/repo"),
		turnStartedLine("turn-1"),
		turnContextLine("turn-1", "gpt-5.3-codex"),
		userMessageLine("u0", nil, environmentContext),
		userMessageLine("u1", nil, agentsInstructions),
		userMessageLine("u2", nil, userInstructions),
		messageLine("user", "u3", "run the tests"),
		messageLine("assistant", "a1", "Tests pass."),
	})

	got := prompts(MapSession(ref, records, MapOptions{}))
	if len(got) != 1 {
		t.Fatalf("prompts = %d, want only the one the person typed", len(got))
	}
	prompt := got[0].Event
	if prompt.Prompt.Text != "run the tests" {
		t.Fatalf("prompt text = %q, want the typed prompt", prompt.Prompt.Text)
	}
	if prompt.Prompt.ID != "turn-1" {
		t.Fatalf("prompt id = %q, want turn-1", prompt.Prompt.ID)
	}
}

// Newer Codex builds name each block's kind. A message whose kinds include no "user.text" is
// injected context even when its text opens with something Beacon does not recognize, and a
// message that mixes both keeps only what the person typed.
func TestContentItemKindsDecideWhatIsAPrompt(t *testing.T) {
	ref := SessionRef{ID: "codex-1", Path: "/tmp/codex-1.jsonl", Workspace: "/tmp/repo"}
	records := decodeFixture(t, []string{
		sessionMetaLine("codex-1", "/tmp/repo"),
		turnStartedLine("turn-1"),
		turnContextLine("turn-1", "gpt-5.3-codex"),
		userMessageLine("u0", []string{"agents_md.instructions", "environments.environment_context"}, agentsInstructions, environmentContext),
		userMessageLine("u1", []string{"environments.future_context"}, "<future_context>a block Beacon has never seen</future_context>"),
		userMessageLine("u2", []string{"user.text"}, "tell me about this repo"),
		userMessageLine("u3", []string{"environments.environment_context", "user.text"}, environmentContext, "and then the tests"),
	})

	var texts []string
	for _, item := range prompts(MapSession(ref, records, MapOptions{})) {
		texts = append(texts, item.Event.Prompt.Text)
	}
	want := []string{"tell me about this repo", "and then the tests"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("prompts = %q, want %q", texts, want)
	}
}

// The passthrough metadata is Codex's own and may change shape. One Beacon cannot read is ignored
// rather than making the record unreadable.
func TestUnreadablePassthroughKeepsThePrompt(t *testing.T) {
	ref := SessionRef{ID: "codex-1", Path: "/tmp/codex-1.jsonl", Workspace: "/tmp/repo"}
	line := `{"timestamp":"2026-09-19T22:00:02.000Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"run the tests"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":"user.text"}}}`
	records := decodeFixture(t, []string{sessionMetaLine("codex-1", "/tmp/repo"), line})

	got := prompts(MapSession(ref, records, MapOptions{}))
	if len(got) != 1 || got[0].Event.Prompt.Text != "run the tests" {
		t.Fatalf("prompts = %+v, want the typed prompt", got)
	}
}
