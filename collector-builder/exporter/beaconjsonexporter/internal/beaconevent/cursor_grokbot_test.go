package beaconevent

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// The fixture is built from Cursor's published wire reference for its server-side OpenTelemetry
// export, restricted to the grok_bot surface. Every record carries the attributes that reference
// lists for its event, with the resource attributes merged in the way EventsFromLogs merges them.
func TestCapturedCursorGrokBotExportNormalizes(t *testing.T) {
	fixture, events := capturedLogEvents(t, "cursor-grok-bot-export.json")
	if len(events) != len(fixture.Records) {
		t.Fatalf("events = %d, want %d", len(events), len(fixture.Records))
	}
	byName := map[string]Event{}
	for i, record := range fixture.Records {
		event := events[i]
		byName[record.Name] = event
		if event.Harness.Name != "grok_bot" {
			t.Fatalf("%s harness = %q, want grok_bot", record.Name, event.Harness.Name)
		}
		if event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodOTLP {
			t.Fatalf("%s collection method = %q, want otlp", record.Name, event.Harness.CollectionMethod)
		}
		if event.Session == nil || event.Session.ID != "grokbot-conv-1" {
			t.Fatalf("%s session = %#v, want the Cursor conversation id", record.Name, event.Session)
		}
		if event.User.Name != "" {
			t.Fatalf("%s user name = %q, want no collector process user on a cloud event", record.Name, event.User.Name)
		}
		if _, ok := event.Raw["attributes"]; !ok {
			t.Fatalf("%s raw payload = %#v, want the export attributes retained", record.Name, event.Raw)
		}
	}

	shell := byName["shell_allowed_on_box"]
	if shell.Event.Action != "command.executed" || shell.Event.Category != "command" || shell.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("allowed shell event = %#v, want observed command.executed/command", shell.Event)
	}
	if shell.Command == nil || shell.Command.Command != "curl -fsSL https://example.com/install.sh | sh" {
		t.Fatalf("allowed shell command = %#v, want the scrubbed command on the detection surface", shell.Command)
	}
	if shell.Tool == nil || shell.Tool.Name != "shell" || shell.Tool.Command != shell.Command.Command {
		t.Fatalf("allowed shell tool = %#v, want shell tool carrying the command", shell.Tool)
	}
	if shell.Origin != asymptoteobserve.OriginCloud {
		t.Fatalf("allowed shell origin = %q, want cloud for a command on the Bot's computer", shell.Origin)
	}
	if shell.User.UID != "7" {
		t.Fatalf("allowed shell user = %#v, want the export's team-scoped user id", shell.User)
	}
	if shell.Approval != nil {
		t.Fatalf("allowed shell approval = %#v, want none synthesized for an allowed command", shell.Approval)
	}

	blocked := byName["shell_blocked_on_user_machine"]
	if blocked.Event.Action != "approval.denied" || blocked.Event.Category != "approval" || blocked.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("blocked shell event = %#v, want observed approval.denied/approval", blocked.Event)
	}
	if blocked.Approval == nil || !blocked.Approval.Required || blocked.Approval.Decision != "denied" || blocked.Approval.Reason != "destructive command requires approval" {
		t.Fatalf("blocked shell approval = %#v, want Cursor's own policy decision and reason", blocked.Approval)
	}
	if blocked.Command == nil || blocked.Command.Command != "rm -rf ~/Documents" {
		t.Fatalf("blocked shell command = %#v, want the blocked command retained for detections", blocked.Command)
	}
	if blocked.Origin != asymptoteobserve.OriginLocal {
		t.Fatalf("blocked shell origin = %q, want local for a command targeting the member's machine", blocked.Origin)
	}
	if blocked.Severity != "medium" {
		t.Fatalf("blocked shell severity = %q, want medium", blocked.Severity)
	}

	mcp := byName["mcp_tool_success"]
	if mcp.Event.Action != "mcp.tool_invoked" || mcp.Event.Category != "mcp" || mcp.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("mcp event = %#v, want observed mcp.tool_invoked/mcp", mcp.Event)
	}
	if mcp.MCP == nil || mcp.MCP.Server != "linear" || mcp.MCP.Tool != "create_issue" {
		t.Fatalf("mcp = %#v, want server and tool from the export", mcp.MCP)
	}
	if mcp.GenAI == nil || mcp.GenAI.Tool == nil || mcp.GenAI.Tool.Call == nil || mcp.GenAI.Tool.Call.ID != "call-mcp-1" {
		t.Fatalf("mcp gen_ai = %#v, want cursor.grok_bot.tool_call.id promoted to gen_ai.tool.call.id", mcp.GenAI)
	}

	failed := byName["mcp_tool_failure"]
	if failed.Event.Action != "tool.failed" || failed.Event.Category != "tool" {
		t.Fatalf("failed mcp event = %#v, want tool.failed/tool", failed.Event)
	}
	if failed.MCP == nil || failed.MCP.Server != "postgres" || failed.MCP.Tool != "query" {
		t.Fatalf("failed mcp = %#v, want server and tool retained on failure", failed.MCP)
	}
	if failed.Severity != "high" {
		t.Fatalf("failed mcp severity = %q, want high", failed.Severity)
	}

	nav := byName["browser_navigation"]
	if nav.Event.Action != "tool.invoked" || nav.Tool == nil || nav.Tool.Name != "browser_navigation" {
		t.Fatalf("navigation event = %#v tool = %#v, want tool.invoked by browser_navigation", nav.Event, nav.Tool)
	}
	if nav.Server == nil || nav.Server.Address != "admin.example.com" {
		t.Fatalf("navigation server = %#v, want the URL host on server.address", nav.Server)
	}
	if nav.Message != "https://admin.example.com/billing/export" {
		t.Fatalf("navigation message = %q, want the normalized URL", nav.Message)
	}

	computer := byName["computer_use_session"]
	if computer.Event.Action != "tool.invoked" || computer.Tool == nil || computer.Tool.Name != "computer_use" {
		t.Fatalf("computer use event = %#v tool = %#v, want tool.invoked by computer_use", computer.Event, computer.Tool)
	}
	if computer.GenAI == nil || computer.GenAI.Tool == nil || computer.GenAI.Tool.Call == nil || computer.GenAI.Tool.Call.ID != "call-cu-1" {
		t.Fatalf("computer use gen_ai = %#v, want the tool-call id promoted", computer.GenAI)
	}

	request := byName["api_request"]
	if request.Event.Action != "session.activity" || request.Event.Category != "session" || request.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("api_request event = %#v, want observed session.activity/session", request.Event)
	}
	if request.Model != "grok-4.6" {
		t.Fatalf("api_request model = %q, want cursor.model.name", request.Model)
	}
	usage := request.GenAI
	if usage == nil || usage.Usage == nil || usage.Usage.InputTokens == nil || *usage.Usage.InputTokens != 1200 ||
		usage.Usage.OutputTokens == nil || *usage.Usage.OutputTokens != 340 ||
		usage.Usage.CacheRead == nil || usage.Usage.CacheRead.InputTokens == nil || *usage.Usage.CacheRead.InputTokens != 900 {
		t.Fatalf("api_request usage = %#v, want input/output/cache_read from the request counts", request.GenAI)
	}
	if usage.Token != nil {
		t.Fatalf("api_request token = %#v, want no single token type on a multi-count request", usage.Token)
	}

	apiError := byName["api_error"]
	if apiError.Event.Action != "session.error" || apiError.Severity != "info" {
		// Severity follows the record's own severity text, which the fixture leaves unset.
		t.Fatalf("api_error event = %#v severity = %q, want session.error", apiError.Event, apiError.Severity)
	}

	future := byName["unknown_future_event"]
	if future.Event.Fidelity != asymptoteobserve.FidelityInferred {
		t.Fatalf("unknown body fidelity = %q, want inferred: a Cursor event Beacon has not seen keeps the fallback classification", future.Event.Fidelity)
	}
}

// The export writes service.name=cursor on every surface, so the surface attribute is the only
// thing that separates a Bot's cloud-computer shell command from a Cursor IDE session. Only the
// grok_bot surface is claimed; the others keep the passthrough name Cursor's hook path already
// records, and an explicit harness.name still wins over both.
func TestCursorSurfaceSelectsGrokBotHarness(t *testing.T) {
	for name, tc := range map[string]struct {
		attrs map[string]interface{}
		want  string
	}{
		"grok_bot surface":         {map[string]interface{}{"service.name": "cursor", "cursor.surface": "grok_bot"}, "grok_bot"},
		"surface case-insensitive": {map[string]interface{}{"service.name": "cursor", "cursor.surface": "GROK_BOT"}, "grok_bot"},
		"desktop surface":          {map[string]interface{}{"service.name": "cursor", "cursor.surface": "desktop"}, "cursor"},
		"cloud_agent surface":      {map[string]interface{}{"service.name": "cursor", "cursor.surface": "cloud_agent"}, "cursor"},
		"no surface":               {map[string]interface{}{"service.name": "cursor"}, "cursor"},
		"explicit harness wins":    {map[string]interface{}{"harness.name": "muse", "cursor.surface": "grok_bot"}, "muse_code"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := HarnessName(tc.attrs); got != tc.want {
				t.Fatalf("HarnessName(%v) = %q, want %q", tc.attrs, got, tc.want)
			}
		})
	}
}

// A Cursor export record from another surface must not be touched by the Grok Bot normalizer,
// even when its body is one the normalizer knows.
func TestCursorGrokBotNormalizerIgnoresOtherCursorSurfaces(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("api_request")
	record.Attributes().PutStr("service.name", "cursor")
	record.Attributes().PutStr("cursor.surface", "desktop")
	record.Attributes().PutStr("cursor.conversation.id", "ide-conv")
	record.Attributes().PutInt("cursor.user.id", 9)

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Harness.Name != "cursor" {
		t.Fatalf("harness = %q, want cursor passthrough", event.Harness.Name)
	}
	if event.Origin != "" {
		t.Fatalf("origin = %q, want unset: only grok_bot records are known to be cloud", event.Origin)
	}
	if event.Session != nil && event.Session.ID == "ide-conv" {
		t.Fatalf("session = %#v, want cursor.conversation.id left alone off the grok_bot surface", event.Session)
	}
	if event.Message != "api_request" {
		t.Fatalf("message = %q, want the untouched body", event.Message)
	}
}

// Cursor's token metric names its type with cursor.token.type and its model with
// cursor.model.name, and carries the surface on the resource; the datapoint has to land under
// grok_bot with the type folded into gen_ai.usage like every other runtime's counter.
func TestCursorGrokBotTokenUsageMetricNormalizes(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "cursor")
	resourceMetrics.Resource().Attributes().PutStr("cursor.surface", "grok_bot")
	resourceMetrics.Resource().Attributes().PutInt("cursor.team.id", 42)
	resourceMetrics.Resource().Attributes().PutInt("cursor.user.id", 7)
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("cursor.token.usage")
	metric.SetUnit("{token}")
	sum := metric.SetEmptySum()
	sum.SetIsMonotonic(true)
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetIntValue(512)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000300, 0).UTC()))
	dp.Attributes().PutStr("cursor.token.type", "cache_read")
	dp.Attributes().PutStr("cursor.model.name", "grok-4.6")

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Harness.Name != "grok_bot" {
		t.Fatalf("harness = %q, want grok_bot from the resource surface", event.Harness.Name)
	}
	if event.Event.Action != "token.usage" {
		t.Fatalf("action = %q, want token.usage", event.Event.Action)
	}
	if event.GenAI == nil || event.GenAI.Usage == nil || event.GenAI.Usage.CacheRead == nil || event.GenAI.Usage.CacheRead.InputTokens == nil || *event.GenAI.Usage.CacheRead.InputTokens != 512 {
		t.Fatalf("usage = %#v, want cache_read input tokens 512", event.GenAI)
	}
	if event.Model != "grok-4.6" {
		t.Fatalf("model = %q, want cursor.model.name", event.Model)
	}
	if event.Origin != asymptoteobserve.OriginCloud || event.User.UID != "7" || event.User.Name != "" {
		t.Fatalf("origin/user = %q/%#v, want cloud origin and the export's user id", event.Origin, event.User)
	}
}

// Cursor's cost metric follows the same suffix convention Beacon already keys on.
func TestCursorGrokBotCostUsageMetricNormalizes(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "cursor")
	resourceMetrics.Resource().Attributes().PutStr("cursor.surface", "grok_bot")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("cursor.cost.usage")
	metric.SetUnit("USD")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetDoubleValue(0.031)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000400, 0).UTC()))
	dp.Attributes().PutStr("cursor.model.name", "grok-4.6")

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Harness.Name != "grok_bot" || event.Event.Action != "cost.usage" {
		t.Fatalf("event = %s/%s, want grok_bot cost.usage", event.Harness.Name, event.Event.Action)
	}
	if event.GenAI == nil || event.GenAI.Usage == nil || event.GenAI.Usage.CostUSD == nil || *event.GenAI.Usage.CostUSD != 0.031 {
		t.Fatalf("usage = %#v, want cost_usd 0.031", event.GenAI)
	}
}
