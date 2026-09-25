package openclawsession

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMapTraceRecordsAHandoffLink(t *testing.T) {
	ref := TraceRef{ID: "new-1", Profile: "main", SourcePath: "/state/agents/main/sessions/new-1.jsonl", Directory: "/work"}
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	records := []Record{
		{Order: 1, NativeID: "s0:session", Type: "session_started", Content: "/work"},
		{Order: 2, NativeID: "u1:user", Type: "user_message", Role: "user", Content: prompt},
		{Order: 3, NativeID: "u2:user", Type: "user_message", Role: "user", Content: "no marker here"},
	}
	mapped := MapTrace(ref, records, MapOptions{})
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
	if link.SourceOrder != 2 || link.Event.Handoff == nil || link.Event.Handoff.SourceHarness != "claude_code" || link.Event.Handoff.SourceSessionID != "src-1" {
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
	again := MapTrace(ref, records, MapOptions{})
	for i := range mapped {
		if again[i].Event.Event.ID != mapped[i].Event.Event.ID {
			t.Fatal("event ids must be deterministic, so a second sync never duplicates the link")
		}
	}
}
