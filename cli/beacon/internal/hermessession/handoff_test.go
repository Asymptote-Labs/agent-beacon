package hermessession

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMapSessionRecordsAHandoffLink(t *testing.T) {
	session := Session{ID: "new-1", Source: "cli", CWD: "/work", Model: "gpt-5", StartedAtMS: 1780632830000}
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	messages := []Message{
		{ID: 1, SessionID: "new-1", Role: "user", Content: prompt, TimestampMS: 1780632831000, Active: true},
		{ID: 2, SessionID: "new-1", Role: "assistant", Content: "Reading the brief.", TimestampMS: 1780632832000, Active: true},
		{ID: 3, SessionID: "new-1", Role: "user", Content: "no marker here", TimestampMS: 1780632833000, Active: true},
	}
	mapped, _ := MapSession(session, messages, nil)
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
	if link.SourceMessageID != 1 || link.Event.Handoff == nil || link.Event.Handoff.SourceHarness != "claude_code" || link.Event.Handoff.SourceSessionID != "src-1" {
		t.Fatalf("link = %+v handoff=%+v", link, link.Event.Handoff)
	}
	if link.Event.Session == nil || link.Event.Session.ID != "new-1" || link.Event.Session.WorkingDirectory != "/work" {
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
	again, _ := MapSession(session, messages, nil)
	for i := range mapped {
		if again[i].Event.Event.ID != mapped[i].Event.Event.ID {
			t.Fatal("event ids must be deterministic, so a second sync never duplicates the link")
		}
	}
}

// A database from before Hermes recorded git branches still lists, with no branch.
func TestSessionDetailsWithoutAGitBranchColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	createHermesFixtureDB(t, path)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	details, err := store.SessionDetails()
	if err != nil {
		t.Fatalf("SessionDetails: %v", err)
	}
	detail, ok := details["hermes-session-1"]
	if !ok || detail.Branch != "" || detail.DelegateFrom != "" || detail.FirstPrompt != "run tests" || detail.LastMessageAtMS != 1780632833000 {
		t.Fatalf("details = %+v", details)
	}
}
