package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestConfigValidateRequiresPath(t *testing.T) {
	cfg := createDefaultConfig()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing path error")
	}
	cfg.Path = filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestDefaultConfigUsesFullRetention(t *testing.T) {
	cfg := createDefaultConfig()
	if cfg.ContentRetention != "full" {
		t.Fatalf("ContentRetention = %q, want full", cfg.ContentRetention)
	}
}

func TestNewExporterDefaultsEmptyRetentionToFull(t *testing.T) {
	cfg := &Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}
	exp, err := newExporter(cfg, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	if exp.cfg.ContentRetention != "full" {
		t.Fatalf("ContentRetention = %q, want full", exp.cfg.ContentRetention)
	}
}

func TestConsumeLogsWritesBeaconJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "redacted",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "claude-cowork")
	sl := rl.ScopeLogs().AppendEmpty()
	rec := sl.LogRecords().AppendEmpty()
	rec.Body().SetStr("tool call token=super-secret")
	rec.Attributes().PutStr("beacon.event.action", "tool.invoked")
	rec.Attributes().PutStr("session.id", "session-1")
	rec.Attributes().PutStr("tool.name", "Shell")
	rec.Attributes().PutStr("command", "kubectl get pods")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Fatalf("expected secret to be redacted: %s", string(data))
	}
	var event beaconEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Vendor != vendor || event.Event.Action != "tool.invoked" || event.Harness.Name != "claude_cowork" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.Event.Category != "tool" {
		t.Fatalf("event category = %q, want tool", event.Event.Category)
	}
	if event.Session == nil || event.Session.ID != "session-1" {
		t.Fatalf("session missing from event: %#v", event.Session)
	}
	if event.Command == nil || event.Command.Command != "kubectl get pods" {
		t.Fatalf("command missing from event: %#v", event.Command)
	}
}

func TestConsumeLogsMapsPromptForFullRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "full",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	rec := sl.LogRecords().AppendEmpty()
	rec.Body().SetStr("prompt submitted")
	rec.Attributes().PutStr("beacon.event.action", "prompt.submitted")
	rec.Attributes().PutStr("gen_ai.prompt", "summarize token=prompt-secret")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	var event beaconEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Prompt == nil || event.Prompt.Text != "summarize token=[REDACTED]" {
		t.Fatalf("prompt = %#v, want redacted typed prompt", event.Prompt)
	}
}

func TestMetadataRetentionOmitsTypedPrompt(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:             filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	attrs := map[string]interface{}{
		"beacon.event.action": "prompt.submitted",
		"gen_ai.prompt":       "summarize this file",
	}
	logs := plog.NewLogs()
	record := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	for key, value := range attrs {
		record.Attributes().PutStr(key, value.(string))
	}

	event := exp.eventFromLog(nil, record)
	if event.Prompt != nil {
		t.Fatalf("metadata retention should omit prompt: %#v", event.Prompt)
	}
}

func TestEventCategoryInfersFromAction(t *testing.T) {
	tests := map[string]string{
		"tool.invoked":       "tool",
		"file.modified":      "file",
		"command.executed":   "command",
		"mcp.tool_invoked":   "mcp",
		"approval.requested": "approval",
		"policy.blocked":     "approval",
		"prompt.submitted":   "prompt",
		"metric.observed":    "metric",
	}
	for action, want := range tests {
		if got := eventCategory(action, ""); got != want {
			t.Fatalf("eventCategory(%q) = %q, want %q", action, got, want)
		}
	}
	if got := eventCategory("tool.invoked", "custom"); got != "custom" {
		t.Fatalf("explicit category = %q, want custom", got)
	}
}

func TestEventFromMetricDefaultsToObservedAction(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:             filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("codex.run.token_usage")

	event := exp.eventFromMetric(map[string]interface{}{"service.name": "codex-cli"}, metric)
	if event.Event.Action != "metric.observed" {
		t.Fatalf("metric action = %q, want metric.observed", event.Event.Action)
	}
	if event.Event.Category != "metric" {
		t.Fatalf("metric category = %q, want metric", event.Event.Category)
	}
	if event.Harness.Name != "codex_cli" {
		t.Fatalf("harness = %q, want codex_cli", event.Harness.Name)
	}
	if event.Raw["metric_name"] != "codex.run.token_usage" {
		t.Fatalf("metric raw payload = %#v", event.Raw)
	}
}

func TestConsumeMetricsDropsRuntimeMetricsByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "full",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "unknown_service:node")
	rm.Resource().Attributes().PutStr("process.command", "/tmp/mcp-server-elasticsearch")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{
		"process.cpu.utilization",
		"process.memory.usage",
		"nodejs.eventloop.utilization",
		"v8js.memory.heap.used",
	} {
		sm.Metrics().AppendEmpty().SetName(name)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		t.Fatalf("runtime metrics should have been dropped, wrote: %s", string(data))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime log: %v", err)
	}
}

func TestConsumeMetricsIncludesRuntimeMetricsWhenConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:                  path,
		MaxEventBytes:         defaultMaxEventBytes,
		RotateBytes:           defaultRotateBytes,
		RedactSecrets:         true,
		ContentRetention:      "full",
		IncludeRuntimeMetrics: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("nodejs.eventloop.utilization")

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if !strings.Contains(string(data), "nodejs.eventloop.utilization") {
		t.Fatalf("runtime metric was not written: %s", string(data))
	}
}

func TestConsumeMetricsKeepsAgentMetricsByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName("codex.tool.call.duration_ms")

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if !strings.Contains(string(data), "codex.tool.call.duration_ms") {
		t.Fatalf("agent metric was not written: %s", string(data))
	}
}

func TestInferActionMapsCodexUserInputToPrompt(t *testing.T) {
	attrs := map[string]interface{}{"service.name": "codex_cli_rs"}
	if got := inferAction(attrs, "op.dispatch.user_input_with_turn_context"); got != "prompt.submitted" {
		t.Fatalf("inferAction() = %q, want prompt.submitted", got)
	}
}

func TestInferActionMapsCodexOpAttributeToPrompt(t *testing.T) {
	attrs := map[string]interface{}{
		"service.name": "codex_cli_rs",
		"codex.op":     "user_input_with_turn_context",
	}
	if got := inferAction(attrs, "op.dispatch"); got != "prompt.submitted" {
		t.Fatalf("inferAction() = %q, want prompt.submitted", got)
	}
}

func TestEventFromSpanMapsCodexUserInputToPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "full",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	span := testSpan("op.dispatch.user_input_with_turn_context")
	span.Attributes().PutStr("prompt", "summarize token=codex-secret")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex-cli")
	span.CopyTo(rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty())

	if err := exp.consumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("consumeTraces returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	var event beaconEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Event.Action != "prompt.submitted" {
		t.Fatalf("span action = %q, want prompt.submitted", event.Event.Action)
	}
	if event.Event.Category != "prompt" {
		t.Fatalf("span category = %q, want prompt", event.Event.Category)
	}
	if event.Harness.Name != "codex_cli" {
		t.Fatalf("harness = %q, want codex_cli", event.Harness.Name)
	}
	if event.Prompt == nil || event.Prompt.Text != "summarize token=[REDACTED]" {
		t.Fatalf("prompt = %#v, want redacted Codex prompt", event.Prompt)
	}
}

func TestMetadataRetentionDropsRawAttributes(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:             filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	raw := exp.rawPayload(map[string]interface{}{"prompt": "do a thing"}, map[string]interface{}{"otel_signal": "logs"})
	if _, ok := raw["attributes"]; ok {
		t.Fatalf("metadata retention should not include raw attributes: %#v", raw)
	}
	if raw["attribute_count"] != 1 {
		t.Fatalf("attribute count missing: %#v", raw)
	}
}

func TestCodexInternalSpanFilter(t *testing.T) {
	codexAttrs := map[string]interface{}{"service.name": "codex-cli"}

	if !shouldDropSpan(codexAttrs, testSpan("FramedRead::poll_next")) {
		t.Fatal("expected Codex transport span to be dropped")
	}
	if shouldDropSpan(codexAttrs, testSpan("session_task.turn")) {
		t.Fatal("expected meaningful Codex session span to be kept")
	}
	if shouldDropSpan(map[string]interface{}{"service.name": "other-agent"}, testSpan("FramedRead::poll_next")) {
		t.Fatal("expected non-Codex span to be kept")
	}
}

func TestConsumeTracesDropsCodexInternalSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:             path,
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex-cli")
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	spans.AppendEmpty().SetName("FramedRead::poll_next")
	spans.AppendEmpty().SetName("session_task.turn")

	if err := exp.consumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("consumeTraces returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "FramedRead::poll_next") {
		t.Fatalf("transport span was written: %s", text)
	}
	if !strings.Contains(text, "session_task.turn") {
		t.Fatalf("meaningful span was not written: %s", text)
	}
}

func testSpan(name string) ptrace.Span {
	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName(name)
	return span
}

func TestHarnessNameSeparatesClaudeCodeAndCowork(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]interface{}
		hints []string
		want  string
	}{
		{
			name:  "claude code log body",
			attrs: map[string]interface{}{"service.name": "claude"},
			hints: []string{"claude_code.api_request"},
			want:  "claude_code",
		},
		{
			name:  "claude code metric",
			attrs: map[string]interface{}{"service.name": "claude-code"},
			hints: []string{"claude_code.token.usage"},
			want:  "claude_code",
		},
		{
			name:  "claude cowork service",
			attrs: map[string]interface{}{"service.name": "claude-cowork"},
			want:  "claude_cowork",
		},
		{
			name:  "codex service",
			attrs: map[string]interface{}{"service.name": "codex-cli"},
			want:  "codex_cli",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := harnessName(tt.attrs, tt.hints...); got != tt.want {
				t.Fatalf("harnessName() = %q, want %q", got, tt.want)
			}
		})
	}
}
