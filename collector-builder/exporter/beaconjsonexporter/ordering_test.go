package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// orderingExporter builds an exporter writing to a fresh runtime log, and returns it with
// the log path.
func orderingExporter(t *testing.T) (*beaconExporter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:                  path,
		MaxEventBytes:         defaultMaxEventBytes,
		RotateBytes:           defaultRotateBytes,
		RedactSecrets:         true,
		IncludeRuntimeMetrics: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	return exp, path
}

// writtenEvents decodes the events the exporter wrote, in file order.
func writtenEvents(t *testing.T, path string) []asymptoteobserve.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var events []asymptoteobserve.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event asymptoteobserve.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatalf("no events written to %s", path)
	}
	return events
}

// promptLogs builds a batch of prompt log records at the given times.
func promptLogs(times ...time.Time) plog.Logs {
	logs := plog.NewLogs()
	records := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	for i, at := range times {
		record := records.AppendEmpty()
		record.SetTimestamp(pcommon.NewTimestampFromTime(at))
		record.Attributes().PutStr("beacon.event.action", "prompt.submitted")
		record.Attributes().PutStr("gen_ai.prompt", string(rune('a'+i)))
	}
	return logs
}

func TestConsumeLogsKeepsWireNanosecondPrecision(t *testing.T) {
	// The exporter already received nanosecond timestamps off the wire and threw the
	// sub-second part away formatting them, which is why no event in a real runtime log
	// carried one.
	exp, path := orderingExporter(t)
	at := time.Date(2026, 8, 21, 9, 14, 3, 123456789, time.UTC)
	if err := exp.consumeLogs(context.Background(), promptLogs(at)); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}

	event := writtenEvents(t, path)[0]
	if want := asymptoteobserve.FormatTimestamp(at); event.Timestamp != want {
		t.Fatalf("timestamp = %q, want %q", event.Timestamp, want)
	}
}

func TestConsumeLogsStampsSequenceInWireOrder(t *testing.T) {
	exp, path := orderingExporter(t)
	base := time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC)
	logs := promptLogs(base, base.Add(time.Millisecond), base.Add(2*time.Millisecond))
	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}

	events := writtenEvents(t, path)
	if len(events) != 3 {
		t.Fatalf("wrote %d events, want 3", len(events))
	}
	for i, event := range events {
		if want := uint64(i + 1); event.Sequence != want {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, want)
		}
	}
}

func TestSequenceContinuesAcrossExports(t *testing.T) {
	// One counter for the exporter's lifetime, so two batches never claim the same
	// number and the second export cannot look like it came first.
	exp, path := orderingExporter(t)
	base := time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC)
	if err := exp.consumeLogs(context.Background(), promptLogs(base)); err != nil {
		t.Fatalf("first consumeLogs returned error: %v", err)
	}
	if err := exp.consumeLogs(context.Background(), promptLogs(base.Add(time.Second))); err != nil {
		t.Fatalf("second consumeLogs returned error: %v", err)
	}

	events := writtenEvents(t, path)
	if len(events) != 2 {
		t.Fatalf("wrote %d events, want 2", len(events))
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", events[0].Sequence, events[1].Sequence)
	}
}

func TestConsumeMetricsSeparatesDatapointsSharingATimestamp(t *testing.T) {
	// The case a more precise timestamp cannot fix: one export flushes a batch of
	// datapoints that all carry that export's collection instant. Token attribution
	// depends on recovering their emission order.
	exp, path := orderingExporter(t)
	at := pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 9, 14, 3, 500000000, time.UTC))

	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("claude_code.token.usage")
	sum := metric.SetEmptySum()
	for _, tokenType := range []string{"input", "output", "cacheRead"} {
		point := sum.DataPoints().AppendEmpty()
		point.SetTimestamp(at)
		point.SetIntValue(10)
		point.Attributes().PutStr("type", tokenType)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}

	events := writtenEvents(t, path)
	if len(events) != 3 {
		t.Fatalf("wrote %d events, want one per datapoint", len(events))
	}
	stamp := events[0].Timestamp
	for i, event := range events {
		if event.Timestamp != stamp {
			t.Fatalf("event %d timestamp = %q, want the shared %q (the premise of this test)",
				i, event.Timestamp, stamp)
		}
		if want := uint64(i + 1); event.Sequence != want {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, want)
		}
	}
}

func TestConsumeLogsPromotesRuntimeSuppliedSequence(t *testing.T) {
	// A runtime that numbers its own telemetry keeps its numbering; the exporter only
	// fills in what nobody counted.
	exp, path := orderingExporter(t)
	logs := plog.NewLogs()
	records := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()

	supplied := records.AppendEmpty()
	supplied.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC)))
	supplied.Attributes().PutStr("beacon.event.action", "prompt.submitted")
	supplied.Attributes().PutInt("event.sequence", 4096)

	unsupplied := records.AppendEmpty()
	unsupplied.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 9, 14, 4, 0, time.UTC)))
	unsupplied.Attributes().PutStr("beacon.event.action", "prompt.submitted")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}

	events := writtenEvents(t, path)
	if len(events) != 2 {
		t.Fatalf("wrote %d events, want 2", len(events))
	}
	if events[0].Sequence != 4096 {
		t.Fatalf("supplied sequence = %d, want 4096 preserved", events[0].Sequence)
	}
	if events[1].Sequence != 1 {
		t.Fatalf("stamped sequence = %d, want 1 (the exporter's own counter is untouched by the promoted one)", events[1].Sequence)
	}
}

func TestPromotedSequenceRejectsNonPositiveValues(t *testing.T) {
	// Zero is the schema's "unsequenced" and a negative counter is not a counter, so
	// neither may displace the exporter's own number.
	exp, _ := orderingExporter(t)
	for _, supplied := range []int64{0, -7} {
		record := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		record.Attributes().PutStr("beacon.event.action", "prompt.submitted")
		record.Attributes().PutInt("event.sequence", supplied)
		event := exp.eventFromLog(nil, record)
		if event.Sequence != 0 {
			t.Fatalf("event.sequence=%d promoted to %d, want left unsequenced", supplied, event.Sequence)
		}
		exp.stampSequence(&event)
		if event.Sequence == 0 {
			t.Fatalf("event.sequence=%d left unstamped by the exporter", supplied)
		}
	}
}
