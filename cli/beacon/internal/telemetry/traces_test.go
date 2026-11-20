package telemetry

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestRecordTracesSendsOneEventPerNewSession(t *testing.T) {
	s, home := setup(t, nil)
	Init(options(nil, io.Discard))
	RecordTraces(TraceSourceBackfill, []Trace{{"claude_code", "a"}, {"codex_cli", "a"}, {"claude_code", "a"}, {"claude_code", ""}})
	RecordTraces(TraceSourceLive, []Trace{{"claude_code", "a"}, {"cursor", "b"}})
	Close()

	events := s.byEvent(EventTraceRecorded)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	want := []struct {
		harness, source string
		first           bool
	}{
		{"claude_code", TraceSourceBackfill, true},
		{"codex_cli", TraceSourceBackfill, false},
		{"cursor", TraceSourceLive, false},
	}
	for i, w := range want {
		props := properties(t, events[i])
		if props[PropHarness] != w.harness || props[PropSource] != w.source || props[PropIsFirstForDevice] != w.first {
			t.Errorf("event %d = %v, want %+v", i, props, w)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, TracesPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"a"`) || strings.Contains(string(data), "claude_code") {
		t.Fatalf("the seen file holds session IDs or names: %s", data)
	}
	assertNoPII(t, s.all())
}

// Any local process can send OTLP under a service name of its choosing, which reaches the runtime
// log as the harness name. Only names Beacon's own collectors write leave the machine.
func TestRecordTracesSendsOnlyKnownHarnessNames(t *testing.T) {
	s, _ := setup(t, nil)
	Init(options(nil, io.Discard))
	RecordTraces(TraceSourceLive, []Trace{{"acme-billing-service", "a"}, {"Jane's laptop", "b"}, {"codex_cli", "c"}})
	Close()
	var got []any
	for _, event := range s.byEvent(EventTraceRecorded) {
		got = append(got, properties(t, event)[PropHarness])
	}
	if want := []any{TraceHarnessOther, TraceHarnessOther, "codex_cli"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("harness properties = %v, want %v", got, want)
	}
}

// Every name NormalizeHarnessName resolves to is a known one, so a runtime Beacon supports is never
// reported as other.
func TestTraceHarnessesCoverNormalizedNames(t *testing.T) {
	for _, spelling := range []string{"claude", "codex", "codex_desktop", "gemini", "copilot", "vscode", "cowork", "claude_agent_sdk", "claude.ai", "chatgpt", "openclaw", "antigravity", "pi", "omp", "prime", "omo", "cline", "qwen", "kimi", "muse", "dsh", "fx", "harness2", "goose", "kiro", "openhands", "cursor", "opencode", "devin", "devin-cli", "devin-desktop", "factory", "grok", "hermes"} {
		if name := asymptoteobserve.NormalizeHarnessName(spelling); traceHarness(name) != name {
			t.Errorf("%q normalizes to %q, which is reported as %q", spelling, name, TraceHarnessOther)
		}
	}
}

func TestRecordTracesCapsEventsAndRemembersTheRest(t *testing.T) {
	s, _ := setup(t, nil)
	Init(options(nil, io.Discard))
	var traces []Trace
	for i := 0; i < maxTraceEventsPerRun+20; i++ {
		traces = append(traces, Trace{"codex", fmt.Sprint(i)})
	}
	RecordTraces(TraceSourceBackfill, traces)
	RecordTraces(TraceSourceLive, traces)
	Close()
	if got := len(s.byEvent(EventTraceRecorded)); got != maxTraceEventsPerRun {
		t.Fatalf("got %d events, want the cap %d", got, maxTraceEventsPerRun)
	}
}

func TestRecordTracesIsOffWithTelemetry(t *testing.T) {
	s, home := setup(t, nil)
	opts := options(nil, io.Discard)
	opts.Version = "dev"
	Init(opts)
	RecordTraces(TraceSourceLive, []Trace{{"codex", "a"}})
	Close()
	if len(s.all()) != 0 {
		t.Fatal("a development build sent a trace event")
	}
	if _, err := os.Stat(filepath.Join(home, TracesPath)); !os.IsNotExist(err) {
		t.Fatalf("a development build wrote the seen file: %v", err)
	}
}

func TestRecordTracesReportsEachSessionOnceAcrossConcurrentRuns(t *testing.T) {
	s, _ := setup(t, nil)
	Init(options(nil, io.Discard))
	var traces []Trace
	for i := 0; i < 10; i++ {
		traces = append(traces, Trace{"codex", fmt.Sprint(i)})
	}
	// Each goroutine takes the lock through its own open file, as separate processes do.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordTraces(TraceSourceLive, traces)
		}()
	}
	wg.Wait()
	Close()
	if got := len(s.byEvent(EventTraceRecorded)); got != len(traces) {
		t.Fatalf("got %d events for %d sessions", got, len(traces))
	}
}

func TestRecordTracesSendsNothingWhileAnotherRunHoldsTheLock(t *testing.T) {
	s, home := setup(t, nil)
	Init(options(nil, io.Discard))
	oldWait := tracesLockWait
	tracesLockWait = 50 * time.Millisecond
	t.Cleanup(func() { tracesLockWait = oldWait })

	unlock, ok := lockTraces(home)
	if !ok {
		t.Fatal("could not take the lock")
	}
	RecordTraces(TraceSourceLive, []Trace{{"codex", "a"}})
	if _, err := os.Stat(filepath.Join(home, TracesPath)); !os.IsNotExist(err) {
		t.Fatalf("a run without the lock wrote the reported set: %v", err)
	}
	unlock()

	// The session was left unreported, so the next run sends it.
	RecordTraces(TraceSourceLive, []Trace{{"codex", "a"}})
	Close()
	if got := len(s.byEvent(EventTraceRecorded)); got != 1 {
		t.Fatalf("got %d events, want 1", got)
	}
}

func TestLiveTraceIsKeptBetweenRuns(t *testing.T) {
	setup(t, nil)
	Init(options(nil, nil))
	if _, ok := LiveTrace(); ok {
		t.Fatal("a fresh home has a position")
	}
	saved := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	want := LiveTracePosition{Offset: 4096, LogID: "abc", SavedAt: saved}
	SetLiveTrace(want)
	Close()
	Init(options(nil, nil))
	if got, ok := LiveTrace(); !ok || got != want {
		t.Fatalf("position after a new run = %+v, %t; want %+v", got, ok, want)
	}
}
