package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

func appendSessionEvent(t *testing.T, logPath, harnessName, method, session string) {
	t.Helper()
	event := schema.NewEvent(schema.NewEventOptions{
		Action: "prompt.submitted", Category: "agent", Severity: schema.SeverityInfo,
		Harness: schema.HarnessInfo{Name: harnessName, CollectionMethod: method},
	})
	event.Session = &schema.SessionInfo{ID: session}
	if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: true}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsInLogCountsEveryCollectionMethod(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	appendSessionEvent(t, logPath, "codex", schema.CollectionMethodPoll, "p1")
	appendSessionEvent(t, logPath, "gemini_cli", "otlp", "o1")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	end := logSize(logPath)
	// A line still being written is left for the next read.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"harness":{"name":"cursor"},"session":{"id":"partial"}`)
	_ = f.Close()

	got, next := sessionsInLog(logPath, 0, "")
	want := []sweptSession{{"claude_code", "h1"}, {"codex", "p1"}, {"gemini_cli", "o1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %+v, want %+v", got, want)
	}
	if next != end {
		t.Fatalf("next offset = %d, want %d (before the partial line)", next, end)
	}
}

// liveTraceHarness stubs the stored offset and what recordLocalTraces sends.
func liveTraceHarness(t *testing.T, connected bool) (calls *[]string, offset *int64, set *bool) {
	t.Helper()
	recordTelemetry(t)
	calls = recordTraces(t, connected)
	offset, set = new(int64), new(bool)
	oldGet, oldSet := telemetryLiveTraceOffset, telemetrySetLiveTraceOffset
	telemetryLiveTraceOffset = func() (int64, bool) { return *offset, *set }
	telemetrySetLiveTraceOffset = func(v int64) { *offset, *set = v, true }
	t.Cleanup(func() { telemetryLiveTraceOffset, telemetrySetLiveTraceOffset = oldGet, oldSet })
	return calls, offset, set
}

func TestRecordLiveTracesReportsNewSessionsFromEveryCollector(t *testing.T) {
	calls, offset, _ := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "history")

	// The first run only records where the log ends: history is not live.
	recordLiveTraces(logPath, true)
	if len(*calls) != 0 || *offset != logSize(logPath) {
		t.Fatalf("first run recorded %v, offset %d", *calls, *offset)
	}

	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	appendSessionEvent(t, logPath, "codex", schema.CollectionMethodPoll, "p1")
	recordLiveTraces(logPath, true)
	want := []string{"live:claude_code/h1", "live:codex/p1"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("recorded %v, want %v", *calls, want)
	}
	if *offset != logSize(logPath) {
		t.Fatalf("offset %d, want the log's end %d", *offset, logSize(logPath))
	}

	// Nothing new, nothing sent.
	recordLiveTraces(logPath, true)
	if len(*calls) != 2 {
		t.Fatalf("a run with nothing new recorded %v", *calls)
	}
}

func TestRecordLiveTracesReadsARotatedLogFromItsStart(t *testing.T) {
	calls, offset, set := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "after-rotation")
	*offset, *set = logSize(logPath)+1000, true

	recordLiveTraces(logPath, true)
	if !reflect.DeepEqual(*calls, []string{"live:claude_code/after-rotation"}) {
		t.Fatalf("recorded %v", *calls)
	}
}

func TestRecordLiveTracesOnlyMovesTheOffsetWhenConnected(t *testing.T) {
	calls, offset, set := liveTraceHarness(t, true)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	*offset, *set = 0, true
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")

	recordLiveTraces(logPath, true)
	if len(*calls) != 0 {
		t.Fatalf("a connected endpoint recorded %v", *calls)
	}
	if *offset != logSize(logPath) {
		t.Fatalf("offset %d, want %d", *offset, logSize(logPath))
	}
}

func TestRecordLiveTracesSkipsALongGap(t *testing.T) {
	calls, offset, set := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	*offset, *set = 0, true
	if err := os.Truncate(logPath, maxLiveTraceScanBytes+1); err != nil {
		t.Fatal(err)
	}

	recordLiveTraces(logPath, true)
	if len(*calls) != 0 || *offset != maxLiveTraceScanBytes+1 {
		t.Fatalf("a long gap recorded %v, offset %d", *calls, *offset)
	}
}

func TestRecordLiveTracesDoesNothingWithTelemetryOff(t *testing.T) {
	calls, _, set := liveTraceHarness(t, false)
	endpointSweepRecordsSessions = func() bool { return false }
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	recordLiveTraces(logPath, true)
	if len(*calls) != 0 || *set {
		t.Fatalf("telemetry off recorded %v or moved the offset", *calls)
	}
}
