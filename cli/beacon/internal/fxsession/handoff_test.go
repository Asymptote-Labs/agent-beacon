package fxsession

import (
	"encoding/json"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func promptTurnLine(t *testing.T, seq int, prompt string) string {
	t.Helper()
	text, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"conversation_language":"en","total_input_tokens":0,"total_output_tokens":0,"work_id":"w",` +
		`"turn":{"kind":"assistant","user":{"text":` + string(text) + `,"images":[]},"assistant":"On it.","execution":null}}`
	return frame(seq, 1770000000000+int64(seq)*1000, KindHistoryTurnCommitted, payload)
}

func TestMapSessionRecordsAHandoffLink(t *testing.T) {
	ref := SessionRef{ID: testSessionID, Manifest: &Manifest{ID: testSessionID, WorkspaceRoot: "/work"}}
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	var events []Event
	for i, line := range []string{promptTurnLine(t, 1, prompt), promptTurnLine(t, 2, "no marker here")} {
		event, err := DecodeEnvelope([]byte(line))
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		events = append(events, *event)
	}
	mapped := MapSession(ref, events, MapOptions{})
	var links []MappedEvent
	for _, m := range mapped {
		if m.Event.Event.Action == "session.handoff" {
			links = append(links, m)
		}
	}
	if len(links) != 1 {
		t.Fatalf("links = %d, want 1: %+v", len(links), mapped)
	}
	link := links[0]
	if link.SourceSeq != 1 || link.Event.Handoff == nil || link.Event.Handoff.SourceHarness != "claude_code" || link.Event.Handoff.SourceSessionID != "src-1" {
		t.Fatalf("link = %+v handoff=%+v", link, link.Event.Handoff)
	}
	if link.Event.Session == nil || link.Event.Session.ID != testSessionID || link.Event.Session.WorkingDirectory != "/work" {
		t.Fatalf("link session = %+v", link.Event.Session)
	}
	if link.Event.Harness.Name != Harness || link.Event.Harness.CollectionMethod != "poll" || link.Event.Event.Fidelity != "observed" {
		t.Fatalf("link provenance = %+v %+v", link.Event.Harness, link.Event.Event)
	}
	if link.Event.Prompt != nil || link.Event.Content != nil || (link.Event.GenAI != nil && link.Event.GenAI.Input != nil) {
		t.Fatalf("the link must not repeat the prompt: %+v", link.Event)
	}
	ids := map[string]bool{}
	for _, m := range mapped {
		if ids[m.Event.Event.ID] {
			t.Fatalf("duplicate event id %s", m.Event.Event.ID)
		}
		ids[m.Event.Event.ID] = true
	}
	again := MapSession(ref, events, MapOptions{})
	for i := range mapped {
		if again[i].Event.Event.ID != mapped[i].Event.Event.ID {
			t.Fatal("event ids must be deterministic, so a second sync never duplicates the link")
		}
	}
}
