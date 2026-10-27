package beaconjsonexporter

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// The CLI and the desktop app both export as codex-app-server. Through the exporter, each
// conversation's logs name its surface and its usage span, which arrives later on the traces
// pipeline without an originator, lands under the same harness.
func TestExporterNamesCodexAppServerSessionsByOriginator(t *testing.T) {
	exp, path := newTestExporter(t)

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
	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
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
	if err := exp.consumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("consumeTraces returned error: %v", err)
	}

	want := map[string]string{"cli-conv": "codex_cli", "desktop-conv": "codex_desktop"}
	usage := 0
	for _, event := range readEvents(t, path) {
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
