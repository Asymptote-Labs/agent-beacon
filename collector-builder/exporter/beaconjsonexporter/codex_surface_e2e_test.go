package beaconjsonexporter

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

type nopHost struct{}

func (nopHost) GetExtensions() map[component.ID]component.Component { return nil }

// The CLI and the desktop app both export as codex-app-server. The collector builds a separate
// exporter per pipeline from one config, as here: each conversation's logs name its surface on the
// logs exporter, and its usage span, which reaches the traces exporter later without an
// originator, lands under the same harness.
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
	for _, exp := range []component.Component{logsExporter, tracesExporter} {
		if err := exp.Start(ctx, nopHost{}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = exp.Shutdown(ctx) })
	}

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "codex-app-server")
	sl := rl.ScopeLogs().AppendEmpty()
	for _, conv := range []struct{ id, originator string }{{"cli-conv", "codex-tui"}, {"desktop-conv", "Codex_Desktop"}} {
		for _, eventName := range []string{"codex.conversation_starts", "codex.user_prompt"} {
			rec := sl.LogRecords().AppendEmpty()
			rec.Attributes().PutStr("event.name", eventName)
			rec.Attributes().PutStr("conversation.id", conv.id)
			rec.Attributes().PutStr("originator", conv.originator)
			rec.Attributes().PutStr("model", "gpt-5.5")
			if eventName == "codex.user_prompt" {
				rec.Attributes().PutStr("prompt", "list the files")
			}
		}
	}
	if err := logsExporter.ConsumeLogs(ctx, logs); err != nil {
		t.Fatalf("ConsumeLogs: %v", err)
	}

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex-app-server")
	ss := rs.ScopeSpans().AppendEmpty()
	for _, thread := range []string{"cli-conv", "desktop-conv"} {
		span := ss.Spans().AppendEmpty()
		span.SetName("session_task.turn")
		span.Attributes().PutStr("thread.id", thread)
		span.Attributes().PutInt("codex.turn.token_usage.input_tokens", 1200)
		span.Attributes().PutInt("codex.turn.token_usage.output_tokens", 80)
	}
	if err := tracesExporter.ConsumeTraces(ctx, traces); err != nil {
		t.Fatalf("ConsumeTraces: %v", err)
	}

	// A short turn's span can beat its prompt log across the two pipelines: the traces exporter
	// holds it, and the logs exporter writes it once the log names the conversation.
	early := ptrace.NewTraces()
	ers := early.ResourceSpans().AppendEmpty()
	ers.Resource().Attributes().PutStr("service.name", "codex-app-server")
	earlySpan := ers.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	earlySpan.SetName("session_task.turn")
	earlySpan.Attributes().PutStr("thread.id", "quick-desktop-conv")
	earlySpan.Attributes().PutInt("codex.turn.token_usage.input_tokens", 300)
	if err := tracesExporter.ConsumeTraces(ctx, early); err != nil {
		t.Fatalf("ConsumeTraces: %v", err)
	}
	for _, event := range readEvents(t, cfg.Path) {
		if event.Session != nil && event.Session.ID == "quick-desktop-conv" {
			t.Fatalf("span written before its conversation was named: %+v", event)
		}
	}
	quick := plog.NewLogs()
	qrl := quick.ResourceLogs().AppendEmpty()
	qrl.Resource().Attributes().PutStr("service.name", "codex-app-server")
	qrec := qrl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	qrec.Attributes().PutStr("event.name", "codex.user_prompt")
	qrec.Attributes().PutStr("conversation.id", "quick-desktop-conv")
	qrec.Attributes().PutStr("originator", "Codex_Desktop")
	qrec.Attributes().PutStr("prompt", "hi")
	if err := logsExporter.ConsumeLogs(ctx, quick); err != nil {
		t.Fatalf("ConsumeLogs: %v", err)
	}

	want := map[string]string{"cli-conv": "codex_cli", "desktop-conv": "codex_desktop", "quick-desktop-conv": "codex_desktop"}
	usage := 0
	for _, event := range readEvents(t, cfg.Path) {
		if event.Session == nil {
			t.Fatalf("event without a session: %#v", event)
		}
		if got := event.Harness.Name; got != want[event.Session.ID] {
			t.Errorf("%s in %s recorded as %q, want %q", event.Event.Action, event.Session.ID, got, want[event.Session.ID])
		}
		if event.Event.Action == "token.usage" {
			usage++
		}
	}
	if usage != 3 {
		t.Fatalf("wrote %d token.usage events, want 3", usage)
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
