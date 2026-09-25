package factorysession

import (
	"encoding/json"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMapSessionRecordsAHandoffLink(t *testing.T) {
	ref := SessionRef{ID: "new-1", Path: "/tmp/new-1.jsonl", CWD: "/work"}
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	marked, err := json.Marshal(map[string]interface{}{"type": "message", "id": "u1", "message": map[string]interface{}{
		"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "text", "text": "an unrelated block"},
			map[string]interface{}{"type": "text", "text": prompt},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		mustRecord(t, 1, `{"type":"session_start","id":"new-1","cwd":"/work"}`),
		mustRecord(t, 2, string(marked)),
		mustRecord(t, 3, `{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"no marker here"}]}}`),
	}
	mapped := MapSession(ref, records, MapOptions{})
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
	if link.SourceLine != 2 || link.Event.Handoff == nil || link.Event.Handoff.SourceHarness != "claude_code" || link.Event.Handoff.SourceSessionID != "src-1" {
		t.Fatalf("link = %+v handoff=%+v", link, link.Event.Handoff)
	}
	if link.Event.Session == nil || link.Event.Session.ID != "new-1" || link.Event.Session.WorkingDirectory != "/work" {
		t.Fatalf("link session = %+v", link.Event.Session)
	}
	if link.Event.Harness.Name != Harness || link.Event.Harness.CollectionMethod != "poll" || link.Event.Event.Fidelity != "observed" {
		t.Fatalf("link provenance = %+v %+v", link.Event.Harness, link.Event.Event)
	}
	if link.Event.Prompt != nil || link.Event.Content != nil || link.Event.GenAI != nil {
		t.Fatalf("the link must not repeat the prompt: %+v", link.Event)
	}
	ids := map[string]bool{}
	for _, m := range mapped {
		if ids[m.Event.Event.ID] {
			t.Fatalf("duplicate event id %s", m.Event.Event.ID)
		}
		ids[m.Event.Event.ID] = true
	}
	again := MapSession(ref, records, MapOptions{})
	for i := range mapped {
		if again[i].Event.Event.ID != mapped[i].Event.Event.ID {
			t.Fatal("event ids must be deterministic, so a second sync never duplicates the link")
		}
	}
}
