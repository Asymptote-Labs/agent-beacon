package beaconevent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
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
