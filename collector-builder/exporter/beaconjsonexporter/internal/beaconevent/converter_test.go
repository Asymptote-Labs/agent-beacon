package beaconevent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
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
