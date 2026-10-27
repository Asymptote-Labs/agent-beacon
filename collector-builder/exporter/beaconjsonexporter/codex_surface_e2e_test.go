package beaconjsonexporter

import (
	"context"
	"path/filepath"
	"testing"

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

	want := map[string]string{"cli-conv": "codex_cli", "desktop-conv": "codex_desktop"}
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
	if usage != 2 {
		t.Fatalf("wrote %d token.usage events, want 2", usage)
	}
}
