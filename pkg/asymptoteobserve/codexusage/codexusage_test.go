package codexusage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFileNormalizesAndDedupesTokenCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-29T10:00:00Z","payload":{"id":"codex-session-1","cwd":"/repo"}}`,
		`{"type":"turn_context","timestamp":"2026-06-29T10:00:01Z","payload":{"model":"gpt-5","turn_id":"turn-1"}}`,
		`{"type":"response_item","timestamp":"2026-06-29T10:00:02Z","payload":{"role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		`{"type":"event_msg","timestamp":"2026-06-29T10:00:03Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":50,"cached_input_tokens":600,"reasoning_output_tokens":7,"total_tokens":1050}}}}`,
		`{"type":"event_msg","timestamp":"2026-06-29T10:00:04Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":50,"cached_input_tokens":600,"reasoning_output_tokens":7,"total_tokens":1050}}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	events, err := ParseFile(path, ParseOptions{})
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want 1 unique token event", events)
	}
	event := events[0]
	if event.SessionID != "codex-session-1" || event.WorkingDir != "/repo" || event.Model != "gpt-5" || event.TurnID != "turn-1" {
		t.Fatalf("event metadata = %#v", event)
	}
	if event.InputTokens != 400 || event.CacheReadTokens != 600 || event.OutputTokens != 50 || event.ReasoningTokens != 7 {
		t.Fatalf("normalized tokens = input %d cache %d output %d reasoning %d", event.InputTokens, event.CacheReadTokens, event.OutputTokens, event.ReasoningTokens)
	}
	if event.DedupKey == "" {
		t.Fatal("dedup key is empty")
	}
}

func TestReconcileUsesStateForIdempotence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	statePath := filepath.Join(dir, "state.json")
	line := `{"type":"session_meta","timestamp":"2026-06-29T10:00:00Z","payload":{"id":"codex-session-1"}}` + "\n" +
		`{"type":"turn_context","timestamp":"2026-06-29T10:00:01Z","payload":{"model":"gpt-5","turn_id":"turn-1"}}` + "\n" +
		`{"type":"event_msg","timestamp":"2026-06-29T10:00:03Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"cached_input_tokens":4}}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}

	first, err := Reconcile(ReconcileOptions{Roots: []string{dir}, StatePath: statePath})
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if len(first.Events) != 1 {
		t.Fatalf("first events = %#v, want 1", first.Events)
	}
	if err := MarkEventsSeen(first.Events, statePath); err != nil {
		t.Fatalf("mark seen: %v", err)
	}
	second, err := Reconcile(ReconcileOptions{Roots: []string{dir}, StatePath: statePath})
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(second.Events) != 0 {
		t.Fatalf("second events = %#v, want idempotent empty", second.Events)
	}
}
