package factorysession

import (
	"encoding/json"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestMapSessionFactoryRecords(t *testing.T) {
	ref := SessionRef{
		ID:    "session-1",
		Path:  "/tmp/session-1.jsonl",
		CWD:   "/repo",
		Model: "claude-opus-4-7",
		Settings: &Settings{TokenUsage: &TokenUsage{
			InputTokens:         10,
			OutputTokens:        20,
			CacheCreationTokens: 30,
			CacheReadTokens:     40,
			ThinkingTokens:      5,
			FactoryCredits:      12.5,
		}},
		SettingsUnixMS: 1770000001000,
	}
	records := []Record{
		mustRecord(t, 1, `{"type":"session_start","id":"session-1","title":"hello","cwd":"/repo"}`),
		mustRecord(t, 2, `{"type":"message","id":"u1","timestamp":"2026-05-16T22:11:04.611Z","message":{"role":"user","content":[{"type":"text","text":"<user_query>\nship it\n</user_query>"}]}}`),
		mustRecord(t, 3, `{"type":"message","id":"a1","timestamp":"2026-05-16T22:11:05.611Z","message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","thinking":"checking"},{"type":"text","text":"done"},{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"go test ./..."}},{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}`),
		mustRecord(t, 4, `{"type":"compaction_state","id":"c1","timestamp":"2026-05-16T22:11:06.611Z","summaryText":"older work","summaryTokens":123,"anchorMessage":{"id":"u1"}}`),
	}

	mapped := MapSession(ref, records, MapOptions{EmitSettingsUsage: true})
	actions := make([]string, 0, len(mapped))
	for _, item := range mapped {
		actions = append(actions, item.Event.Event.Action)
		if item.Event.Harness.Name != Harness {
			t.Fatalf("harness = %q, want %q", item.Event.Harness.Name, Harness)
		}
		if item.Event.Harness.CollectionMethod != "poll" {
			t.Fatalf("collection_method = %q, want poll", item.Event.Harness.CollectionMethod)
		}
	}
	want := []string{"session.started", "prompt.submitted", "agent.reasoning", "agent.message", "tool.invoked", "tool.completed", "session.compacted", "token.usage"}
	if !equalStrings(actions, want) {
		t.Fatalf("actions = %#v, want %#v", actions, want)
	}
	if got := findAction(t, mapped, "prompt.submitted").Prompt.Text; got != "ship it" {
		t.Fatalf("prompt = %q, want stripped user_query text", got)
	}
	tool := findAction(t, mapped, "tool.invoked")
	if tool.Tool == nil || tool.Tool.Name != "Bash" {
		t.Fatalf("tool = %#v, want Bash", tool.Tool)
	}
	usage := findAction(t, mapped, "token.usage")
	if usage.GenAI == nil || usage.GenAI.Usage == nil || usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 10 {
		t.Fatalf("usage = %#v, want input tokens", usage.GenAI)
	}
}

func mustRecord(t *testing.T, line int, text string) Record {
	t.Helper()
	var record Record
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		t.Fatal(err)
	}
	record.Line = line
	record.Raw = []byte(text)
	return record
}

func findAction(t *testing.T, mapped []MappedEvent, action string) *schema.Event {
	t.Helper()
	for i := range mapped {
		if mapped[i].Event.Event.Action == action {
			return &mapped[i].Event
		}
	}
	t.Fatalf("action %q not found", action)
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
