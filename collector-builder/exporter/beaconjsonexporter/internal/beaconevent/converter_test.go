package beaconevent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Guards against the exporter Event mirror struct drifting from the shared
// asymptoteobserve schema: new optional fields must survive JSON marshaling.
func TestEventMirrorSerializesTraceAndUsageCost(t *testing.T) {
	event := NewEvent("token.usage", "metric", "info", "claude_code", time.Unix(1700000000, 0).UTC())
	event.Trace = &TraceInfo{ID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", ParentSpanID: "fedcba9876543210"}
	cost := 0.0123
	input := int64(120)
	event.GenAI = &GenAIInfo{Usage: &GenAIUsageInfo{InputTokens: &input, CostUSD: &cost}}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"trace":{"id":"0123456789abcdef0123456789abcdef","span_id":"0123456789abcdef","parent_span_id":"fedcba9876543210"}`,
		`"cost_usd":0.0123`,
		`"input_tokens":120`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON missing %s: %s", want, text)
		}
	}
}

func TestEventsFromTracesNormalizesObserveSDKSpan(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("agent.plan")
	attrs := span.Attributes()
	attrs.PutStr("beacon.event.action", "prompt.submitted")
	attrs.PutStr("beacon.event.category", "prompt")
	attrs.PutStr("beacon.prompt.text", "summarize this deployment")
	attrs.PutStr("gen_ai.provider.name", "openai")
	attrs.PutStr("gen_ai.operation.name", "chat")
	attrs.PutStr("gen_ai.request.model", "gpt-4o-mini")
	attrs.PutInt("gen_ai.usage.input_tokens", 12)
	attrs.PutInt("gen_ai.usage.output_tokens", 34)

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Origin != "cloud" {
		t.Fatalf("origin = %q, want cloud", event.Origin)
	}
	if event.Harness.Name != "asymptote_observe" {
		t.Fatalf("harness = %q, want asymptote_observe", event.Harness.Name)
	}
	if event.Event.Action != "prompt.submitted" {
		t.Fatalf("action = %q, want prompt.submitted", event.Event.Action)
	}
	if event.Event.Category != "prompt" {
		t.Fatalf("category = %q, want prompt", event.Event.Category)
	}
	if event.Prompt == nil || event.Prompt.Text != "summarize this deployment" {
		t.Fatalf("prompt = %#v, want captured prompt text", event.Prompt)
	}
	if event.GenAI == nil || event.GenAI.Provider == nil || event.GenAI.Provider.Name != "openai" {
		t.Fatalf("gen_ai provider = %#v, want openai", event.GenAI)
	}
	if event.GenAI.Request == nil || event.GenAI.Request.Model != "gpt-4o-mini" {
		t.Fatalf("gen_ai request = %#v, want model", event.GenAI.Request)
	}
	if event.GenAI.Usage == nil || event.GenAI.Usage.InputTokens == nil || *event.GenAI.Usage.InputTokens != 12 {
		t.Fatalf("gen_ai usage input = %#v, want 12", event.GenAI.Usage)
	}
	if event.GenAI.Usage.OutputTokens == nil || *event.GenAI.Usage.OutputTokens != 34 {
		t.Fatalf("gen_ai usage output = %#v, want 34", event.GenAI.Usage)
	}
}

func TestEventsFromTracesNormalizesVercelAISDKSpan(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("ai.generateText")
	attrs := span.Attributes()
	attrs.PutStr("beacon.harness.name", "vercel_ai_sdk")
	attrs.PutStr("beacon.event.action", "prompt.submitted")
	attrs.PutStr("beacon.event.category", "prompt")
	attrs.PutStr("gen_ai.provider.name", "anthropic")
	attrs.PutStr("gen_ai.operation.name", "chat")
	attrs.PutStr("gen_ai.request.model", "claude-3-5-sonnet")
	attrs.PutBool("gen_ai.request.stream", true)
	attrs.PutInt("gen_ai.usage.input_tokens", 42)

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Harness.Name != "vercel_ai_sdk" {
		t.Fatalf("harness = %q, want vercel_ai_sdk", event.Harness.Name)
	}
	if event.Event.Action != "prompt.submitted" || event.Event.Category != "prompt" {
		t.Fatalf("event = %#v, want prompt.submitted prompt", event.Event)
	}
	if event.GenAI == nil || event.GenAI.Provider == nil || event.GenAI.Provider.Name != "anthropic" {
		t.Fatalf("gen_ai provider = %#v, want anthropic", event.GenAI)
	}
	if event.GenAI.Request == nil || event.GenAI.Request.Model != "claude-3-5-sonnet" {
		t.Fatalf("gen_ai request = %#v, want model", event.GenAI.Request)
	}
	if event.GenAI.Request.Stream == nil || !*event.GenAI.Request.Stream {
		t.Fatalf("gen_ai stream = %#v, want true", event.GenAI.Request)
	}
	if event.GenAI.Usage == nil || event.GenAI.Usage.InputTokens == nil || *event.GenAI.Usage.InputTokens != 42 {
		t.Fatalf("gen_ai usage input = %#v, want 42", event.GenAI.Usage)
	}
}

func TestEventsFromTracesNormalizesClaudeAgentSDKSpan(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("claude_agent_sdk.query")
	attrs := span.Attributes()
	attrs.PutStr("beacon.harness.name", "claude_agent_sdk")
	attrs.PutStr("beacon.event.action", "prompt.submitted")
	attrs.PutStr("beacon.event.category", "prompt")
	attrs.PutStr("beacon.prompt.text", "review this pull request")

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Harness.Name != "claude_agent_sdk" {
		t.Fatalf("harness = %q, want claude_agent_sdk", event.Harness.Name)
	}
	if event.Event.Action != "prompt.submitted" || event.Event.Category != "prompt" {
		t.Fatalf("event = %#v, want prompt.submitted prompt", event.Event)
	}
	if event.Prompt == nil || event.Prompt.Text != "review this pull request" {
		t.Fatalf("prompt = %#v, want captured prompt text", event.Prompt)
	}
}

func TestEventFromSpanNormalizesClaudeCodeLLMRequestUsage(t *testing.T) {
	// Claude Code's claude_code.llm_request span records token usage under bare
	// attribute names (input_tokens, output_tokens, cache_read_tokens,
	// cache_creation_tokens), not the gen_ai.usage.* semconv names. These must
	// normalize into the canonical gen_ai.usage so the per-step session
	// drilldown and span-level attribution carry real usage.
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "claude-code")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("claude_code.llm_request")
	span.Attributes().PutStr("session.id", "session-span")
	span.Attributes().PutStr("model", "claude-sonnet-4-5")
	span.Attributes().PutInt("input_tokens", 1200)
	span.Attributes().PutInt("output_tokens", 340)
	span.Attributes().PutInt("cache_read_tokens", 8000)
	span.Attributes().PutInt("cache_creation_tokens", 256)

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	usage := events[0].GenAI.Usage
	if usage == nil {
		t.Fatalf("usage missing on span event: %#v", events[0].GenAI)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 1200 {
		t.Fatalf("input_tokens = %v, want 1200", usage.InputTokens)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 340 {
		t.Fatalf("output_tokens = %v, want 340", usage.OutputTokens)
	}
	if usage.CacheRead == nil || usage.CacheRead.InputTokens == nil || *usage.CacheRead.InputTokens != 8000 {
		t.Fatalf("cache_read = %#v, want 8000", usage.CacheRead)
	}
	if usage.CacheCreation == nil || usage.CacheCreation.InputTokens == nil || *usage.CacheCreation.InputTokens != 256 {
		t.Fatalf("cache_creation = %#v, want 256", usage.CacheCreation)
	}
}

func TestEventsFromTracesKeepsAndNormalizesCodexTurnUsage(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex_exec")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	internal := spans.AppendEmpty()
	internal.SetName("handle_responses")

	turn := spans.AppendEmpty()
	turn.SetName("session_task.turn")
	turn.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0).UTC()))
	turn.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000005, 0).UTC()))
	turn.Attributes().PutStr("thread.id", "codex-session")
	turn.Attributes().PutStr("turn.id", "codex-turn")
	turn.Attributes().PutStr("model", "gpt-5.6-sol")
	turn.Attributes().PutInt("codex.turn.token_usage.input_tokens", 13347)
	turn.Attributes().PutInt("codex.turn.token_usage.cached_input_tokens", 0)
	turn.Attributes().PutInt("codex.turn.token_usage.cache_write_input_tokens", 13344)
	turn.Attributes().PutInt("codex.turn.token_usage.non_cached_input_tokens", 13347)
	turn.Attributes().PutInt("codex.turn.token_usage.output_tokens", 6)
	turn.Attributes().PutInt("codex.turn.token_usage.reasoning_output_tokens", 2)
	turn.Attributes().PutInt("codex.turn.token_usage.total_tokens", 13353)

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("events = %d, want only the usage-bearing turn span", len(events))
	}
	event := events[0]
	if event.Event.Action != "token.usage" || event.Event.Category != "metric" || event.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("event = %#v, want observed token.usage metric", event.Event)
	}
	if event.Session == nil || event.Session.ID != "codex-session" {
		t.Fatalf("session = %#v, want codex-session", event.Session)
	}
	if event.Model != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol", event.Model)
	}
	if event.Timestamp != "2023-11-14T22:13:25.000000000Z" {
		t.Fatalf("timestamp = %q, want turn completion time", event.Timestamp)
	}
	usage := event.GenAI.Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 3 {
		t.Fatalf("input usage = %#v, want 3 uncached tokens", usage)
	}
	if usage.CacheRead == nil || usage.CacheRead.InputTokens == nil || *usage.CacheRead.InputTokens != 0 {
		t.Fatalf("cache read = %#v, want explicit zero", usage.CacheRead)
	}
	if usage.CacheCreation == nil || usage.CacheCreation.InputTokens == nil || *usage.CacheCreation.InputTokens != 13344 {
		t.Fatalf("cache creation = %#v, want 13344", usage.CacheCreation)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 6 {
		t.Fatalf("output usage = %#v, want 6", usage)
	}
	if usage.Reasoning == nil || usage.Reasoning.OutputTokens == nil || *usage.Reasoning.OutputTokens != 2 {
		t.Fatalf("reasoning usage = %#v, want 2", usage)
	}
	if event.Raw["source"] != "codex_turn_span" || event.Raw["turn_id"] != "codex-turn" {
		t.Fatalf("raw source metadata = %#v", event.Raw)
	}
}

func TestEventFromSpanCapturesTraceIdentity(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("agent.step")
	span.SetTraceID(pcommon.TraceID([16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}))
	span.SetSpanID(pcommon.SpanID([8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}))
	span.SetParentSpanID(pcommon.SpanID([8]byte{0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}))

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	trace := events[0].Trace
	if trace == nil {
		t.Fatalf("trace identity missing: %#v", events[0])
	}
	if trace.ID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("trace.id = %q, want hex trace id", trace.ID)
	}
	if trace.SpanID != "0123456789abcdef" {
		t.Fatalf("trace.span_id = %q, want hex span id", trace.SpanID)
	}
	if trace.ParentSpanID != "fedcba9876543210" {
		t.Fatalf("trace.parent_span_id = %q, want hex parent span id", trace.ParentSpanID)
	}
}

func TestEventFromSpanOmitsTraceWhenUnset(t *testing.T) {
	_, traces := newObserveSDKTraceSpan("agent.step")

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Trace != nil {
		t.Fatalf("trace = %#v, want nil for span without trace identity", events[0].Trace)
	}
}

func TestEventFromLogCapturesTraceContext(t *testing.T) {
	logs := plog.NewLogs()
	resourceLogs := logs.ResourceLogs().AppendEmpty()
	scopeLogs := resourceLogs.ScopeLogs().AppendEmpty()
	record := scopeLogs.LogRecords().AppendEmpty()
	record.Body().SetStr("model call completed")
	record.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0).UTC()))
	record.SetTraceID(pcommon.TraceID([16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}))
	record.SetSpanID(pcommon.SpanID([8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}))

	events := NewConverter(Options{}).EventsFromLogs(logs)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	trace := events[0].Trace
	if trace == nil || trace.ID != "0123456789abcdef0123456789abcdef" || trace.SpanID != "0123456789abcdef" {
		t.Fatalf("trace = %#v, want log trace context", trace)
	}
	if trace.ParentSpanID != "" {
		t.Fatalf("trace.parent_span_id = %q, want empty for logs", trace.ParentSpanID)
	}
}

func TestEventsFromMetricsExpandsClaudeCodeTokenUsageDataPoints(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "claude-code")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("claude_code.token.usage")
	metric.SetUnit("tokens")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	sum.SetIsMonotonic(true)

	ts := pcommon.NewTimestampFromTime(time.Unix(1700000100, 0).UTC())
	for tokenType, value := range map[string]int64{
		"input":         120,
		"output":        45,
		"cacheRead":     90,
		"cacheCreation": 30,
	} {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetTimestamp(ts)
		dp.SetIntValue(value)
		dp.Attributes().PutStr("type", tokenType)
		dp.Attributes().PutStr("model", "claude-sonnet-4-5")
		dp.Attributes().PutStr("session.id", "session-123")
	}

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 4 {
		t.Fatalf("expected 4 events (one per datapoint), got %d", len(events))
	}
	got := map[string]int64{}
	for _, event := range events {
		if event.Event.Action != "token.usage" || event.Event.Category != "metric" {
			t.Fatalf("event = %#v, want token.usage metric", event.Event)
		}
		if event.Harness.Name != "claude_code" {
			t.Fatalf("harness = %q, want claude_code", event.Harness.Name)
		}
		if event.Model != "claude-sonnet-4-5" {
			t.Fatalf("model = %q, want datapoint model attribute", event.Model)
		}
		if event.Session == nil || event.Session.ID != "session-123" {
			t.Fatalf("session = %#v, want datapoint session attribute", event.Session)
		}
		if !event.ObservedAt.Equal(time.Unix(1700000100, 0).UTC()) {
			t.Fatalf("timestamp = %v, want datapoint timestamp", event.ObservedAt)
		}
		if event.Raw["metric_temporality"] != "Delta" || event.Raw["metric_monotonic"] != true {
			t.Fatalf("raw temporality = %#v, want Delta monotonic", event.Raw)
		}
		usage := event.GenAI.Usage
		if usage == nil {
			t.Fatalf("usage missing on event: %#v", event.GenAI)
		}
		switch {
		case usage.InputTokens != nil:
			got["input"] = *usage.InputTokens
		case usage.OutputTokens != nil:
			got["output"] = *usage.OutputTokens
		case usage.CacheRead != nil && usage.CacheRead.InputTokens != nil:
			got["cacheRead"] = *usage.CacheRead.InputTokens
		case usage.CacheCreation != nil && usage.CacheCreation.InputTokens != nil:
			got["cacheCreation"] = *usage.CacheCreation.InputTokens
		default:
			t.Fatalf("usage has no recognized token field: %#v", usage)
		}
	}
	want := map[string]int64{"input": 120, "output": 45, "cacheRead": 90, "cacheCreation": 30}
	for tokenType, value := range want {
		if got[tokenType] != value {
			t.Fatalf("token usage %s = %d, want %d (all: %#v)", tokenType, got[tokenType], value, got)
		}
	}
}

func TestEventsFromMetricsTokenUsageIgnoresStrayUsageAttributes(t *testing.T) {
	// A token.usage datapoint event must carry only the value from its own
	// datapoint, not gen_ai.usage.* attributes that happen to ride along on the
	// resource or datapoint. Otherwise the stray field is attached to every
	// expanded datapoint event and double-counted by tokens.Aggregate.
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "claude-code")
	// Stray usage attribute that overlaps the per-datapoint token type.
	resourceMetrics.Resource().Attributes().PutInt("gen_ai.usage.input_tokens", 999)
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("claude_code.token.usage")
	metric.SetUnit("tokens")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	sum.SetIsMonotonic(true)
	ts := pcommon.NewTimestampFromTime(time.Unix(1700000200, 0).UTC())
	for _, tokenType := range []string{"input", "output"} {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetTimestamp(ts)
		dp.SetIntValue(10)
		dp.Attributes().PutStr("type", tokenType)
	}

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for _, event := range events {
		usage := event.GenAI.Usage
		if usage == nil {
			t.Fatalf("usage missing on event: %#v", event.GenAI)
		}
		if usage.OutputTokens != nil {
			// The output datapoint event must not also carry the stray input.
			if usage.InputTokens != nil {
				t.Fatalf("output datapoint leaked input_tokens=%d from stray attribute", *usage.InputTokens)
			}
			continue
		}
		if usage.InputTokens == nil || *usage.InputTokens != 10 {
			t.Fatalf("input datapoint = %#v, want input_tokens=10 from datapoint value", usage)
		}
	}
}

func TestEventsFromMetricsCapturesCostUsage(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "claude-code")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("claude_code.cost.usage")
	metric.SetUnit("USD")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetDoubleValue(0.42)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000200, 0).UTC()))
	dp.Attributes().PutStr("model", "claude-sonnet-4-5")

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Event.Action != "cost.usage" {
		t.Fatalf("action = %q, want cost.usage", event.Event.Action)
	}
	if event.GenAI == nil || event.GenAI.Usage == nil || event.GenAI.Usage.CostUSD == nil || *event.GenAI.Usage.CostUSD != 0.42 {
		t.Fatalf("usage = %#v, want cost_usd 0.42", event.GenAI)
	}
	if event.Model != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want datapoint model attribute", event.Model)
	}
	if event.Raw["metric_value"] != 0.42 || event.Raw["metric_unit"] != "USD" {
		t.Fatalf("raw payload = %#v, want metric_value and unit", event.Raw)
	}
}

func TestEventsFromMetricsCapturesTokenUsageHistogram(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("beacon.harness.name", "asymptote_observe")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("gen_ai.client.token.usage")
	histogram := metric.SetEmptyHistogram()
	histogram.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := histogram.DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000300, 0).UTC()))
	dp.SetCount(3)
	dp.SetSum(456)
	dp.Attributes().PutStr("gen_ai.token.type", "output")
	dp.Attributes().PutStr("gen_ai.request.model", "gpt-4o-mini")

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.GenAI == nil || event.GenAI.Usage == nil || event.GenAI.Usage.OutputTokens == nil || *event.GenAI.Usage.OutputTokens != 456 {
		t.Fatalf("usage = %#v, want output 456", event.GenAI)
	}
	if event.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want gpt-4o-mini", event.Model)
	}
	if event.Raw["metric_count"] != int64(3) {
		t.Fatalf("raw metric_count = %#v, want 3", event.Raw["metric_count"])
	}
}

func TestEventsFromMetricsNormalizesCodexTurnTokenUsage(t *testing.T) {
	// Codex reports per-turn usage only on the codex.turn.token_usage histogram,
	// split by a token_type dimension. It must survive the codex.* metric drop
	// and normalize into gen_ai.usage; the "total" rollup must not add usage.
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("beacon.harness.name", "codex_cli")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("codex.turn.token_usage")
	histogram := metric.SetEmptyHistogram()
	histogram.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	// Codex's input (120) is inclusive of cached_input (90) and
	// cache_write_input (10). The normalized input_tokens must drop to the
	// uncached portion (20) so the canonical fields remain disjoint.
	tokenTypes := map[string]float64{
		"input":             120,
		"cached_input":      90,
		"cache_write_input": 10,
		"output":            45,
		"reasoning_output":  30,
		"total":             165,
	}
	for tokenType, value := range tokenTypes {
		dp := histogram.DataPoints().AppendEmpty()
		dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000400, 0).UTC()))
		dp.SetCount(1)
		dp.SetSum(value)
		dp.Attributes().PutStr("token_type", tokenType)
	}

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != len(tokenTypes) {
		t.Fatalf("expected %d events (one per token_type), got %d", len(tokenTypes), len(events))
	}
	var input, cacheRead, cacheCreation, output, reasoning int64
	for _, event := range events {
		if event.Event.Action != "token.usage" || event.Harness.Name != "codex_cli" {
			t.Fatalf("event = %#v, want codex token.usage", event.Event)
		}
		usage := event.GenAI.Usage
		if usage == nil {
			continue
		}
		if usage.InputTokens != nil {
			input += *usage.InputTokens
		}
		if usage.OutputTokens != nil {
			output += *usage.OutputTokens
		}
		if usage.CacheRead != nil && usage.CacheRead.InputTokens != nil {
			cacheRead += *usage.CacheRead.InputTokens
		}
		if usage.CacheCreation != nil && usage.CacheCreation.InputTokens != nil {
			cacheCreation += *usage.CacheCreation.InputTokens
		}
		if usage.Reasoning != nil && usage.Reasoning.OutputTokens != nil {
			reasoning += *usage.Reasoning.OutputTokens
		}
	}
	if input != 20 || cacheRead != 90 || cacheCreation != 10 || output != 45 || reasoning != 30 {
		t.Fatalf("normalized usage = input %d, cache_read %d, cache_creation %d, output %d, reasoning %d (want 20/90/10/45/30)", input, cacheRead, cacheCreation, output, reasoning)
	}
	if input+cacheRead+cacheCreation != 120 {
		t.Fatalf("input + cache_read + cache_creation = %d, want 120 (= Codex input, no double-count)", input+cacheRead+cacheCreation)
	}
}

func TestEventsFromMetricsUnknownTokenTypeKeepsRawValueOnly(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "claude-code")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("claude_code.token.usage")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetIntValue(7)
	dp.Attributes().PutStr("type", "speculative")

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	usage := event.GenAI.Usage
	if usage != nil && (usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheRead != nil || usage.CacheCreation != nil || usage.Reasoning != nil) {
		t.Fatalf("unknown token type populated usage: %#v", usage)
	}
	if event.GenAI.Token == nil || event.GenAI.Token.Type != "speculative" {
		t.Fatalf("token type = %#v, want speculative recorded", event.GenAI.Token)
	}
	if event.Raw["metric_value"] != float64(7) {
		t.Fatalf("raw metric_value = %#v, want 7", event.Raw["metric_value"])
	}
}

func TestEventsFromMetricTokenUsageWithoutDataPointsFallsBack(t *testing.T) {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	resourceMetrics.Resource().Attributes().PutStr("service.name", "claude-code")
	metric := resourceMetrics.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("claude_code.token.usage")

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 1 {
		t.Fatalf("expected 1 fallback event, got %d", len(events))
	}
	if events[0].Event.Action != "metric.observed" {
		t.Fatalf("action = %q, want metric.observed fallback", events[0].Event.Action)
	}
}

func TestGenAIUsageFromAttrsNormalizesAliases(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]interface{}
		check func(t *testing.T, usage *GenAIUsageInfo)
	}{
		{
			name:  "underscore cache read alias",
			attrs: map[string]interface{}{"gen_ai.usage.cache_read_input_tokens": int64(90)},
			check: func(t *testing.T, usage *GenAIUsageInfo) {
				if usage.CacheRead == nil || usage.CacheRead.InputTokens == nil || *usage.CacheRead.InputTokens != 90 {
					t.Fatalf("cache_read = %#v, want 90", usage.CacheRead)
				}
			},
		},
		{
			name:  "underscore cache creation alias",
			attrs: map[string]interface{}{"gen_ai.usage.cache_creation_input_tokens": int64(30)},
			check: func(t *testing.T, usage *GenAIUsageInfo) {
				if usage.CacheCreation == nil || usage.CacheCreation.InputTokens == nil || *usage.CacheCreation.InputTokens != 30 {
					t.Fatalf("cache_creation = %#v, want 30", usage.CacheCreation)
				}
			},
		},
		{
			name:  "runtime reported cost attribute",
			attrs: map[string]interface{}{"gen_ai.usage.cost": 0.0123},
			check: func(t *testing.T, usage *GenAIUsageInfo) {
				if usage.CostUSD == nil || *usage.CostUSD != 0.0123 {
					t.Fatalf("cost_usd = %#v, want 0.0123", usage.CostUSD)
				}
			},
		},
		{
			name:  "semconv dotted names take precedence",
			attrs: map[string]interface{}{"gen_ai.usage.cache_read.input_tokens": int64(7), "gen_ai.usage.cache_read_input_tokens": int64(99)},
			check: func(t *testing.T, usage *GenAIUsageInfo) {
				if usage.CacheRead == nil || usage.CacheRead.InputTokens == nil || *usage.CacheRead.InputTokens != 7 {
					t.Fatalf("cache_read = %#v, want semconv value 7", usage.CacheRead)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := GenAIUsageFromAttrs(tt.attrs)
			if usage == nil {
				t.Fatalf("usage = nil, want populated usage")
			}
			tt.check(t, usage)
		})
	}
}

func TestPopulateCommonMapsBeaconSessionAttributes(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("agent.step")
	span.Attributes().PutStr("beacon.session.id", "cloud-session-42")
	span.Attributes().PutStr("beacon.session.working_directory", "/srv/agent")

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	session := events[0].Session
	if session == nil || session.ID != "cloud-session-42" {
		t.Fatalf("session = %#v, want beacon.session.id mapped", session)
	}
	if session.WorkingDirectory != "/srv/agent" {
		t.Fatalf("working directory = %q, want beacon.session.working_directory mapped", session.WorkingDirectory)
	}
}

func TestEventsFromTracesMapsMCPGenAIClientSpanAttributes(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("tools/call get_organizations")
	attrs := span.Attributes()
	attrs.PutStr("gen_ai.operation.name", "execute_tool")
	attrs.PutStr("gen_ai.tool.name", "get_organizations")
	attrs.PutStr("gen_ai.tool.call.id", "call-1")
	attrs.PutStr("gen_ai.tool.call.arguments", `{"org":"meulo"}`)
	attrs.PutStr("gen_ai.tool.call.result", `{"ok":true}`)
	attrs.PutStr("mcp.method.name", "tools/call")
	attrs.PutStr("mcp.protocol.version", "2025-06-18")
	attrs.PutStr("mcp.resource.uri", "file:///tmp/report.md")
	attrs.PutStr("mcp.session.id", "mcp-session")
	attrs.PutStr("jsonrpc.request.id", "request-7")
	attrs.PutStr("jsonrpc.protocol.version", "2.0")
	attrs.PutStr("rpc.response.status_code", "OK")
	attrs.PutStr("network.protocol.name", "HTTP")
	attrs.PutStr("network.protocol.version", "2")
	attrs.PutStr("network.transport", "TCP")
	attrs.PutStr("server.address", "example.com")
	attrs.PutInt("server.port", 443)
	attrs.PutStr("error.type", "tool_error")

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Event.Action != "mcp.tool_invoked" || event.Event.Category != "mcp" {
		t.Fatalf("event = %#v, want mcp.tool_invoked/mcp", event.Event)
	}
	if event.GenAI == nil || event.GenAI.Operation == nil || event.GenAI.Operation.Name != "execute_tool" {
		t.Fatalf("gen_ai operation = %#v, want execute_tool", event.GenAI)
	}
	if event.GenAI.Tool == nil || event.GenAI.Tool.Name != "get_organizations" || event.GenAI.Tool.Call == nil {
		t.Fatalf("gen_ai tool = %#v, want get_organizations call", event.GenAI.Tool)
	}
	if event.MCP == nil || event.MCP.Method == nil || event.MCP.Method.Name != "tools/call" || event.MCP.Protocol == nil || event.MCP.Protocol.Version != "2025-06-18" {
		t.Fatalf("mcp = %#v, want method/protocol", event.MCP)
	}
	if event.JSONRPC == nil || event.JSONRPC.Request == nil || event.JSONRPC.Request.ID != "request-7" || event.JSONRPC.Protocol == nil || event.JSONRPC.Protocol.Version != "2.0" {
		t.Fatalf("jsonrpc = %#v, want request/protocol", event.JSONRPC)
	}
	if event.Network == nil || event.Network.Protocol == nil || event.Network.Protocol.Name != "http" || event.Network.Transport != "tcp" {
		t.Fatalf("network = %#v, want lower-cased protocol/transport", event.Network)
	}
	if event.RPC == nil || event.RPC.Response == nil || event.RPC.Response.StatusCode != "OK" {
		t.Fatalf("rpc = %#v, want OK response status", event.RPC)
	}
	if event.Server == nil || event.Server.Address != "example.com" || event.Server.Port == nil || *event.Server.Port != 443 {
		t.Fatalf("server = %#v, want example.com:443", event.Server)
	}
	if event.Error == nil || event.Error.Type != "tool_error" {
		t.Fatalf("error = %#v, want tool_error", event.Error)
	}
}

func TestEventsFromTracesMapsBeaconMCPAliases(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("beacon mcp tool")
	attrs := span.Attributes()
	attrs.PutStr("beacon.tool.name", "alias_tool")
	attrs.PutStr("beacon.tool.command", "alias command")
	attrs.PutStr("beacon.tool.path", "/tmp/input.txt")
	attrs.PutStr("beacon.mcp.server", "clickhouse")
	attrs.PutStr("beacon.mcp.tool", "query")
	attrs.PutStr("mcp.method.name", "tools/call")

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Event.Action != "mcp.tool_invoked" {
		t.Fatalf("event.action = %q, want mcp.tool_invoked", event.Event.Action)
	}
	if event.Tool == nil || event.Tool.Name != "alias_tool" || event.Tool.Command != "alias command" || event.Tool.Path != "/tmp/input.txt" {
		t.Fatalf("tool = %#v, want beacon.tool aliases", event.Tool)
	}
	if event.MCP == nil || event.MCP.Server != "clickhouse" || event.MCP.Tool != "query" {
		t.Fatalf("mcp = %#v, want beacon.mcp aliases", event.MCP)
	}
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Name != "alias_tool" {
		t.Fatalf("gen_ai tool = %#v, want beacon.tool.name fallback", event.GenAI)
	}
}

func TestEventsFromTracesDoesNotPromotePlainGenAIToolToMCP(t *testing.T) {
	span, traces := newObserveSDKTraceSpan("execute_tool plain_tool")
	attrs := span.Attributes()
	attrs.PutStr("gen_ai.operation.name", "execute_tool")
	attrs.PutStr("gen_ai.tool.name", "plain_tool")

	events := NewConverter(Options{}).EventsFromTraces(traces)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Event.Action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", event.Event.Action)
	}
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Name != "plain_tool" {
		t.Fatalf("gen_ai tool = %#v, want plain_tool", event.GenAI)
	}
	if event.MCP != nil {
		t.Fatalf("plain GenAI tool should not populate MCP: %#v", event.MCP)
	}
}

func TestCapturedClaudeCodeLogNormalization(t *testing.T) {
	fixture, events := capturedLogEvents(t, "claude-code-2.1.220.json")
	if len(events) != len(fixture.Records) {
		t.Fatalf("events = %d, want %d", len(events), len(fixture.Records))
	}
	byName := map[string]Event{}
	for i, record := range fixture.Records {
		byName[record.Name] = events[i]
	}

	apiRequest := byName["api_request"]
	if apiRequest.Event.Action != "session.activity" || apiRequest.Event.Category != "session" {
		t.Fatalf("api request event = %#v, want session.activity/session", apiRequest.Event)
	}
	if apiRequest.GenAI == nil || apiRequest.GenAI.Usage == nil || apiRequest.GenAI.Usage.InputTokens == nil || *apiRequest.GenAI.Usage.InputTokens != 2 {
		t.Fatalf("api request usage = %#v, want captured input tokens", apiRequest.GenAI)
	}

	decision := byName["bash_decision"]
	if decision.Event.Action != "approval.allowed" || decision.Event.Category != "approval" {
		t.Fatalf("decision event = %#v, want approval.allowed/approval", decision.Event)
	}
	if decision.Approval == nil || !decision.Approval.Required || decision.Approval.Decision != "accept" || decision.Approval.Reason != "config" {
		t.Fatalf("approval = %#v, want captured accept/config decision", decision.Approval)
	}
	if decision.Command == nil || decision.Command.Command != "echo CLAUDE_MARKER" {
		t.Fatalf("decision command = %#v, want full command", decision.Command)
	}

	bash := byName["bash_result"]
	if bash.Event.Action != "command.executed" || bash.Event.Category != "command" {
		t.Fatalf("bash event = %#v, want command.executed/command", bash.Event)
	}
	if bash.Command == nil || bash.Command.Command != "echo CLAUDE_MARKER" || bash.Command.DurationMS != 94 {
		t.Fatalf("bash command = %#v, want command and string duration", bash.Command)
	}
	if bash.Command.ExitCode != nil {
		t.Fatalf("bash exit code = %#v, want nil without an exact process status", bash.Command.ExitCode)
	}
	if bash.Tool == nil || bash.Tool.Name != "Bash" || bash.Tool.Command != "echo CLAUDE_MARKER" {
		t.Fatalf("bash tool = %#v, want normalized command", bash.Tool)
	}
	rawAttrs, ok := bash.Raw["attributes"].(map[string]interface{})
	if !ok || rawAttrs["tool_input"] != fixture.Records[2].Attributes["tool_input"] {
		t.Fatalf("raw attributes changed: %#v", bash.Raw)
	}

	// Claude Code names every tool call with a tool_use_id and puts the same one
	// on the decision and the result. Promoting it is what links an approval to
	// the execution it approved, and this event to the hook's report of the same
	// command; until it was promoted the value reached the log only inside raw.
	if got := toolCallIDOfEvent(decision); got != "toolu_fixture_bash" {
		t.Fatalf("decision call ID = %q, want the tool_use_id Claude Code assigned", got)
	}
	if got := toolCallIDOfEvent(bash); got != "toolu_fixture_bash" {
		t.Fatalf("bash call ID = %q, want the tool_use_id Claude Code assigned", got)
	}

	for _, tc := range []struct {
		name      string
		action    string
		operation string
	}{
		{name: "write_result", action: "file.modified", operation: "create"},
		{name: "read_result", action: "file.read", operation: "read"},
	} {
		event := byName[tc.name]
		if event.Event.Action != tc.action || event.Event.Category != "file" {
			t.Fatalf("%s event = %#v, want %s/file", tc.name, event.Event, tc.action)
		}
		if event.File == nil || event.File.Path != "/tmp/beacon-otel-fixture/CLAUDE_FILE_MARKER.txt" || event.File.Operation != tc.operation {
			t.Fatalf("%s file = %#v, want captured path/%s", tc.name, event.File, tc.operation)
		}
		if event.Tool == nil || event.Tool.Path != event.File.Path {
			t.Fatalf("%s tool = %#v, want mirrored path", tc.name, event.Tool)
		}
		if toolCallIDOfEvent(event) == "" {
			t.Fatalf("%s has no call ID, so nothing links it to the hook's report of the same edit", tc.name)
		}
	}
	if toolCallIDOfEvent(byName["write_result"]) == toolCallIDOfEvent(byName["read_result"]) {
		t.Fatal("the write and the read are separate calls and must not share a call ID")
	}
}

func toolCallIDOfEvent(event Event) string {
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Call == nil {
		return ""
	}
	return event.GenAI.Tool.Call.ID
}

// Codex names its calls call_id rather than tool_use_id. Both are read from one
// shared alias list, so a runtime cannot be supported on one capture path and
// invisible on the other.
func TestCapturedCodexLogPromotesItsOwnCallID(t *testing.T) {
	_, events := capturedLogEvents(t, "codex-0.142.4.json")
	found := false
	for _, event := range events {
		if id := toolCallIDOfEvent(event); id != "" {
			if id != "call_fixture" {
				t.Fatalf("call ID = %q, want the call_id Codex assigned", id)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no event promoted Codex's call_id")
	}
}

func TestCapturedClaudeCodeLifecycleTaxonomy(t *testing.T) {
	fixture, events := capturedLogEvents(t, "claude-code-lifecycle-2.1.220.json")
	if len(events) != len(fixture.Records) {
		t.Fatalf("events = %d, want %d", len(events), len(fixture.Records))
	}
	expected := map[string]eventClassification{
		"user_prompt":             {action: "prompt.submitted", category: "prompt"},
		"assistant_response":      {action: "session.activity", category: "session"},
		"api_request":             {action: "session.activity", category: "session"},
		"api_error":               {action: "session.error", category: "session"},
		"api_refusal":             {action: "session.status", category: "session"},
		"api_request_body":        {action: "session.activity", category: "session"},
		"api_response_body":       {action: "session.activity", category: "session"},
		"permission_mode_changed": {action: "session.status", category: "session"},
		"auth":                    {action: "session.activity", category: "session"},
		"mcp_server_connection":   {action: "mcp.connection", category: "mcp"},
		"internal_error":          {action: "session.error", category: "session"},
		"plugin_installed":        {action: "session.activity", category: "session"},
		"plugin_loaded":           {action: "session.activity", category: "session"},
		"skill_activated":         {action: "session.activity", category: "session"},
		"at_mention":              {action: "session.activity", category: "session"},
		"api_retries_exhausted":   {action: "session.error", category: "session"},
		"hook_registered":         {action: "session.activity", category: "session"},
		"hook_execution_start":    {action: "session.activity", category: "session"},
		"hook_execution_complete": {action: "session.activity", category: "session"},
		"hook_plugin_metrics":     {action: "session.activity", category: "session"},
		"compaction":              {action: "session.activity", category: "session"},
		"feedback_survey":         {action: "session.activity", category: "session"},
	}
	if len(claudeLogEventClassifications) != len(expected)+2 {
		t.Fatalf("documented Claude taxonomy has %d events, want %d", len(claudeLogEventClassifications), len(expected)+2)
	}
	for i, captured := range fixture.Records {
		want, ok := expected[captured.Name]
		if !ok {
			t.Fatalf("fixture %q has no expected classification", captured.Name)
		}
		event := events[i]
		if event.Event.Action != want.action || event.Event.Category != want.category {
			t.Errorf("%s event = %#v, want %s/%s", captured.Name, event.Event, want.action, want.category)
		}
		switch event.Event.Action {
		case "tool.invoked", "command.executed", "mcp.tool_invoked":
			t.Errorf("%s lifecycle event incorrectly classified as %s", captured.Name, event.Event.Action)
		}
	}
	if prompt := events[0].Prompt; prompt == nil || prompt.Text != "CLAUDE_LIFECYCLE_MARKER" {
		t.Fatalf("user prompt = %#v, want captured prompt", prompt)
	}
}

func TestClaudeLogEventNameRecognizesKnownShortNames(t *testing.T) {
	for eventName := range claudeLogEventClassifications {
		shortName := strings.TrimPrefix(eventName, "claude_code.")
		if got := ClaudeLogEventName(map[string]interface{}{"event.name": shortName}, "unstructured body"); got != eventName {
			t.Errorf("short event %q normalized to %q, want %q", shortName, got, eventName)
		}
	}
}

func TestClaudeLifecycleUsesResourceHarnessIdentity(t *testing.T) {
	logs := plog.NewLogs()
	resourceLogs := logs.ResourceLogs().AppendEmpty()
	resourceLogs.Resource().Attributes().PutStr("service.name", "claude-code")
	resourceLogs.Resource().Attributes().PutStr("service.version", "2.1.220")
	record := resourceLogs.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.Body().SetStr("claude_code.assistant_response")
	record.Attributes().PutStr("event.name", "assistant_response")

	events := NewConverter(Options{}).EventsFromLogs(logs)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Harness.Name != "claude_code" || events[0].Event.Action != "session.activity" || events[0].Event.Category != "session" {
		t.Fatalf("event = %#v, want resource-scoped Claude session activity", events[0])
	}
}

func TestClaudeUnknownFullBodyDefaultsToSessionActivity(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.future_lifecycle_event")
	record.Attributes().PutStr("service.name", "claude-code")
	record.Attributes().PutStr("event.name", "future_lifecycle_event")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "session.activity" || event.Event.Category != "session" {
		t.Fatalf("event = %#v, want safe session.activity fallback", event.Event)
	}
	if got := ClaudeLogEventName(map[string]interface{}{"event.name": "future_lifecycle_event"}, "unstructured body"); got != "" {
		t.Fatalf("unknown short event normalized to %q, want no match", got)
	}
	if got := ClaudeLogEventName(map[string]interface{}{"event.name": "claude_code.future_lifecycle_event"}, "unstructured body"); got != "claude_code.future_lifecycle_event" {
		t.Fatalf("unknown namespaced event normalized to %q, want safe full-name match", got)
	}

	namespaced := plog.NewLogRecord()
	namespaced.Body().SetStr("unstructured body")
	namespaced.Attributes().PutStr("service.name", "claude-code")
	namespaced.Attributes().PutStr("event.name", "claude_code.future_lifecycle_event")
	namespacedEvent := NewConverter(Options{}).EventFromLog(nil, namespaced)
	if namespacedEvent.Event.Action != "session.activity" || namespacedEvent.Event.Category != "session" {
		t.Fatalf("namespaced event = %#v, want safe session.activity fallback", namespacedEvent.Event)
	}
}

func TestClaudeNormalizationPreservesExplicitClassification(t *testing.T) {
	t.Run("explicit action keeps inferred category", func(t *testing.T) {
		record := plog.NewLogRecord()
		record.Body().SetStr("claude_code.assistant_response")
		record.Attributes().PutStr("service.name", "claude-code")
		record.Attributes().PutStr("event.name", "assistant_response")
		record.Attributes().PutStr("beacon.event.action", "prompt.submitted")

		event := NewConverter(Options{}).EventFromLog(nil, record)
		if event.Event.Action != "prompt.submitted" || event.Event.Category != "prompt" {
			t.Fatalf("event = %#v, want explicit prompt classification", event.Event)
		}
	})

	t.Run("explicit category overrides taxonomy category", func(t *testing.T) {
		record := plog.NewLogRecord()
		record.Body().SetStr("claude_code.assistant_response")
		record.Attributes().PutStr("service.name", "claude-code")
		record.Attributes().PutStr("event.name", "assistant_response")
		record.Attributes().PutStr("beacon.event.category", "custom")

		event := NewConverter(Options{}).EventFromLog(nil, record)
		if event.Event.Action != "session.activity" || event.Event.Category != "custom" {
			t.Fatalf("event = %#v, want taxonomy action with explicit category", event.Event)
		}
	})

	t.Run("tool operands still normalize", func(t *testing.T) {
		record := plog.NewLogRecord()
		record.Body().SetStr("claude_code.tool_result")
		record.Attributes().PutStr("service.name", "claude-code")
		record.Attributes().PutStr("event.name", "tool_result")
		record.Attributes().PutStr("event.action", "custom.tool_result")
		record.Attributes().PutStr("event.category", "custom")
		record.Attributes().PutStr("tool_name", "Bash")
		record.Attributes().PutStr("tool_input", `{"command":"echo explicit"}`)
		record.Attributes().PutStr("duration_ms", "12")

		event := NewConverter(Options{}).EventFromLog(nil, record)
		if event.Event.Action != "custom.tool_result" || event.Event.Category != "custom" {
			t.Fatalf("event = %#v, want explicit tool classification", event.Event)
		}
		if event.Command == nil || event.Command.Command != "echo explicit" || event.Command.DurationMS != 12 {
			t.Fatalf("command = %#v, want normalized tool operands", event.Command)
		}
	})
}

func TestClaudeToolDecisionMapsRejectAndCommand(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.tool_decision")
	attrs := record.Attributes()
	attrs.PutStr("service.name", "claude-code")
	attrs.PutStr("event.name", "tool_decision")
	attrs.PutStr("tool_name", "Bash")
	attrs.PutStr("decision", "reject")
	attrs.PutStr("source", "user_reject")
	attrs.PutStr("tool_parameters", `{"bash_command":"rm","full_command":"rm -rf /tmp/example"}`)

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "approval.denied" || event.Event.Category != "approval" {
		t.Fatalf("event = %#v, want approval.denied/approval", event.Event)
	}
	if event.Approval == nil || event.Approval.Decision != "reject" || event.Approval.Reason != "user_reject" {
		t.Fatalf("approval = %#v, want reject/user_reject", event.Approval)
	}
	if event.Command == nil || event.Command.Command != "rm -rf /tmp/example" {
		t.Fatalf("command = %#v, want denied full command", event.Command)
	}
}

func TestClaudeToolResultOperandPrecedenceAndCoercion(t *testing.T) {
	event := NewEvent("tool.invoked", "tool", "info", "claude_code", time.Now())
	NormalizeClaudeToolResult(&event, map[string]interface{}{
		"tool_name":       "Bash",
		"duration_ms":     int64(71),
		"tool_input":      `{"command":"echo input"}`,
		"tool_parameters": `{"full_command":"echo parameters","bash_command":"echo"}`,
		"success":         "true",
	})
	if event.Command == nil || event.Command.Command != "echo input" || event.Command.DurationMS != 71 {
		t.Fatalf("command = %#v, want tool_input command and numeric duration", event.Command)
	}
	if event.Command.ExitCode != nil {
		t.Fatalf("exit code = %#v, want nil", event.Command.ExitCode)
	}
}

func TestClaudeEditToolResultsMapFileOperations(t *testing.T) {
	for _, tc := range []struct {
		toolName  string
		inputKey  string
		operation string
	}{
		{toolName: "Edit", inputKey: "file_path", operation: "modify"},
		{toolName: "NotebookEdit", inputKey: "notebook_path", operation: "modify"},
	} {
		t.Run(tc.toolName, func(t *testing.T) {
			event := NewEvent("tool.invoked", "tool", "info", "claude_code", time.Now())
			NormalizeClaudeToolResult(&event, map[string]interface{}{
				"tool_name":  tc.toolName,
				"tool_input": `{"` + tc.inputKey + `":"/tmp/example.ipynb"}`,
			})
			if event.Event.Action != "file.modified" || event.Event.Category != "file" {
				t.Fatalf("event = %#v, want file.modified/file", event.Event)
			}
			if event.File == nil || event.File.Path != "/tmp/example.ipynb" || event.File.Operation != tc.operation {
				t.Fatalf("file = %#v, want path/%s", event.File, tc.operation)
			}
		})
	}
}

func TestClaudeToolResultToleratesMalformedJSON(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.tool_result")
	attrs := record.Attributes()
	attrs.PutStr("service.name", "claude-code")
	attrs.PutStr("event.name", "tool_result")
	attrs.PutStr("tool_name", "Bash")
	attrs.PutStr("tool_input", "{")
	attrs.PutStr("tool_parameters", "[")
	attrs.PutStr("duration_ms", "not-a-number")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "command.executed" || event.Command == nil {
		t.Fatalf("event = %#v command = %#v, want classified empty command", event.Event, event.Command)
	}
	if event.Command.Command != "" || event.Command.DurationMS != 0 {
		t.Fatalf("command = %#v, want empty values for malformed attributes", event.Command)
	}
}

func TestClaudeMCPToolResultDerivesServerAndTool(t *testing.T) {
	// Claude Code reports an MCP tool call as a tool_result LOG record whose tool_name is
	// "mcp__<server>__<tool>" and carries no structured mcp.* attributes. Before the collector
	// learned this convention, event.action became mcp.tool_invoked (InferAction sees "mcp" in the
	// name) but mcp.server/mcp.tool were empty, so every shipped rule gated on e.mcp.server was
	// silently dead on this runtime. This is the regression guard: the identity in the name must
	// reach the structured fields, exactly as the beacon-hooks path already does.
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.tool_result")
	attrs := record.Attributes()
	attrs.PutStr("service.name", "claude-code")
	attrs.PutStr("event.name", "tool_result")
	attrs.PutStr("tool_name", "mcp__notion__create_page")
	attrs.PutStr("tool_input", `{"title":"notes"}`)

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "mcp.tool_invoked" || event.Event.Category != "mcp" {
		t.Fatalf("event = %#v, want mcp.tool_invoked/mcp", event.Event)
	}
	if event.MCP == nil || event.MCP.Server != "notion" || event.MCP.Tool != "create_page" {
		t.Fatalf("mcp = %#v, want server=notion tool=create_page", event.MCP)
	}
	if event.Tool == nil || event.Tool.Name != "mcp__notion__create_page" {
		t.Fatalf("tool = %#v, want raw name preserved", event.Tool)
	}
}

func TestDeriveMCPServerToolFromName(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantServer string
		wantTool   string
	}{
		{name: "mcp__notion__create_page", wantServer: "notion", wantTool: "create_page"},
		// The tool leaf may itself contain the "__" separator; only the first split is the server.
		{name: "mcp__github__issues__create", wantServer: "github", wantTool: "issues__create"},
		// Cursor's flattened form carries a tool but no server.
		{name: "MCP:search", wantServer: "", wantTool: "search"},
		// A built-in tool is not MCP and must not be promoted (guards the external-tool contract).
		{name: "Bash", wantServer: "", wantTool: ""},
		{name: "mcp__onlyserver", wantServer: "", wantTool: ""},
		{name: "", wantServer: "", wantTool: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, tool := deriveMCPServerToolFromName(tc.name)
			if server != tc.wantServer || tool != tc.wantTool {
				t.Fatalf("derive(%q) = (%q, %q), want (%q, %q)", tc.name, server, tool, tc.wantServer, tc.wantTool)
			}
		})
	}
}

func TestClaudeBuiltinToolResultDoesNotPopulateMCP(t *testing.T) {
	// The name-convention derivation must not leak into built-in tools: a Bash tool_result is a
	// command, never an MCP call, so event.MCP stays nil.
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.tool_result")
	attrs := record.Attributes()
	attrs.PutStr("service.name", "claude-code")
	attrs.PutStr("event.name", "tool_result")
	attrs.PutStr("tool_name", "Bash")
	attrs.PutStr("tool_input", `{"command":"echo hi"}`)

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.MCP != nil {
		t.Fatalf("mcp = %#v, want nil for a built-in tool", event.MCP)
	}
	if event.Event.Action != "command.executed" {
		t.Fatalf("event = %#v, want command.executed", event.Event)
	}
}

func TestCapturedCodexLogNormalizationIsUnchanged(t *testing.T) {
	fixture, events := capturedLogEvents(t, "codex-0.142.4.json")
	if len(events) != len(fixture.Records) {
		t.Fatalf("events = %d, want %d", len(events), len(fixture.Records))
	}
	if events[0].Event.Action != "approval.requested" || events[0].Approval == nil || events[0].Approval.Decision != "approved" {
		t.Fatalf("Codex decision changed: %#v", events[0])
	}
	if events[1].Event.Action != "command.executed" || events[1].Command == nil || events[1].Command.Command != "echo CODEX_MARKER" {
		t.Fatalf("Codex result changed: %#v", events[1])
	}
}

func TestClaudeNormalizerDoesNotAffectOtherHarnesses(t *testing.T) {
	for _, serviceName := range []string{
		"generic-runtime",
		"codex_cli_rs",
		"gemini-cli",
		"github-copilot",
		"copilot-chat",
		"cursor",
		"claude-cowork",
		"claude_agent_sdk",
		"claude_web",
	} {
		t.Run(serviceName, func(t *testing.T) {
			record := plog.NewLogRecord()
			record.Body().SetStr("claude_code.assistant_response")
			attrs := record.Attributes()
			attrs.PutStr("service.name", serviceName)
			attrs.PutStr("event.name", "assistant_response")

			event := NewConverter(Options{}).EventFromLog(nil, record)
			if event.Event.Action != "tool.invoked" || event.Event.Category != "tool" {
				t.Fatalf("%s event changed by Claude normalizer: %#v", serviceName, event)
			}
		})
	}
}

type capturedLogFixture struct {
	Runtime string              `json:"runtime"`
	Version string              `json:"version"`
	Records []capturedLogRecord `json:"records"`
}

type capturedLogRecord struct {
	Name       string                 `json:"name"`
	Body       string                 `json:"body"`
	Attributes map[string]interface{} `json:"attributes"`
}

func capturedLogEvents(t *testing.T, name string) (capturedLogFixture, []Event) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read captured fixture: %v", err)
	}
	var fixture capturedLogFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode captured fixture: %v", err)
	}
	logs := plog.NewLogs()
	scopeLogs := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	for _, captured := range fixture.Records {
		record := scopeLogs.LogRecords().AppendEmpty()
		record.Body().SetStr(captured.Body)
		if err := record.Attributes().FromRaw(captured.Attributes); err != nil {
			t.Fatalf("load %s attributes: %v", captured.Name, err)
		}
	}
	return fixture, NewConverter(Options{}).EventsFromLogs(logs)
}

func newObserveSDKTraceSpan(name string) (ptrace.Span, ptrace.Traces) {
	traces := ptrace.NewTraces()
	resourceSpans := traces.ResourceSpans().AppendEmpty()
	resourceAttrs := resourceSpans.Resource().Attributes()
	resourceAttrs.PutStr("beacon.origin", "cloud")
	resourceAttrs.PutStr("beacon.harness.name", "asymptote_observe")
	resourceAttrs.PutStr("service.name", "agent-api")

	scopeSpans := resourceSpans.ScopeSpans().AppendEmpty()
	scopeSpans.Scope().SetName("asymptote-observe")
	span := scopeSpans.Spans().AppendEmpty()
	span.SetName(name)
	span.SetKind(ptrace.SpanKindClient)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0).UTC()))
	return span, traces
}

// Browser-based collectors (agent-beacon-browser-extension) must keep their own
// harness identity and not be coerced into claude_code by the generic "claude"
// rule.
func TestNormalizeHarnessNameBrowserSources(t *testing.T) {
	cases := map[string]string{
		"claude_web":  "claude_web",
		"claude-web":  "claude_web",
		"claude.ai":   "claude_web",
		"chatgpt_web": "chatgpt_web",
		"chatgpt.com": "chatgpt_web",
		// Regression: existing harnesses must be unchanged.
		"claude_code": "claude_code",
		"claude":      "claude_code",
		"codex":       "codex_cli",
		"gemini":      "gemini_cli",
	}
	for in, want := range cases {
		if got := NormalizeHarnessName(in); got != want {
			t.Errorf("NormalizeHarnessName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHarnessNameHonorsBrowserExtensionAttr(t *testing.T) {
	attrs := map[string]interface{}{
		"beacon.harness.name": "claude_web",
		"beacon.origin":       "browser-extension",
		"service.name":        "agent-beacon-browser-collector",
	}
	if got := HarnessName(attrs); got != "claude_web" {
		t.Errorf("HarnessName = %q, want claude_web", got)
	}
}

// Claude Code ships a native PowerShell tool that replaces Bash as the default shell on Windows,
// and it reports the command in the same tool_input.command field. Handling only "bash" left the
// event classified as command.executed with an empty e.command.command -- the leaf every
// rules/risky-command/ rule matches on -- so command threat detection was silently disabled on
// Windows while the telemetry carried the command all along.
//
// The attributes below are copied from a real Windows session captured by the w00-probe scenario,
// rather than invented, so this fails if the runtime's shape changes rather than only if the
// mapping does.
func TestClaudeShellToolsPopulateTheCommandDetectionSurface(t *testing.T) {
	const want = `Write-Output 'BEACON_SANDBOX_2922d672fb2f' | Tee-Object -FilePath 'C:\beacon-sandbox\work\w00.sentinel'`

	for _, toolName := range []string{"PowerShell", "pwsh", "Bash", "bash"} {
		t.Run(toolName, func(t *testing.T) {
			event := &Event{}
			NormalizeClaudeToolResult(event, map[string]interface{}{
				"tool_name":   toolName,
				"tool_input":  `{"command":"` + strings.ReplaceAll(want, `\`, `\\`) + `","description":"write the sentinel"}`,
				"duration_ms": int64(386),
			})

			if event.Event.Action != "command.executed" {
				t.Fatalf("action = %q, want command.executed", event.Event.Action)
			}
			if event.Command == nil || event.Command.Command != want {
				t.Fatalf("command.command = %#v, want %q -- this is the field every "+
					"rules/risky-command/ rule matches on, so an empty value disables them all",
					event.Command, want)
			}
			if event.Tool == nil || event.Tool.Command != want {
				t.Errorf("tool.command = %#v, want the command mirrored", event.Tool)
			}
			if event.Command.DurationMS != 386 {
				t.Errorf("duration_ms = %d, want 386", event.Command.DurationMS)
			}
		})
	}
}

// --- provenance: harness.collection_method and event.fidelity ---

// Everything this exporter emits arrived as OpenTelemetry, so the method is the same on all three
// signals. Asserting it per signal rather than once is what would catch a future construction path
// that bypasses NewEvent.
func TestCollectionMethodIsOTLPOnEverySignal(t *testing.T) {
	logs := plog.NewLogs()
	logRecord := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	logRecord.Body().SetStr("claude_code.assistant_response")
	logRecord.Attributes().PutStr("service.name", "claude-code")
	logRecord.Attributes().PutStr("event.name", "assistant_response")

	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("chat gpt-4")
	span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")

	converter := NewConverter(Options{})
	var events []Event
	events = append(events, converter.EventsFromLogs(logs)...)
	events = append(events, converter.EventsFromTraces(traces)...)
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least one per signal", len(events))
	}
	for _, event := range events {
		if event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodOTLP {
			t.Fatalf("collection_method = %q for action %q, want otlp", event.Harness.CollectionMethod, event.Event.Action)
		}
	}
}

// The catch-all is the whole reason the field exists: a log record whose body matches nothing
// lands on tool.invoked, and until now nothing in the event said that the action was a fallback
// rather than a report.
func TestFidelityMarksCatchAllInferred(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("something entirely unclassifiable")
	record.Attributes().PutStr("service.name", "some-runtime")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "tool.invoked" {
		t.Fatalf("action = %q, want the tool.invoked catch-all", event.Event.Action)
	}
	if event.Event.Fidelity != asymptoteobserve.FidelityInferred {
		t.Fatalf("fidelity = %q, want inferred", event.Event.Fidelity)
	}
}

// A body containing "exec" becomes command.executed, which is the exact case that made the field
// necessary: rules/risky-command/ matches on command.executed, and prose is not a command.
func TestFidelityMarksKeywordMatchInferred(t *testing.T) {
	for body, wantAction := range map[string]string{
		"agent decided to exec something":      "command.executed",
		"wrote the file to disk":               "file.modified",
		"switching approval_mode_switch to on": "approval.requested",
	} {
		record := plog.NewLogRecord()
		record.Body().SetStr(body)
		record.Attributes().PutStr("service.name", "some-runtime")

		event := NewConverter(Options{}).EventFromLog(nil, record)
		if event.Event.Action != wantAction {
			t.Fatalf("body %q gave action %q, want %q", body, event.Event.Action, wantAction)
		}
		if event.Event.Fidelity != asymptoteobserve.FidelityInferred {
			t.Fatalf("body %q gave fidelity %q, want inferred", body, event.Event.Fidelity)
		}
	}
}

// A structured attribute that names the operation is a report, not a guess, and must not be
// tarred with the same marker as the keyword branches -- otherwise a consumer filtering out
// inferred events would throw away the good telemetry along with the bad.
func TestFidelityMarksStructuredAttributesObserved(t *testing.T) {
	for name, attrs := range map[string]map[string]string{
		"mcp tools/call":        {"mcp.method.name": "tools/call"},
		"gen_ai execute_tool":   {"gen_ai.operation.name": "execute_tool"},
		"gen_ai tool call id":   {"gen_ai.tool.call.id": "call_123"},
		"explicit event action": {"beacon.event.action": "file.read"},
	} {
		t.Run(name, func(t *testing.T) {
			record := plog.NewLogRecord()
			record.Body().SetStr("prose that would otherwise be keyword-matched: exec file")
			record.Attributes().PutStr("service.name", "some-runtime")
			for key, value := range attrs {
				record.Attributes().PutStr(key, value)
			}
			event := NewConverter(Options{}).EventFromLog(nil, record)
			if event.Event.Fidelity != asymptoteobserve.FidelityObserved {
				t.Fatalf("fidelity = %q for %s, want observed", event.Event.Fidelity, name)
			}
		})
	}
}

// Codex names its events explicitly. Those names are resolved after construction, overwriting
// whatever the body was guessed to mean, so the fidelity has to be upgraded with the action --
// otherwise Codex's best telemetry would be labeled as a guess.
func TestFidelityUpgradedWhenCodexNormalizerResolvesAction(t *testing.T) {
	record := plog.NewLogRecord()
	// A body that on its own reaches the "prompt" keyword branch and would be marked inferred.
	record.Body().SetStr("codex prompt something")
	record.Attributes().PutStr("service.name", "codex")
	record.Attributes().PutStr("event.name", string(CodexUserPrompt))

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "prompt.submitted" {
		t.Fatalf("action = %q, want prompt.submitted", event.Event.Action)
	}
	if event.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("fidelity = %q, want observed after Codex normalization", event.Event.Fidelity)
	}
}

// A known claude_code.* name is a report; an unknown one falls back to session.activity, which is
// a placeholder. Both arrive as session.activity, so the fidelity is the only thing that tells a
// consumer which of the two it is holding.
func TestFidelitySeparatesKnownFromUnknownClaudeEvents(t *testing.T) {
	known := plog.NewLogRecord()
	known.Body().SetStr("claude_code.assistant_response")
	known.Attributes().PutStr("service.name", "claude-code")
	known.Attributes().PutStr("event.name", "assistant_response")

	unknown := plog.NewLogRecord()
	unknown.Body().SetStr("claude_code.future_lifecycle_event")
	unknown.Attributes().PutStr("service.name", "claude-code")
	unknown.Attributes().PutStr("event.name", "claude_code.future_lifecycle_event")

	converter := NewConverter(Options{})
	knownEvent := converter.EventFromLog(nil, known)
	unknownEvent := converter.EventFromLog(nil, unknown)

	if knownEvent.Event.Action != "session.activity" || unknownEvent.Event.Action != "session.activity" {
		t.Fatalf("actions = %q / %q, want both session.activity so fidelity is the only difference",
			knownEvent.Event.Action, unknownEvent.Event.Action)
	}
	if knownEvent.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("known event fidelity = %q, want observed", knownEvent.Event.Fidelity)
	}
	if unknownEvent.Event.Fidelity != asymptoteobserve.FidelityInferred {
		t.Fatalf("unknown event fidelity = %q, want inferred", unknownEvent.Event.Fidelity)
	}
}

// An explicit beacon.event.action wins over the Claude normalizer, and its fidelity has to travel
// with it. Marking this observed-by-attribute but then letting the normalizer's verdict stick
// would describe an action the event no longer carries.
func TestFidelityRestoredWithPreservedExplicitAction(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.future_lifecycle_event")
	record.Attributes().PutStr("service.name", "claude-code")
	record.Attributes().PutStr("event.name", "claude_code.future_lifecycle_event")
	record.Attributes().PutStr("beacon.event.action", "file.read")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Event.Action != "file.read" {
		t.Fatalf("action = %q, want the explicit file.read", event.Event.Action)
	}
	if event.Event.Fidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("fidelity = %q, want observed for an explicitly stated action", event.Event.Fidelity)
	}
}

func TestCopilotFidelitySplitsNamedEventsFromFallback(t *testing.T) {
	named, namedFidelity := CopilotActionWithFidelity(map[string]interface{}{"event.name": "copilot_chat.tool.call"}, "", "")
	if named != "tool.invoked" || namedFidelity != asymptoteobserve.FidelityObserved {
		t.Fatalf("named copilot event = (%q, %q), want (tool.invoked, observed)", named, namedFidelity)
	}
	fallback, fallbackFidelity := CopilotActionWithFidelity(map[string]interface{}{}, "", "asking for permission")
	if fallback != "approval.requested" || fallbackFidelity != asymptoteobserve.FidelityInferred {
		t.Fatalf("keyword copilot event = (%q, %q), want (approval.requested, inferred)", fallback, fallbackFidelity)
	}
}

// InferAction is still exported and still returns just the action; the wrapper must not drift from
// the function it delegates to.
func TestInferActionMatchesWithFidelityVariant(t *testing.T) {
	for _, fallback := range []string{"", "unclassifiable", "wrote a file", "exec something", "claude_code.assistant_response"} {
		attrs := map[string]interface{}{"service.name": "some-runtime"}
		want, _ := InferActionWithFidelity(attrs, fallback)
		if got := InferAction(attrs, fallback); got != want {
			t.Fatalf("InferAction(%q) = %q, InferActionWithFidelity = %q", fallback, got, want)
		}
	}
}
