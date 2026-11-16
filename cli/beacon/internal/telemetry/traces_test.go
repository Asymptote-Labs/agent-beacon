package telemetry

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordTracesSendsOneEventPerNewSession(t *testing.T) {
	s, home := setup(t, nil)
	Init(options(nil, io.Discard))
	RecordTraces(TraceSourceBackfill, []Trace{{"claude_code", "a"}, {"codex", "a"}, {"claude_code", "a"}, {"claude_code", ""}})
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
		{"codex", TraceSourceBackfill, false},
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
