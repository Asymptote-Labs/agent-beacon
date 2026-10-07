package beaconevent

import (
	"fmt"
	"testing"
	"time"
)

func listing(session, tool, description string) Event {
	event := NewEvent("mcp.tool_listed", "mcp", "info", "claude_code", time.Unix(1, 0).UTC())
	event.Session = &SessionInfo{ID: session}
	event.GenAI = &GenAIInfo{Tool: &GenAIToolInfo{Name: tool, Description: description}}
	return event
}

// Claude Code repeats its tools array on every model request. A session's listing is kept the first
// time and again only when the description changes; another session gets its own, and nothing that
// is not a listing is touched. Each batch is remembered once written, the way the exporters do.
func TestMCPListingsKeepsOnePerToolPerSessionAndDescription(t *testing.T) {
	var listings MCPListings
	other := NewEvent("session.activity", "session", "info", "claude_code", time.Unix(1, 0).UTC())
	batches := [][]Event{
		{listing("s1", "mcp__notes__save", "Saves a note."), other},
		{listing("s1", "mcp__notes__save", "Saves a note."), other},
		{listing("s1", "mcp__notes__save", "Saves a note. Now poisoned.")},
		{listing("s2", "mcp__notes__save", "Saves a note.")},
	}
	var got []string
	for _, batch := range batches {
		kept := listings.Filter(batch)
		for _, event := range kept {
			if event.GenAI != nil {
				got = append(got, event.Session.ID+":"+event.GenAI.Tool.Description)
			} else {
				got = append(got, event.Event.Action)
			}
		}
		listings.Remember(kept...)
	}
	want := []string{
		"s1:Saves a note.", "session.activity",
		"session.activity",
		"s1:Saves a note. Now poisoned.",
		"s2:Saves a note.",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
}

// Two requests in one batch advertise the same tool; only the first listing is kept.
func TestMCPListingsDropsARepeatWithinOneBatch(t *testing.T) {
	var listings MCPListings
	batch := []Event{listing("s1", "mcp__notes__save", "Saves a note."), listing("s1", "mcp__notes__save", "Saves a note.")}
	if kept := listings.Filter(batch); len(kept) != 1 {
		t.Fatalf("kept %d of a repeated listing within one batch, want 1", len(kept))
	}
}

// A listing whose write or send failed is never remembered, so the session's next request lists it
// again rather than taking it for one already delivered.
func TestMCPListingsListsAgainAfterAFailedWrite(t *testing.T) {
	var listings MCPListings
	batch := []Event{listing("s1", "mcp__notes__save", "Saves a note.")}
	if kept := listings.Filter(batch); len(kept) != 1 {
		t.Fatalf("first listing dropped")
	}
	kept := listings.Filter(batch)
	if len(kept) != 1 {
		t.Fatal("a listing that was never written was dropped as a repeat")
	}
	listings.Remember(kept...)
	if kept := listings.Filter(batch); len(kept) != 0 {
		t.Fatal("a written listing was kept again")
	}
}

func TestMCPListingsForgetsTheOldestPastItsBound(t *testing.T) {
	var listings MCPListings
	for i := range maxMCPListings + 1 {
		listings.Remember(listing(fmt.Sprintf("s%d", i), "mcp__notes__save", "Saves a note."))
	}
	if len(listings.seen) != maxMCPListings {
		t.Fatalf("remembered %d listings, want the bound %d", len(listings.seen), maxMCPListings)
	}
	if kept := listings.Filter([]Event{listing("s0", "mcp__notes__save", "Saves a note.")}); len(kept) != 1 {
		t.Fatalf("the evicted oldest session's listing was dropped as a repeat")
	}
}

func TestNilMCPListingsKeepsEverything(t *testing.T) {
	var listings *MCPListings
	batch := []Event{listing("s1", "mcp__notes__save", "Saves a note."), listing("s1", "mcp__notes__save", "Saves a note.")}
	if kept := listings.Filter(batch); len(kept) != 2 {
		t.Fatalf("nil MCPListings kept %d of 2 events", len(kept))
	}
	listings.Remember(batch...)
}
