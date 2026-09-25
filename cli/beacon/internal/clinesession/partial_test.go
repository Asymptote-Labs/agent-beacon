package clinesession

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestRecordCompleteWaitsForTheRecordsLastEvent(t *testing.T) {
	// Record 2 maps to a prompt and its session.handoff link.
	mapped := []MappedEvent{{SourceOrder: 1}, {SourceOrder: 2}, {SourceOrder: 2}, {SourceOrder: 3}}
	want := []bool{true, false, true, true}
	for i := range mapped {
		if got := recordComplete(mapped, i); got != want[i] {
			t.Fatalf("recordComplete(%d) = %v, want %v", i, got, want[i])
		}
	}
}

// A sync whose session.handoff write fails after the prompt from the same record was written
// retries that record, so the link is not lost.
func TestCollectOnceRetriesARecordWhoseLinkFailedToWrite(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	f := newSweepFixture(t)
	sessionDir := filepath.Join(f.clineDir, "data", "sessions", "new-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	writeJSON(t, filepath.Join(sessionDir, "new-1.json"), map[string]interface{}{"task": "Continue", "workspacePath": "/repo"})
	writeJSON(t, filepath.Join(sessionDir, "new-1.messages.json"), map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": prompt},
	}})

	prev := emitEvent
	t.Cleanup(func() { emitEvent = prev })
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if event.Event.Action == "session.handoff" {
			return errors.New("disk full")
		}
		return prev(event, opts)
	}
	if _, err := CollectOnce(f.options()); err == nil {
		t.Fatal("the failed link write must surface")
	}
	emitEvent = prev
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	links := 0
	for _, e := range f.logLines(t) {
		if e.Event.Action == "session.handoff" {
			links++
		}
	}
	if links != 1 {
		t.Fatalf("session.handoff events after the retry = %d, want 1", links)
	}
}

// A kanban card is re-read by content hash rather than order, so the hash must also wait for the
// card's last event: a card whose link failed to write is retried, not marked seen.
func TestCollectOnceRetriesAKanbanCardWhoseLinkFailedToWrite(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	f := newSweepFixture(t)
	f.writeKanbanCard(t, "Continue the work.\n\n"+asymptoteobserve.HandoffMarker("claude_code", "src-1"), "", "2026-01-01T00:01:00Z")

	prev := emitEvent
	t.Cleanup(func() { emitEvent = prev })
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if event.Event.Action == "session.handoff" {
			return errors.New("disk full")
		}
		return prev(event, opts)
	}
	if _, err := CollectOnce(f.options()); err == nil {
		t.Fatal("the failed link write must surface")
	}
	emitEvent = prev
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	links := 0
	for _, e := range f.logLines(t) {
		if e.Event.Action == "session.handoff" {
			links++
		}
	}
	if links != 1 {
		t.Fatalf("session.handoff events after the retry = %d, want 1", links)
	}
}
