package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

type nopHost struct{}

func (nopHost) GetExtensions() map[component.ID]component.Component { return nil }

// codexAppServerExport is one Codex app-server process's OTLP export of one turn, as OTLP/JSON:
// the conversation's start and prompt logs, the turn span and the legacy token metric. The
// fixtures follow openai/codex's own emitters field for field (otel/src/events/shared.rs,
// core/src/tasks/mod.rs, core/src/state/turn_token_usage.rs) and the tracing bridges' defaults,
// which put the worker's OS thread id on the span under the same thread.id key Codex uses for the
// conversation.
type codexAppServerExport struct {
	Originator   string          `json:"originator"`
	Conversation string          `json:"conversation"`
	Logs         json.RawMessage `json:"logs"`
	Traces       json.RawMessage `json:"traces"`
	Metrics      json.RawMessage `json:"metrics"`
}

func loadCodexAppServerExport(t *testing.T, name string) (codexAppServerExport, plog.Logs, ptrace.Traces, pmetric.Metrics) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("internal", "beaconevent", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var export codexAppServerExport
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(export.Logs)
	if err != nil {
		t.Fatal(err)
	}
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(export.Traces)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(export.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	return export, logs, traces, metrics
}

// The CLI and the desktop app both export as codex-app-server. The collector builds a separate
// exporter per pipeline from one config, as here: each conversation's logs name its surface on the
// logs exporter, its usage span, which reaches the traces exporter without an originator, lands
// under the same harness, and the legacy token metric is named by the originator it carries.
func TestCollectorNamesCodexAppServerSessionsByOriginator(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}
	nop := processortest.NewNopSettings(componentType)
	set := exporter.Settings{ID: nop.ID, TelemetrySettings: nop.TelemetrySettings, BuildInfo: nop.BuildInfo}
	factory := NewFactory()
	logsExporter, err := factory.CreateLogs(ctx, set, cfg)
	if err != nil {
		t.Fatalf("CreateLogs: %v", err)
	}
	tracesExporter, err := factory.CreateTraces(ctx, set, cfg)
	if err != nil {
		t.Fatalf("CreateTraces: %v", err)
	}
	metricsExporter, err := factory.CreateMetrics(ctx, set, cfg)
	if err != nil {
		t.Fatalf("CreateMetrics: %v", err)
	}
	for _, exp := range []component.Component{logsExporter, tracesExporter, metricsExporter} {
		if err := exp.Start(ctx, nopHost{}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = exp.Shutdown(ctx) })
	}

	written := 0
	for _, tc := range []struct {
		fixture, want string
		spanFirst     bool
	}{
		{"codex-app-server-cli-0.164.0.json", "codex_cli", false},
		// A short turn's span can beat its prompt log across the two pipelines: the traces
		// exporter holds it, and the logs exporter writes it once the log names the conversation.
		{"codex-app-server-desktop-0.164.0.json", "codex_desktop", true},
	} {
		export, logs, traces, metrics := loadCodexAppServerExport(t, tc.fixture)
		if tc.spanFirst {
			if err := tracesExporter.ConsumeTraces(ctx, traces); err != nil {
				t.Fatalf("ConsumeTraces: %v", err)
			}
			if events := readEvents(t, cfg.Path); len(events) != written {
				t.Fatalf("%s: span written before its conversation was named: %+v", export.Originator, events[written:])
			}
		}
		if err := logsExporter.ConsumeLogs(ctx, logs); err != nil {
			t.Fatalf("ConsumeLogs: %v", err)
		}
		if !tc.spanFirst {
			if err := tracesExporter.ConsumeTraces(ctx, traces); err != nil {
				t.Fatalf("ConsumeTraces: %v", err)
			}
		}
		if err := metricsExporter.ConsumeMetrics(ctx, metrics); err != nil {
			t.Fatalf("ConsumeMetrics: %v", err)
		}

		events := readEvents(t, cfg.Path)[written:]
		written += len(events)
		var span, metric int
		for _, event := range events {
			if got := event.Harness.Name; got != tc.want {
				t.Errorf("%s: %s recorded as %q, want %q", export.Originator, event.Event.Action, got, tc.want)
			}
			if event.Event.Action != "token.usage" {
				continue
			}
			if event.Session != nil && event.Session.ID == export.Conversation {
				span++
			} else {
				metric++
			}
		}
		if span != 1 || metric == 0 {
			t.Fatalf("%s: wrote %d usage span(s) in the conversation and %d metric usage event(s), want 1 and some", export.Originator, span, metric)
		}
	}
}

// A span whose conversation is never named goes out when its wait runs out, without waiting for
// another batch to carry it.
func TestExporterWritesAnUnnamedCodexSpanWhenItsWaitRunsOut(t *testing.T) {
	var ahead atomic.Int64
	clock, every := codexClock, codexReleaseEvery
	codexClock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	codexReleaseEvery = 10 * time.Millisecond
	t.Cleanup(func() { codexClock, codexReleaseEvery = clock, every })

	ctx := context.Background()
	exp, path := newTestExporter(t)
	if err := exp.start(ctx, nopHost{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = exp.shutdown(ctx) }()

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex-app-server")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("session_task.turn")
	span.Attributes().PutStr("thread.id", "logs-never-came")
	span.Attributes().PutInt("codex.turn.token_usage.input_tokens", 300)
	if err := exp.consumeTraces(ctx, traces); err != nil {
		t.Fatalf("consumeTraces: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if events := readEvents(t, path); len(events) != 0 {
		t.Fatalf("written before the wait ran out: %+v", events)
	}

	ahead.Store(int64(time.Minute))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if events := readEvents(t, path); len(events) == 1 {
			if events[0].Harness.Name != "codex_cli" || events[0].Event.Action != "token.usage" {
				t.Fatalf("released %s as %q, want token.usage as codex_cli", events[0].Event.Action, events[0].Harness.Name)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the held span was never written")
}
