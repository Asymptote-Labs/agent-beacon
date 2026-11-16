package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
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
	// A record Beacon writes about an old session itself (a share, a commit link) has no collection
	// method, and is not activity in that session.
	appendSessionEvent(t, logPath, "codex", "", "shared-old")
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

// liveTraceClock is the time the scans in these tests run at.
var liveTraceClock = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// liveTraceHarness stubs the stored position, the clock and what recordLocalTraces sends.
func liveTraceHarness(t *testing.T, connected bool) (calls *[]string, pos *telemetry.LiveTracePosition, set *bool) {
	t.Helper()
	recordTelemetry(t)
	calls = recordTraces(t, connected)
	pos, set = new(telemetry.LiveTracePosition), new(bool)
	oldGet, oldSet, oldNow := telemetryLiveTrace, telemetrySetLiveTrace, liveTraceNow
	telemetryLiveTrace = func() (telemetry.LiveTracePosition, bool) { return *pos, *set }
	telemetrySetLiveTrace = func(v telemetry.LiveTracePosition) { *pos, *set = v, true }
	liveTraceNow = func() time.Time { return liveTraceClock }
	t.Cleanup(func() { telemetryLiveTrace, telemetrySetLiveTrace, liveTraceNow = oldGet, oldSet, oldNow })
	return calls, pos, set
}

// readUpTo stores a position at the end of the log, saved an hour before the scan.
func readUpTo(logPath string, pos *telemetry.LiveTracePosition, set *bool) {
	*pos = telemetry.LiveTracePosition{Offset: logSize(logPath), LogID: runtimeLogID(logPath), SavedAt: liveTraceClock.Add(-time.Hour)}
	*set = true
}

// rotate moves the log to path.1, the newer archives up first, as RotateRuntimeLog does.
func rotate(t *testing.T, logPath string) {
	t.Helper()
	for i := maxLiveTraceArchives - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", logPath, i)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, fmt.Sprintf("%s.%d", logPath, i+1)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordLiveTracesReportsNewSessionsFromEveryCollector(t *testing.T) {
	calls, pos, _ := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "history")

	// The first run only records where the log ends: history is not live.
	recordLiveTraces(logPath, true)
	if len(*calls) != 0 || pos.Offset != logSize(logPath) || pos.LogID != runtimeLogID(logPath) || !pos.SavedAt.Equal(liveTraceClock) {
		t.Fatalf("first run recorded %v, position %+v", *calls, *pos)
	}

	pos.SavedAt = liveTraceClock.Add(-6 * time.Hour)
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	appendSessionEvent(t, logPath, "codex", schema.CollectionMethodPoll, "p1")
	recordLiveTraces(logPath, true)
	want := []string{"live:claude_code/h1", "live:codex/p1"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("recorded %v, want %v", *calls, want)
	}
	if pos.Offset != logSize(logPath) {
		t.Fatalf("offset %d, want the log's end %d", pos.Offset, logSize(logPath))
	}

	// Nothing new, nothing sent.
	recordLiveTraces(logPath, true)
	if len(*calls) != 2 {
		t.Fatalf("a run with nothing new recorded %v", *calls)
	}
}

func TestRecordLiveTracesFollowsTheLogThroughARotation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// grow makes the new live log longer than the old offset, so a check on size alone would
		// seek into the middle of it.
		grow bool
	}{{"new log shorter than the offset", false}, {"new log longer than the offset", true}} {
		t.Run(tc.name, func(t *testing.T) {
			calls, pos, set := liveTraceHarness(t, false)
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "history")
			readUpTo(logPath, pos, set)
			appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "before-rotation")
			rotate(t, logPath)
			appendSessionEvent(t, logPath, "codex", schema.CollectionMethodHook, "after-rotation")
			want := []string{"live:claude_code/before-rotation", "live:codex/after-rotation"}
			if tc.grow {
				for i := 0; logSize(logPath) <= pos.Offset+512; i++ {
					appendSessionEvent(t, logPath, "codex", schema.CollectionMethodHook, "after-rotation")
				}
			}

			recordLiveTraces(logPath, true)
			if !reflect.DeepEqual(*calls, want) {
				t.Fatalf("recorded %v, want %v", *calls, want)
			}
			if pos.Offset != logSize(logPath) || pos.LogID != runtimeLogID(logPath) {
				t.Fatalf("position %+v, want the new log's end %d", *pos, logSize(logPath))
			}
		})
	}
}

func TestRecordLiveTracesReadsEveryArchiveNewerThanTheLastRead(t *testing.T) {
	calls, pos, set := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "history")
	readUpTo(logPath, pos, set)
	rotate(t, logPath)
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "middle")
	rotate(t, logPath)
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "newest")

	recordLiveTraces(logPath, true)
	want := []string{"live:claude_code/middle", "live:claude_code/newest"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("recorded %v, want %v", *calls, want)
	}
}

func TestRecordLiveTracesSkipsAStaleScan(t *testing.T) {
	calls, pos, set := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "history")
	readUpTo(logPath, pos, set)
	pos.SavedAt = liveTraceClock.Add(-maxLiveTraceGap - time.Minute)
	// Telemetry was off for days: the log rotated and its new file is short, which a size check
	// alone would read from the start.
	rotate(t, logPath)
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "old-while-off")

	recordLiveTraces(logPath, true)
	if len(*calls) != 0 {
		t.Fatalf("a stale scan recorded %v", *calls)
	}
	if pos.Offset != logSize(logPath) || !pos.SavedAt.Equal(liveTraceClock) {
		t.Fatalf("position %+v, want the log's end now", *pos)
	}
}

func TestRecordLiveTracesOnlyMovesTheOffsetWhenConnected(t *testing.T) {
	calls, pos, set := liveTraceHarness(t, true)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "history")
	readUpTo(logPath, pos, set)
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")

	recordLiveTraces(logPath, true)
	if len(*calls) != 0 {
		t.Fatalf("a connected endpoint recorded %v", *calls)
	}
	if pos.Offset != logSize(logPath) {
		t.Fatalf("offset %d, want %d", pos.Offset, logSize(logPath))
	}
}

func TestRecordLiveTracesSkipsALongGap(t *testing.T) {
	calls, pos, set := liveTraceHarness(t, false)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendSessionEvent(t, logPath, "claude_code", schema.CollectionMethodHook, "h1")
	readUpTo(logPath, pos, set)
	pos.Offset = 0
	if err := os.Truncate(logPath, maxLiveTraceScanBytes+1); err != nil {
		t.Fatal(err)
	}

	recordLiveTraces(logPath, true)
	if len(*calls) != 0 || pos.Offset != maxLiveTraceScanBytes+1 {
		t.Fatalf("a long gap recorded %v, offset %d", *calls, pos.Offset)
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
