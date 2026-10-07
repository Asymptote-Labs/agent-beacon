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
// is not a listing is touched.
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
		for _, event := range listings.Filter(batch) {
			if event.GenAI != nil {
				got = append(got, event.Session.ID+":"+event.GenAI.Tool.Description)
			} else {
				got = append(got, event.Event.Action)
			}
		}
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

func TestMCPListingsForgetsTheOldestPastItsBound(t *testing.T) {
	var listings MCPListings
	for i := range maxMCPListings + 1 {
		listings.Filter([]Event{listing(fmt.Sprintf("s%d", i), "mcp__notes__save", "Saves a note.")})
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
}
