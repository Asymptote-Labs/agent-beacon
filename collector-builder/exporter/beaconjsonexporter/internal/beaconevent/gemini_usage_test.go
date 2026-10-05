package beaconevent

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// geminiAPIResponse is one Gemini usageMetadata as Gemini CLI's ApiResponseEvent copies it onto
// the gemini_cli.api_response log record: input_token_count is promptTokenCount,
// output_token_count candidatesTokenCount, cached_content_token_count cachedContentTokenCount,
// thoughts_token_count thoughtsTokenCount, tool_token_count toolUsePromptTokenCount, and
// total_token_count totalTokenCount.
type geminiAPIResponse struct {
	input, output, cached, thoughts, tool, total int64
}

// appendGeminiAPIResponseLogs appends the two log records Gemini CLI's logApiResponse emits for
// one model response: the native gemini_cli.api_response record and the OTel GenAI
// gen_ai.client.inference.operation.details record that restates its input and output counts.
func appendGeminiAPIResponseLogs(records plog.LogRecordSlice, sessionID, model string, ts time.Time, usage geminiAPIResponse) {
	common := func(rec plog.LogRecord) {
		rec.SetTimestamp(pcommon.NewTimestampFromTime(ts))
		attrs := rec.Attributes()
		attrs.PutStr("session.id", sessionID)
		attrs.PutStr("installation.id", "6f1e2b4c-0d3a-4e5f-9a8b-7c6d5e4f3a2b")
		attrs.PutBool("interactive", true)
		attrs.PutStr("auth_type", "oauth-personal")
		attrs.PutStr("event.timestamp", ts.Format(time.RFC3339Nano))
	}

	native := records.AppendEmpty()
	common(native)
	native.Body().SetStr("API response from " + model + ". Status: 200. Duration: 2311ms.")
	attrs := native.Attributes()
	attrs.PutStr("event.name", "gemini_cli.api_response")
	attrs.PutStr("model", model)
	attrs.PutInt("duration_ms", 2311)
	attrs.PutInt("input_token_count", usage.input)
	attrs.PutInt("output_token_count", usage.output)
	attrs.PutInt("cached_content_token_count", usage.cached)
	attrs.PutInt("thoughts_token_count", usage.thoughts)
	attrs.PutInt("tool_token_count", usage.tool)
	attrs.PutInt("total_token_count", usage.total)
	attrs.PutStr("prompt_id", sessionID+"########1")
	attrs.PutInt("status_code", 200)
	attrs.PutInt("http.status_code", 200)
	attrs.PutStr("role", "main")
	attrs.PutEmptySlice("finish_reasons").AppendEmpty().SetStr("STOP")

	semantic := records.AppendEmpty()
	common(semantic)
	semantic.Body().SetStr("GenAI operation details from " + model + ". Status: 200. Duration: 2311ms.")
	attrs = semantic.Attributes()
	attrs.PutStr("event.name", "gen_ai.client.inference.operation.details")
	attrs.PutStr("gen_ai.response.id", "resp-"+sessionID)
	attrs.PutEmptySlice("gen_ai.response.finish_reasons").AppendEmpty().SetStr("stop")
	attrs.PutStr("gen_ai.operation.name", "generate_content")
	attrs.PutStr("gen_ai.provider.name", "gcp.vertex_ai")
	attrs.PutStr("gen_ai.request.model", model)
	attrs.PutStr("gen_ai.response.model", model)
	attrs.PutInt("gen_ai.usage.input_tokens", usage.input)
	attrs.PutInt("gen_ai.usage.output_tokens", usage.output)
}

func newGeminiLogs() (plog.Logs, plog.LogRecordSlice) {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "gemini-cli")
	rl.Resource().Attributes().PutStr("service.version", "0.9.0")
	return logs, rl.ScopeLogs().AppendEmpty().LogRecords()
}

func int64Value(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

// Gemini's promptTokenCount already contains cachedContentTokenCount, its candidatesTokenCount
// leaves out thoughtsTokenCount, and toolUsePromptTokenCount is input that promptTokenCount does
// not hold (totalTokenCount is prompt + candidates + tool-use prompt + thoughts). Beacon's
// disjoint usage therefore records input = prompt - cached + tool, output = candidates +
// thoughts with thoughts as its reasoning breakdown, and cached as cache read, so that
// input + output + cache_read reproduces Gemini's own total.
func TestEventsFromLogsMapsGeminiAPIResponseUsage(t *testing.T) {
	tests := []struct {
		name                                string
		usage                               geminiAPIResponse
		input, output, cacheRead, reasoning int64
	}{
		{
			name:  "thinking model with a cached prefix",
			usage: geminiAPIResponse{input: 12000, output: 350, cached: 8000, thoughts: 420, tool: 0, total: 12770},
			input: 4000, output: 770, cacheRead: 8000, reasoning: 420,
		},
		{
			name:  "tool-use prompt tokens are input outside promptTokenCount",
			usage: geminiAPIResponse{input: 5000, output: 100, cached: 0, thoughts: 0, tool: 600, total: 5700},
			input: 5600, output: 100, cacheRead: 0, reasoning: 0,
		},
		{
			// Gemini CLI writes 0 for every count a response's usageMetadata omitted.
			name:  "response without usage metadata",
			usage: geminiAPIResponse{},
			input: 0, output: 0, cacheRead: 0, reasoning: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs, records := newGeminiLogs()
			appendGeminiAPIResponseLogs(records, "gemini-session-1", "gemini-2.5-pro", time.Unix(1760000000, 0).UTC(), tt.usage)

			events := NewConverter(Options{}).EventsFromLogs(logs)
			if len(events) != 2 {
				t.Fatalf("expected 2 events, got %d", len(events))
			}
			native, semantic := events[0], events[1]
			if native.Harness.Name != "gemini_cli" || native.Session == nil || native.Session.ID != "gemini-session-1" {
				t.Fatalf("native record harness/session = %q/%#v, want gemini_cli/gemini-session-1", native.Harness.Name, native.Session)
			}
			if native.GenAI == nil || native.GenAI.Usage == nil {
				t.Fatalf("api_response record carries no usage: %#v", native.GenAI)
			}
			usage := native.GenAI.Usage
			var cacheRead, reasoning *int64
			if usage.CacheRead != nil {
				cacheRead = usage.CacheRead.InputTokens
			}
			if usage.Reasoning != nil {
				reasoning = usage.Reasoning.OutputTokens
			}
			if int64Value(usage.InputTokens) != tt.input || int64Value(usage.OutputTokens) != tt.output ||
				int64Value(cacheRead) != tt.cacheRead || int64Value(reasoning) != tt.reasoning {
				t.Fatalf("usage = input %d output %d cache_read %d reasoning %d, want %d/%d/%d/%d",
					int64Value(usage.InputTokens), int64Value(usage.OutputTokens), int64Value(cacheRead), int64Value(reasoning),
					tt.input, tt.output, tt.cacheRead, tt.reasoning)
			}
			if usage.CacheCreation != nil {
				t.Fatalf("cache_creation = %#v, want none: Gemini reports no cache writes", usage.CacheCreation)
			}
			if got := tt.input + tt.output + tt.cacheRead; got != tt.usage.total {
				t.Fatalf("input + output + cache_read = %d, want Gemini's total_token_count %d", got, tt.usage.total)
			}
			if native.Model != "gemini-2.5-pro" {
				t.Fatalf("model = %q, want gemini-2.5-pro", native.Model)
			}
			// The semconv restatement of the same response must not count it a second time.
			if semantic.GenAI != nil && semantic.GenAI.Usage != nil {
				t.Fatalf("operation.details record kept usage %#v; it restates the api_response counts", semantic.GenAI.Usage)
			}
		})
	}
}

// The operation-details record is only a duplicate on Gemini CLI, which always emits it beside
// api_response. The same event name from any other runtime is that runtime's only usage record.
func TestEventsFromLogsKeepsOperationDetailsUsageOutsideGemini(t *testing.T) {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "agent-api")
	rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("event.name", "gen_ai.client.inference.operation.details")
	rec.Attributes().PutStr("gen_ai.operation.name", "chat")
	rec.Attributes().PutInt("gen_ai.usage.input_tokens", 120)
	rec.Attributes().PutInt("gen_ai.usage.output_tokens", 30)

	events := NewConverter(Options{}).EventsFromLogs(logs)
	if len(events) != 1 || events[0].GenAI == nil || events[0].GenAI.Usage == nil {
		t.Fatalf("events = %#v, want one event with usage", events)
	}
	if got := int64Value(events[0].GenAI.Usage.InputTokens); got != 120 {
		t.Fatalf("input_tokens = %d, want 120", got)
	}
}

// A non-Gemini record that happens to use the same attribute names is not reinterpreted.
func TestEventsFromLogsIgnoresGeminiCountNamesOutsideGemini(t *testing.T) {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "agent-api")
	rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("event.name", "gemini_cli.api_response")
	rec.Attributes().PutInt("input_token_count", 100)

	events := NewConverter(Options{}).EventsFromLogs(logs)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].GenAI != nil && events[0].GenAI.Usage != nil {
		t.Fatalf("usage = %#v, want none for a non-Gemini harness", events[0].GenAI.Usage)
	}
}

type geminiTokenPoint struct {
	session, model, tokenType string
	value                     int64
}

// newGeminiTokenUsageMetric builds gemini_cli.token.usage the way Gemini CLI's
// recordCustomTokenUsageMetrics records it: one monotonic cumulative counter whose series are
// split by the common attributes (session.id, installation.id, interactive, auth_type) plus model
// and a type of input, output, thought, cache or tool. Every series of one collection carries the
// collection timestamp.
func newGeminiTokenUsageMetric(ts time.Time, points []geminiTokenPoint) pmetric.Metrics {
	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "gemini-cli")
	rm.Resource().Attributes().PutStr("service.version", "0.9.0")
	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("gemini_cli.token.usage")
	metric.SetDescription("Counts the total number of tokens used.")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	sum.SetIsMonotonic(true)
	for _, p := range points {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetStartTimestamp(pcommon.NewTimestampFromTime(ts.Add(-time.Minute)))
		dp.SetTimestamp(pcommon.NewTimestampFromTime(ts))
		dp.SetIntValue(p.value)
		dp.Attributes().PutStr("session.id", p.session)
		dp.Attributes().PutStr("installation.id", "6f1e2b4c-0d3a-4e5f-9a8b-7c6d5e4f3a2b")
		dp.Attributes().PutBool("interactive", true)
		dp.Attributes().PutStr("auth_type", "oauth-personal")
		dp.Attributes().PutStr("model", p.model)
		dp.Attributes().PutStr("type", p.tokenType)
	}
	return metrics
}

type geminiMetricUsage struct {
	input, output, cacheRead, reasoning int64
	unmapped                            []string
}

func sumGeminiMetricUsage(t *testing.T, events []Event) map[string]*geminiMetricUsage {
	t.Helper()
	out := map[string]*geminiMetricUsage{}
	for _, event := range events {
		if event.Event.Action != "token.usage" || event.Harness.Name != "gemini_cli" {
			t.Fatalf("event = %#v harness %q, want gemini_cli token.usage", event.Event, event.Harness.Name)
		}
		key := event.Session.ID + "/" + event.Model
		got := out[key]
		if got == nil {
			got = &geminiMetricUsage{}
			out[key] = got
		}
		usage := event.GenAI.Usage
		mapped := false
		if usage.InputTokens != nil {
			got.input += *usage.InputTokens
			mapped = true
		}
		if usage.OutputTokens != nil {
			got.output += *usage.OutputTokens
			mapped = true
		}
		if usage.CacheRead != nil && usage.CacheRead.InputTokens != nil {
			got.cacheRead += *usage.CacheRead.InputTokens
			mapped = true
		}
		if usage.Reasoning != nil && usage.Reasoning.OutputTokens != nil {
			got.reasoning += *usage.Reasoning.OutputTokens
			mapped = true
		}
		if !mapped {
			got.unmapped = append(got.unmapped, event.GenAI.Token.Type)
		}
	}
	return out
}

// The metric carries the same five Gemini counts as the api_response log, one per series, so it
// gets the same reading: the input series loses its cached subset and gains the tool-use prompt,
// the output series gains thoughts, thought is the reasoning breakdown and cache is cache read.
// Siblings are matched on the full attribute set, so two models and two sessions in one export
// never borrow each other's counts.
func TestEventsFromMetricsMapsGeminiTokenTypes(t *testing.T) {
	ts := time.Unix(1760000060, 0).UTC()
	metrics := newGeminiTokenUsageMetric(ts, []geminiTokenPoint{
		{"gemini-session-1", "gemini-2.5-pro", "input", 17000},
		{"gemini-session-1", "gemini-2.5-pro", "output", 450},
		{"gemini-session-1", "gemini-2.5-pro", "thought", 420},
		{"gemini-session-1", "gemini-2.5-pro", "cache", 8000},
		{"gemini-session-1", "gemini-2.5-pro", "tool", 600},
		{"gemini-session-1", "gemini-2.5-flash", "input", 900},
		{"gemini-session-1", "gemini-2.5-flash", "output", 40},
		{"gemini-session-1", "gemini-2.5-flash", "thought", 0},
		{"gemini-session-1", "gemini-2.5-flash", "cache", 0},
		{"gemini-session-1", "gemini-2.5-flash", "tool", 0},
		{"gemini-session-2", "gemini-2.5-pro", "input", 3000},
		{"gemini-session-2", "gemini-2.5-pro", "output", 10},
		{"gemini-session-2", "gemini-2.5-pro", "thought", 5},
		{"gemini-session-2", "gemini-2.5-pro", "cache", 2048},
		{"gemini-session-2", "gemini-2.5-pro", "tool", 0},
	})

	events := NewConverter(Options{}).EventsFromMetrics(metrics)
	if len(events) != 15 {
		t.Fatalf("expected one event per datapoint (15), got %d", len(events))
	}
	got := sumGeminiMetricUsage(t, events)
	want := map[string]geminiMetricUsage{
		"gemini-session-1/gemini-2.5-pro":   {input: 17000 - 8000 + 600, output: 450 + 420, cacheRead: 8000, reasoning: 420},
		"gemini-session-1/gemini-2.5-flash": {input: 900, output: 40},
		"gemini-session-2/gemini-2.5-pro":   {input: 3000 - 2048, output: 10 + 5, cacheRead: 2048, reasoning: 5},
	}
	for key, w := range want {
		g := got[key]
		if g == nil {
			t.Fatalf("no events for %s", key)
		}
		if g.input != w.input || g.output != w.output || g.cacheRead != w.cacheRead || g.reasoning != w.reasoning {
			t.Fatalf("%s usage = input %d output %d cache_read %d reasoning %d, want %d/%d/%d/%d",
				key, g.input, g.output, g.cacheRead, g.reasoning, w.input, w.output, w.cacheRead, w.reasoning)
		}
		// tool is folded into input, so its own datapoint must not add to any total.
		if len(g.unmapped) != 1 || g.unmapped[0] != "tool" {
			t.Fatalf("%s unmapped token types = %v, want only tool", key, g.unmapped)
		}
	}
	for _, event := range events {
		if event.Raw["metric_value"] == nil {
			t.Fatalf("raw metric_value missing: %#v", event.Raw)
		}
	}
}

// Cumulative series reach the exporter as running totals, so the input series' adjusted value
// must itself be a running total: the cached and tool series of the same collection are running
// totals too, and differencing two collections of (input - cache + tool) gives exactly the
// interval's uncached input.
func TestEventsFromMetricsGeminiCumulativeCollectionsStayConsistent(t *testing.T) {
	first := newGeminiTokenUsageMetric(time.Unix(1760000060, 0).UTC(), []geminiTokenPoint{
		{"s", "gemini-2.5-pro", "input", 12000},
		{"s", "gemini-2.5-pro", "cache", 8000},
		{"s", "gemini-2.5-pro", "tool", 0},
		{"s", "gemini-2.5-pro", "output", 350},
		{"s", "gemini-2.5-pro", "thought", 420},
	})
	second := newGeminiTokenUsageMetric(time.Unix(1760000120, 0).UTC(), []geminiTokenPoint{
		{"s", "gemini-2.5-pro", "input", 17000},
		{"s", "gemini-2.5-pro", "cache", 8000},
		{"s", "gemini-2.5-pro", "tool", 600},
		{"s", "gemini-2.5-pro", "output", 450},
		{"s", "gemini-2.5-pro", "thought", 420},
	})
	converter := NewConverter(Options{})
	a := sumGeminiMetricUsage(t, converter.EventsFromMetrics(first))["s/gemini-2.5-pro"]
	b := sumGeminiMetricUsage(t, converter.EventsFromMetrics(second))["s/gemini-2.5-pro"]
	if a.input != 4000 || b.input != 9600 {
		t.Fatalf("cumulative uncached input = %d then %d, want 4000 then 9600", a.input, b.input)
	}
	if delta := b.input - a.input; delta != 5000-0+600 {
		t.Fatalf("interval uncached input = %d, want 5600 (5000 new prompt, no new cache, 600 tool)", delta)
	}
	if a.output != 770 || b.output != 870 {
		t.Fatalf("cumulative output = %d then %d, want 770 then 870", a.output, b.output)
	}
}

// Other harnesses' token metrics are not touched by the Gemini reading.
func TestEventsFromMetricsGeminiAdjustmentIsScopedToGeminiMetric(t *testing.T) {
	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "agent-api")
	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("gen_ai.client.token.usage")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	for tokenType, value := range map[string]int64{"input": 100, "output": 20, "cache": 60} {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetIntValue(value)
		dp.Attributes().PutStr("gen_ai.token.type", tokenType)
	}
	for _, event := range NewConverter(Options{}).EventsFromMetrics(metrics) {
		usage := event.GenAI.Usage
		if usage.InputTokens != nil && *usage.InputTokens != 100 {
			t.Fatalf("input_tokens = %d, want 100 as reported", *usage.InputTokens)
		}
		if usage.OutputTokens != nil && *usage.OutputTokens != 20 {
			t.Fatalf("output_tokens = %d, want 20 as reported", *usage.OutputTokens)
		}
	}
}
