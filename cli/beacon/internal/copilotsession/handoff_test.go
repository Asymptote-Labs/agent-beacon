package copilotsession

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMapSessionRecordsAHandoffLink(t *testing.T) {
	ref := SessionRef{ID: "new-1", Path: "/tmp/session-state/new-1/events.jsonl", Meta: &WorkspaceMeta{ID: "new-1", CWD: "/work"}}
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	records := []Record{
		{Line: 1, Type: "session.start", Data: map[string]interface{}{"sessionId": "new-1", "context": map[string]interface{}{"cwd": "/work"}}},
		{Line: 2, Type: "user.message", Data: map[string]interface{}{"content": prompt, "transformedContent": "<context>\n" + prompt}},
		{Line: 3, Type: "user.message", Data: map[string]interface{}{"content": "no marker here"}},
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
	if link.Event.Prompt != nil || link.Event.Content != nil || link.Event.GenAI != nil || link.Event.Raw != nil {
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
