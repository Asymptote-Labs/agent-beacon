package beaconevent

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// codexLog is a Codex semantic log record with the attributes Codex sends: the event name, the
// conversation it belongs to, and the surface that started it. An empty originator leaves the
// attribute off, as Codex's own debug logs do.
func codexLog(eventName, conversation, originator string) plog.LogRecord {
	record := plog.NewLogRecord()
	record.Attributes().PutStr("event.name", eventName)
	if conversation != "" {
		record.Attributes().PutStr("conversation.id", conversation)
	}
	if originator != "" {
		record.Attributes().PutStr("originator", originator)
	}
	record.Attributes().PutStr("app.version", "0.164.0")
	record.Attributes().PutStr("model", "gpt-5.5")
	if eventName == "codex.user_prompt" {
		record.Attributes().PutStr("prompt", "list the files")
	}
	return record
}

// codexTurnSpan is the per-turn usage span: it names its conversation as thread.id and carries no
// originator.
func codexTurnSpan(thread string) ptrace.Span {
	span := ptrace.NewSpan()
	span.SetName("session_task.turn")
	span.Attributes().PutStr("thread.id", thread)
	span.Attributes().PutStr("turn.id", "turn-1")
	span.Attributes().PutInt("codex.turn.token_usage.input_tokens", 1200)
	span.Attributes().PutInt("codex.turn.token_usage.output_tokens", 80)
	return span
}

// codexUsageMetric is the legacy token metric. Codex exports it with no conversation id.
func codexUsageMetric() pmetric.Metric {
	metric := pmetric.NewMetric()
	metric.SetName("codex.turn.token_usage")
	dp := metric.SetEmptySum().DataPoints().AppendEmpty()
	dp.SetDoubleValue(80)
	dp.Attributes().PutStr("token_type", "output")
	return metric
}

func appServer() map[string]interface{} {
	return map[string]interface{}{"service.name": "codex-app-server"}
}

func TestCodexLogHarnessFollowsOriginator(t *testing.T) {
	for name, tc := range map[string]struct {
		resource   map[string]interface{}
		originator string
		want       string
	}{
		"CLI TUI on the app-server":           {appServer(), "codex-tui", "codex_cli"},
		"desktop app":                         {appServer(), "Codex_Desktop", "codex_desktop"},
		"desktop, any spelling":               {appServer(), "codex desktop", "codex_desktop"},
		"desktop, upper case":                 {appServer(), "CODEX-DESKTOP", "codex_desktop"},
		"codex exec on the app-server":        {appServer(), "codex_exec", "codex_cli"},
		"an originator Beacon has never seen": {appServer(), "some_new_surface", "codex_cli"},
		"app-server record without one":       {appServer(), "", "codex_cli"},
		"codex exec":                          {map[string]interface{}{"service.name": "codex_exec"}, "codex_exec", "codex_cli"},
		"older CLI":                           {map[string]interface{}{"service.name": "codex_cli_rs"}, "codex-tui", "codex_cli"},
		"a desktop service name":              {map[string]interface{}{"service.name": "codex_desktop"}, "", "codex_desktop"},
		"a stated harness wins": {
			map[string]interface{}{"service.name": "codex-app-server", "beacon.harness.name": "codex_desktop"}, "codex-tui", "codex_desktop",
		},
	} {
		t.Run(name, func(t *testing.T) {
			converter := NewConverter(Options{CodexSurfaces: &CodexSurfaces{}})
			event := converter.EventFromLog(tc.resource, codexLog("codex.user_prompt", "conv-1", tc.originator))
			if event.Harness.Name != tc.want {
				t.Fatalf("harness = %q, want %q", event.Harness.Name, tc.want)
			}
		})
	}
}

// The bug this fixes: Codex CLI 0.158+ runs on the app-server, and every one of its sessions was
// recorded as codex_desktop, the usage span included, on machines that never ran the desktop app.
func TestCodexCLIOnTheAppServerIsNotRecordedAsDesktop(t *testing.T) {
	converter := NewConverter(Options{CodexSurfaces: &CodexSurfaces{}})
	conversation := "019a17e0-efe2-7ae3-b558-d333b8e7ce96"
	events := []Event{
		converter.EventFromLog(appServer(), codexLog("codex.conversation_starts", conversation, "codex-tui")),
		converter.EventFromLog(appServer(), codexLog("codex.user_prompt", conversation, "codex-tui")),
		converter.EventFromSpan(appServer(), codexTurnSpan(conversation)),
	}
	for _, event := range events {
		if event.Harness.Name != "codex_cli" {
			t.Fatalf("%s recorded as %q, want codex_cli", event.Event.Action, event.Harness.Name)
		}
	}
}

func TestCodexConversationKeepsItsSurfaceOnRecordsWithoutOriginator(t *testing.T) {
	converter := NewConverter(Options{CodexSurfaces: &CodexSurfaces{}})

	// Nothing has named this conversation yet: the usage span takes the default (and the exporter
	// holds it for the conversation's logs; see TestCodexSurfacesHoldAConversationUntilItIsNamed).
	if got := converter.EventFromSpan(appServer(), codexTurnSpan("early")).Harness.Name; got != "codex_cli" {
		t.Fatalf("span before any log = %q, want codex_cli", got)
	}

	converter.EventFromLog(appServer(), codexLog("codex.conversation_starts", "desktop-conv", "Codex_Desktop"))
	converter.EventFromLog(appServer(), codexLog("codex.conversation_starts", "cli-conv", "codex-tui"))

	span := converter.EventFromSpan(appServer(), codexTurnSpan("desktop-conv"))
	if span.Event.Action != "token.usage" || span.Harness.Name != "codex_desktop" {
		t.Fatalf("desktop usage span = %s/%q, want token.usage/codex_desktop", span.Event.Action, span.Harness.Name)
	}
	if got := converter.EventFromSpan(appServer(), codexTurnSpan("cli-conv")).Harness.Name; got != "codex_cli" {
		t.Fatalf("CLI usage span = %q, want codex_cli", got)
	}
	if got := converter.EventFromLog(appServer(), codexLog("codex.tool_result", "desktop-conv", "")).Harness.Name; got != "codex_desktop" {
		t.Fatalf("desktop log without originator = %q, want codex_desktop", got)
	}
	// The legacy metric names no conversation, so nothing places it; the token report folds the
	// two Codex names together where it matters (cli/beacon/internal/tokens usageHarnessKey).
	for _, event := range converter.EventsFromMetric(appServer(), codexUsageMetric()) {
		if event.Harness.Name != "codex_cli" {
			t.Fatalf("usage metric without a conversation = %q, want codex_cli", event.Harness.Name)
		}
	}
	if got := converter.EventFromSpan(appServer(), codexTurnSpan("unknown-conv")).Harness.Name; got != "codex_cli" {
		t.Fatalf("span for an unnamed conversation = %q, want codex_cli", got)
	}
}

func TestCodexSurfacesForgetTheLeastRecentlyUsedConversation(t *testing.T) {
	surfaces := &CodexSurfaces{}
	converter := NewConverter(Options{CodexSurfaces: surfaces})
	converter.EventFromLog(appServer(), codexLog("codex.user_prompt", "oldest", "Codex_Desktop"))
	converter.EventFromLog(appServer(), codexLog("codex.user_prompt", "active", "Codex_Desktop"))
	// Two more than fit, so first-in-first-out would drop "active" too.
	for i := 0; i < maxCodexConversations; i++ {
		surfaces.remember(fmt.Sprintf("conv-%d", i), "codex_cli")
		if i == maxCodexConversations/2 {
			// A conversation still in use stays remembered however many start after it.
			converter.EventFromSpan(appServer(), codexTurnSpan("active"))
		}
	}
	if got := converter.EventFromSpan(appServer(), codexTurnSpan("oldest")).Harness.Name; got != "codex_cli" {
		t.Fatalf("evicted conversation = %q, want the codex_cli default", got)
	}
	if got := converter.EventFromSpan(appServer(), codexTurnSpan("active")).Harness.Name; got != "codex_desktop" {
		t.Fatalf("recently used conversation = %q, want codex_desktop", got)
	}
	surfaces.mu.Lock()
	defer surfaces.mu.Unlock()
	if surfaces.recent.Len() != maxCodexConversations || len(surfaces.byID) != maxCodexConversations {
		t.Fatalf("remembered %d/%d conversations, want at most %d", surfaces.recent.Len(), len(surfaces.byID), maxCodexConversations)
	}
}

// Without the shared memory each record is placed on its own: an originator still decides, and a
// record without one takes the default.
func TestCodexSurfaceWithoutMemory(t *testing.T) {
	converter := Converter{}
	if got := converter.EventFromLog(appServer(), codexLog("codex.user_prompt", "c", "Codex_Desktop")).Harness.Name; got != "codex_desktop" {
		t.Fatalf("desktop log = %q, want codex_desktop", got)
	}
	if got := converter.EventFromSpan(appServer(), codexTurnSpan("c")).Harness.Name; got != "codex_cli" {
		t.Fatalf("span = %q, want codex_cli", got)
	}
}

// Logs, traces and metrics reach the exporter on separate pipelines.
func TestCodexSurfacesAreSafeForConcurrentSignals(t *testing.T) {
	converter := NewConverter(Options{CodexSurfaces: &CodexSurfaces{}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conversation := fmt.Sprintf("conv-%d", i%3)
			for j := 0; j < 200; j++ {
				converter.EventFromLog(appServer(), codexLog("codex.user_prompt", conversation, "Codex_Desktop"))
				converter.EventFromSpan(appServer(), codexTurnSpan(conversation))
			}
		}(i)
	}
	wg.Wait()
}

// The collector builds the logs, traces and metrics exporters from one config separately; they
// must hold one memory between them, and it goes when the last of them shuts down.
func TestAcquireCodexSurfacesSharesOnePerConfig(t *testing.T) {
	type config struct{ path string }
	a, b := &config{"a"}, &config{"a"}
	logs, releaseLogs := AcquireCodexSurfaces(a)
	traces, releaseTraces := AcquireCodexSurfaces(a)
	other, releaseOther := AcquireCodexSurfaces(b)
	defer releaseOther()
	if logs != traces {
		t.Fatal("two exporters built from one config got separate memories")
	}
	if logs == other {
		t.Fatal("exporters built from different configs share a memory")
	}
	logs.remember("desktop-conv", codexDesktopHarness)
	if got := traces.recall("desktop-conv"); got != codexDesktopHarness {
		t.Fatalf("traces exporter recalls %q, want codex_desktop", got)
	}

	releaseLogs()
	releaseLogs() // a second release is a no-op
	again, release := AcquireCodexSurfaces(a)
	if again != traces {
		t.Fatal("memory freed while an exporter still held it")
	}
	release()
	releaseTraces()
	fresh, release := AcquireCodexSurfaces(a)
	defer release()
	if fresh == traces {
		t.Fatal("memory kept after every exporter released it")
	}
}

// The logs and traces pipelines flush on their own timers, so a short Desktop turn's usage span can
// arrive before the prompt log that names its conversation.
func TestCodexSurfacesHoldAConversationUntilItIsNamed(t *testing.T) {
	surfaces := &CodexSurfaces{}
	converter := NewConverter(Options{CodexSurfaces: surfaces})
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	early := converter.EventFromSpan(appServer(), codexTurnSpan("desktop-conv"))
	if ready := surfaces.Sort([]Event{early}, now); len(ready) != 0 {
		t.Fatalf("span for an unnamed conversation written at once: %+v", ready)
	}

	prompt := converter.EventFromLog(appServer(), codexLog("codex.user_prompt", "desktop-conv", "Codex_Desktop"))
	ready := surfaces.Sort([]Event{prompt}, now.Add(2*time.Second))
	if len(ready) != 2 || ready[0].Event.Action != "prompt.submitted" || ready[1].Event.Action != "token.usage" {
		t.Fatalf("ready = %+v, want the prompt then the held span", ready)
	}
	for _, event := range ready {
		if event.Harness.Name != "codex_desktop" || event.codexConversation != "" {
			t.Fatalf("%s = %q (held for %q), want codex_desktop", event.Event.Action, event.Harness.Name, event.codexConversation)
		}
	}

	// Once named, a conversation's later spans go straight out.
	later := converter.EventFromSpan(appServer(), codexTurnSpan("desktop-conv"))
	if ready := surfaces.Sort([]Event{later}, now.Add(3*time.Second)); len(ready) != 1 || ready[0].Harness.Name != "codex_desktop" {
		t.Fatalf("span for a named conversation = %+v, want it written as codex_desktop", ready)
	}
}

func TestCodexSurfacesReleaseAnUnnamedConversationAfterTheWait(t *testing.T) {
	surfaces := &CodexSurfaces{}
	converter := NewConverter(Options{CodexSurfaces: surfaces})
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	surfaces.Sort([]Event{converter.EventFromSpan(appServer(), codexTurnSpan("logs-never-came"))}, now)
	if ready := surfaces.Sort(nil, now.Add(codexHoldWindow-time.Millisecond)); len(ready) != 0 {
		t.Fatalf("released before the wait ran out: %+v", ready)
	}
	ready := surfaces.Sort(nil, now.Add(codexHoldWindow))
	if len(ready) != 1 || ready[0].Harness.Name != "codex_cli" {
		t.Fatalf("after the wait = %+v, want the span as codex_cli", ready)
	}

	// A record with no conversation can never be named, so it is not held.
	debug := converter.EventFromLog(appServer(), codexLog("codex.startup", "", ""))
	if ready := surfaces.Sort([]Event{debug}, now); len(ready) != 1 {
		t.Fatalf("record without a conversation held: %+v", ready)
	}
}

func TestCodexSurfacesDrainOnShutdown(t *testing.T) {
	surfaces := &CodexSurfaces{}
	converter := NewConverter(Options{CodexSurfaces: surfaces})
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	surfaces.Sort([]Event{
		converter.EventFromSpan(appServer(), codexTurnSpan("unnamed")),
		converter.EventFromSpan(appServer(), codexTurnSpan("named-late")),
	}, now)
	surfaces.remember("named-late", codexDesktopHarness)

	drained := surfaces.Drain()
	if len(drained) != 2 || drained[0].Harness.Name != "codex_cli" || drained[1].Harness.Name != "codex_desktop" {
		t.Fatalf("drained = %+v, want unnamed as codex_cli and named-late as codex_desktop", drained)
	}
	if again := surfaces.Drain(); len(again) != 0 {
		t.Fatalf("second drain = %+v, want nothing", again)
	}
}

func TestCodexSurfacesBoundWhatTheyHold(t *testing.T) {
	surfaces := &CodexSurfaces{}
	converter := NewConverter(Options{CodexSurfaces: surfaces})
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var released []Event
	for i := 0; i <= maxHeldCodexEvents; i++ {
		released = append(released, surfaces.Sort([]Event{converter.EventFromSpan(appServer(), codexTurnSpan(fmt.Sprintf("conv-%d", i)))}, now)...)
	}
	if len(released) != 1 || released[0].Session.ID != "conv-0" {
		t.Fatalf("released = %+v, want only the oldest", released)
	}
	surfaces.mu.Lock()
	defer surfaces.mu.Unlock()
	if len(surfaces.held) != maxHeldCodexEvents {
		t.Fatalf("holding %d, want %d", len(surfaces.held), maxHeldCodexEvents)
	}
}

func TestCodexSortWithoutMemoryPassesEverythingThrough(t *testing.T) {
	var surfaces *CodexSurfaces
	events := []Event{Converter{}.EventFromSpan(appServer(), codexTurnSpan("c"))}
	if ready := surfaces.Sort(events, time.Now()); len(ready) != 1 {
		t.Fatalf("nil memory held a record: %+v", ready)
	}
	if drained := surfaces.Drain(); drained != nil {
		t.Fatalf("nil memory drained %+v", drained)
	}
}
