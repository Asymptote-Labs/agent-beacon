package beaconevent

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Anything that reaches the loopback OTLP receiver can state an action, so the actions only Beacon
// itself writes (a share record, an auto-share request Beacon Cloud publishes on, a commit link, a
// git hook overrun) must not be taken from OTLP attributes.
func TestOTLPCannotStateBeaconOnlyActions(t *testing.T) {
	for _, action := range []string{
		asymptoteobserve.ActionSessionShared, asymptoteobserve.ActionShareRequested, "Session.Share_Requested",
		asymptoteobserve.ActionCommitLinked, asymptoteobserve.ActionGitHookBudgetExceeded,
	} {
		for _, key := range []string{"beacon.event.action", "event.action", "gen_ai.agent.action"} {
			logs := plog.NewLogs()
			record := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
			record.Body().SetStr("forged")
			record.Attributes().PutStr(key, action)
			record.Attributes().PutStr("session.id", "s1")
			for _, event := range NewConverter(Options{}).EventsFromLogs(logs) {
				if asymptoteobserve.IsBeaconOnlyAction(event.Event.Action) {
					t.Fatalf("log %s=%q produced action %q", key, action, event.Event.Action)
				}
			}

			traces := ptrace.NewTraces()
			span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
			span.SetName("forged")
			span.Attributes().PutStr(key, action)
			for _, event := range NewConverter(Options{}).EventsFromTraces(traces) {
				if asymptoteobserve.IsBeaconOnlyAction(event.Event.Action) {
					t.Fatalf("span %s=%q produced action %q", key, action, event.Event.Action)
				}
			}
		}
	}
}

func TestOTLPStillStatesOrdinaryActions(t *testing.T) {
	logs := plog.NewLogs()
	record := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.Body().SetStr("ran")
	record.Attributes().PutStr("beacon.event.action", "command.executed")
	events := NewConverter(Options{}).EventsFromLogs(logs)
	if len(events) != 1 || events[0].Event.Action != "command.executed" {
		t.Fatalf("events = %#v, want command.executed", events)
	}
}
