package dashboard

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestReadEventsAppendOrderBreaksTimestampTiesOnSequence(t *testing.T) {
	// A batch of cumulative datapoints from one export shares a timestamp however precise
	// the timestamp is, and token aggregation needs their emission order back. The file
	// deliberately holds them in the wrong order to prove the sequence is what recovers it
	// rather than the line number.
	tie := "2026-05-13T01:00:00.000000000Z"
	first := testSchemaEvent(tie, "claude_code", "metric.observed", "metric", "")
	first.Sequence = 11
	first.Message = "first"
	second := testSchemaEvent(tie, "claude_code", "metric.observed", "metric", "")
	second.Sequence = 12
	second.Message = "second"

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, marshalEvents(t, second, first)...)

	events, err := ReadEventsAppendOrder(path, EventQuery{})
	if err != nil {
		t.Fatalf("ReadEventsAppendOrder returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	if events[0].Message != "first" || events[1].Message != "second" {
		t.Fatalf("order = %q, %q; want first, second", events[0].Message, events[1].Message)
	}
}

func TestReadEventsAppendOrderFallsBackToLineOrderWhenUnsequenced(t *testing.T) {
	// Events written before sequence stamping still have to come back in append order.
	tie := "2026-05-13T01:00:00Z"
	earlier := testSchemaEvent(tie, "claude_code", "metric.observed", "metric", "")
	earlier.Message = "line-1"
	later := testSchemaEvent(tie, "claude_code", "metric.observed", "metric", "")
	later.Message = "line-2"

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, marshalEvents(t, earlier, later)...)

	events, err := ReadEventsAppendOrder(path, EventQuery{})
	if err != nil {
		t.Fatalf("ReadEventsAppendOrder returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	if events[0].Message != "line-1" || events[1].Message != "line-2" {
		t.Fatalf("order = %q, %q; want line-1, line-2", events[0].Message, events[1].Message)
	}
}

func TestBuildSummaryPicksLastEventByInstantNotByString(t *testing.T) {
	// A log spanning the move to nanosecond stamping: ":05Z" is the older event but sorts
	// after ":05.500000000Z" byte-wise, because 'Z' outranks '.'.
	older := testSchemaEvent("2026-05-13T01:00:05Z", "cursor", "tool.started", "tool", "")
	newer := testSchemaEvent("2026-05-13T01:00:05.500000000Z", "cursor", "tool.completed", "tool", "")

	summary := BuildSummary(EventResult{
		TotalMatched: 2,
		Events:       []EventRecord{{Event: older}, {Event: newer}},
	})
	if summary.LastEventTime != newer.Timestamp {
		t.Fatalf("LastEventTime = %q, want %q", summary.LastEventTime, newer.Timestamp)
	}
}

func TestBuildSummarySkipsUnparseableTimestamps(t *testing.T) {
	real := testSchemaEvent("2026-05-13T01:00:05.000000000Z", "cursor", "tool.started", "tool", "")
	broken := testSchemaEvent("whenever", "cursor", "tool.completed", "tool", "")

	summary := BuildSummary(EventResult{
		TotalMatched: 2,
		Events:       []EventRecord{{Event: real}, {Event: broken}},
	})
	if summary.LastEventTime != real.Timestamp {
		t.Fatalf("LastEventTime = %q, want %q", summary.LastEventTime, real.Timestamp)
	}
}

func TestStreamEventsCarriesSequenceThrough(t *testing.T) {
	// Detection reads the log through StreamEvents, so the ordering key has to survive
	// the round trip to be of any use to the sort in front of the rules engine.
	event := testSchemaEvent("2026-05-13T01:00:00.000000000Z", "claude_code", "command.executed", "command", "")
	event.Sequence = 7

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, marshalEvents(t, event)...)

	var got []schema.Event
	if err := StreamEvents(path, func(e schema.Event) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("StreamEvents returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("streamed %d events, want 1", len(got))
	}
	if got[0].Sequence != 7 {
		t.Fatalf("sequence = %d, want 7", got[0].Sequence)
	}
}
