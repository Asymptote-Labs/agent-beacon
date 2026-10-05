package beaconjsonexporter

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// One Gemini CLI response, exported the way Gemini CLI's logApiResponse exports it: the native
// gemini_cli.api_response record, its gen_ai.client.inference.operation.details restatement, and
// the gemini_cli.token.usage counter series it records for the same usageMetadata
// (promptTokenCount 12000 of which cachedContentTokenCount 8000, candidatesTokenCount 350,
// thoughtsTokenCount 420, toolUsePromptTokenCount 0, totalTokenCount 12770).
//
// Every line the exporter writes must carry Beacon's disjoint reading, with exactly one log line
// holding the response's usage. The metric lines carry the same usage again under a metric_name,
// which is what the token report's log-over-metric dedupe keys on.
func TestConsumeGeminiAPIResponseWritesDisjointUsageOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:          path,
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	ts := time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)
	common := func(attrs pcommon.Map) {
		attrs.PutStr("session.id", "8c4f0d2e-5b1a-4e7c-9f3d-2a6b8e1c7d40")
		attrs.PutStr("installation.id", "6f1e2b4c-0d3a-4e5f-9a8b-7c6d5e4f3a2b")
		attrs.PutBool("interactive", true)
		attrs.PutStr("auth_type", "oauth-personal")
	}

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "gemini-cli")
	rl.Resource().Attributes().PutStr("service.version", "0.9.0")
	records := rl.ScopeLogs().AppendEmpty().LogRecords()
	native := records.AppendEmpty()
	native.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	native.Body().SetStr("API response from gemini-2.5-pro. Status: 200. Duration: 2311ms.")
	common(native.Attributes())
	native.Attributes().PutStr("event.name", "gemini_cli.api_response")
	native.Attributes().PutStr("event.timestamp", ts.Format(time.RFC3339Nano))
	native.Attributes().PutStr("model", "gemini-2.5-pro")
	native.Attributes().PutInt("duration_ms", 2311)
	native.Attributes().PutInt("input_token_count", 12000)
	native.Attributes().PutInt("output_token_count", 350)
	native.Attributes().PutInt("cached_content_token_count", 8000)
	native.Attributes().PutInt("thoughts_token_count", 420)
	native.Attributes().PutInt("tool_token_count", 0)
	native.Attributes().PutInt("total_token_count", 12770)
	native.Attributes().PutStr("prompt_id", "8c4f0d2e########1")
	native.Attributes().PutInt("status_code", 200)
	semantic := records.AppendEmpty()
	semantic.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	semantic.Body().SetStr("GenAI operation details from gemini-2.5-pro. Status: 200. Duration: 2311ms.")
	common(semantic.Attributes())
	semantic.Attributes().PutStr("event.name", "gen_ai.client.inference.operation.details")
	semantic.Attributes().PutStr("gen_ai.operation.name", "generate_content")
	semantic.Attributes().PutStr("gen_ai.provider.name", "gcp.vertex_ai")
	semantic.Attributes().PutStr("gen_ai.request.model", "gemini-2.5-pro")
	semantic.Attributes().PutStr("gen_ai.response.model", "gemini-2.5-pro")
	semantic.Attributes().PutInt("gen_ai.usage.input_tokens", 12000)
	semantic.Attributes().PutInt("gen_ai.usage.output_tokens", 350)

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "gemini-cli")
	rm.Resource().Attributes().PutStr("service.version", "0.9.0")
	metric := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("gemini_cli.token.usage")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	sum.SetIsMonotonic(true)
	for _, point := range []struct {
		tokenType string
		value     int64
	}{{"input", 12000}, {"output", 350}, {"cache", 8000}, {"thought", 420}, {"tool", 0}} {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetTimestamp(pcommon.NewTimestampFromTime(ts.Add(time.Minute)))
		dp.SetIntValue(point.value)
		common(dp.Attributes())
		dp.Attributes().PutStr("model", "gemini-2.5-pro")
		dp.Attributes().PutStr("type", point.tokenType)
	}

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open runtime log: %v", err)
	}
	defer file.Close()
	type totals struct{ input, output, cacheRead, reasoning, lines int64 }
	var logTotals, metricTotals totals
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var event beaconEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		if event.Harness.Name != "gemini_cli" || event.Session == nil || event.Session.ID != "8c4f0d2e-5b1a-4e7c-9f3d-2a6b8e1c7d40" {
			t.Fatalf("line harness/session = %q/%#v, want gemini_cli and the Gemini session id on every channel", event.Harness.Name, event.Session)
		}
		if event.GenAI == nil || event.GenAI.Usage == nil {
			continue
		}
		target := &logTotals
		if name, _ := event.Raw["metric_name"].(string); name != "" {
			target = &metricTotals
		}
		u := event.GenAI.Usage
		if u.InputTokens != nil {
			target.input += *u.InputTokens
		}
		if u.OutputTokens != nil {
			target.output += *u.OutputTokens
		}
		if u.CacheRead != nil && u.CacheRead.InputTokens != nil {
			target.cacheRead += *u.CacheRead.InputTokens
		}
		if u.Reasoning != nil && u.Reasoning.OutputTokens != nil {
			target.reasoning += *u.Reasoning.OutputTokens
		}
		if u.CacheCreation != nil {
			t.Fatalf("cache_creation = %#v, want none: Gemini reports no cache writes", u.CacheCreation)
		}
		target.lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan runtime log: %v", err)
	}
	want := totals{input: 4000, output: 770, cacheRead: 8000, reasoning: 420}
	gotLog := logTotals
	gotLog.lines = 0
	if gotLog != want || logTotals.lines != 1 {
		t.Fatalf("log channel = %+v over %d lines, want %+v on exactly one line", gotLog, logTotals.lines, want)
	}
	if logTotals.input+logTotals.output+logTotals.cacheRead != 12770 {
		t.Fatalf("log channel total = %d, want Gemini's total_token_count 12770", logTotals.input+logTotals.output+logTotals.cacheRead)
	}
	gotMetric := metricTotals
	gotMetric.lines = 0
	if gotMetric != want {
		t.Fatalf("metric channel = %+v, want the same disjoint reading %+v", gotMetric, want)
	}
}
