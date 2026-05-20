package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestConsumeMetricsDropsCodexMetricsByDefault(t *testing.T) {
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
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "codex-cli")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{
		"codex.turn.memory",
		"codex.turn.token_usage",
		"codex.websocket.event.duration_ms",
		"codex.remote_models.load_cache.duration_ms",
		"codex.tool.call.duration_ms",
	} {
		sm.Metrics().AppendEmpty().SetName(name)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		t.Fatalf("codex metrics should have been dropped, wrote: %s", string(data))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime log: %v", err)
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

func TestConsumeTracesDropsCodexUserInputSpan(t *testing.T) {
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
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		t.Fatalf("Codex user input span should have been dropped, wrote: %s", string(data))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime log: %v", err)
	}
}

func TestConsumeTracesIncludesCodexSpansWhenConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:              path,
		MaxEventBytes:     defaultMaxEventBytes,
		RotateBytes:       defaultRotateBytes,
		RedactSecrets:     true,
		ContentRetention:  "full",
		IncludeCodexSpans: true,
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
	if event.Raw["otel_signal"] != "traces" || event.Raw["span_name"] != "op.dispatch.user_input_with_turn_context" {
		t.Fatalf("trace raw payload missing: %#v", event.Raw)
	}
}

func TestConsumeLogsMapsCodexSemanticEventsAndDropsTransportNoise(t *testing.T) {
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
	rl.Resource().Attributes().PutStr("service.name", "codex_cli_rs")
	sl := rl.ScopeLogs().AppendEmpty()
	for _, eventName := range []string{
		"codex.conversation_starts",
		"codex.user_prompt",
		"codex.tool_decision",
		"codex.tool_result",
		"codex.startup_phase",
		"codex.turn_ttft",
		"codex.websocket_event",
		"codex.sse_event",
	} {
		rec := sl.LogRecords().AppendEmpty()
		rec.Attributes().PutStr("event.name", eventName)
		rec.Attributes().PutStr("conversation.id", "codex-session")
		rec.Attributes().PutStr("model", "gpt-5.5")
		switch eventName {
		case "codex.user_prompt":
			rec.Body().SetStr("op.dispatch.user_input_with_turn_context")
			rec.Attributes().PutStr("prompt", "look up weather token=codex-secret")
		case "codex.tool_decision":
			rec.Attributes().PutStr("decision", "approved")
			rec.Attributes().PutStr("source", "Config")
		case "codex.tool_result":
			rec.Attributes().PutStr("tool", "shell")
			rec.Attributes().PutStr("arguments", `{"cmd":"date"}`)
		}
	}
	debug := sl.LogRecords().AppendEmpty()
	debug.Body().SetStr("runtime metrics reset skipped: runtime metrics snapshot reader is not enabled")
	flush := sl.LogRecords().AppendEmpty()
	flush.Body().SetStr("flushing OTEL metrics")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	text := string(data)
	for _, noisy := range []string{"codex.startup_phase", "codex.turn_ttft", "codex.websocket_event", "codex.sse_event", "runtime metrics reset skipped", "flushing OTEL metrics"} {
		if strings.Contains(text, noisy) {
			t.Fatalf("noisy Codex event %q was written: %s", noisy, text)
		}
	}

	var actions []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var event beaconEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		actions = append(actions, event.Event.Action)
		if event.Event.Action == "prompt.submitted" {
			if event.Prompt == nil || event.Prompt.Text != "look up weather token=[REDACTED]" {
				t.Fatalf("prompt = %#v, want redacted typed prompt", event.Prompt)
			}
			if event.Message != "look up weather token=[REDACTED]" {
				t.Fatalf("prompt message = %q, want prompt text", event.Message)
			}
		}
	}
	wantActions := []string{"session.started", "prompt.submitted", "approval.requested", "command.executed"}
	for _, want := range wantActions {
		if !containsString(actions, want) {
			t.Fatalf("actions = %#v, want %q", actions, want)
		}
	}
}

func TestCodexToolResultExtractsShellCommand(t *testing.T) {
	event := newBeaconEvent("tool.invoked", "tool", "info", "codex_cli", time.Now())
	normalizeCodexToolResult(&event, map[string]interface{}{
		"tool":      "shell",
		"arguments": `{"cmd":"date","workdir":"/tmp"}`,
	})

	if event.Event.Action != "command.executed" || event.Event.Category != "command" {
		t.Fatalf("unexpected action/category: %#v", event.Event)
	}
	if event.Command == nil || event.Command.Command != "date" {
		t.Fatalf("command = %#v, want parsed date command", event.Command)
	}
	if event.Tool == nil || event.Tool.Command != `{"cmd":"date","workdir":"/tmp"}` {
		t.Fatalf("tool payload not preserved: %#v", event.Tool)
	}
}

func TestMetadataRetentionOmitsCodexPromptMessage(t *testing.T) {
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
	rec := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Body().SetStr("op.dispatch.user_input_with_turn_context")
	rec.Attributes().PutStr("service.name", "codex_cli_rs")
	rec.Attributes().PutStr("event.name", codexUserPrompt)
	rec.Attributes().PutStr("prompt", "do not leak token=codex-secret")

	event := exp.eventFromLog(nil, rec)
	if event.Prompt != nil {
		t.Fatalf("metadata retention should omit prompt: %#v", event.Prompt)
	}
	if strings.Contains(event.Message, "do not leak") || strings.Contains(event.Message, "codex-secret") {
		t.Fatalf("metadata retention leaked prompt in message: %q", event.Message)
	}
	if event.Message != "Codex prompt submitted" {
		t.Fatalf("message = %q, want metadata-safe placeholder", event.Message)
	}
}

func TestConsumeLogsMapsGeminiPrompt(t *testing.T) {
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
	rl.Resource().Attributes().PutStr("service.name", "gemini-cli")
	rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("event.name", "gemini_cli.user_prompt")
	rec.Attributes().PutStr("session.id", "gemini-session")
	rec.Attributes().PutStr("prompt", "summarize token=gemini-secret")
	rec.Attributes().PutStr("prompt_id", "prompt-1")

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
	if event.Harness.Name != "gemini_cli" {
		t.Fatalf("harness = %q, want gemini_cli", event.Harness.Name)
	}
	if event.Event.Action != "prompt.submitted" || event.Event.Category != "prompt" {
		t.Fatalf("unexpected event action/category: %#v", event.Event)
	}
	if event.Session == nil || event.Session.ID != "gemini-session" {
		t.Fatalf("session missing from event: %#v", event.Session)
	}
	if event.Prompt == nil || event.Prompt.Text != "summarize token=[REDACTED]" {
		t.Fatalf("prompt = %#v, want redacted Gemini prompt", event.Prompt)
	}
}

func TestConsumeLogsMapsGeminiMCPTool(t *testing.T) {
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

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "gemini")
	rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("event.name", "gemini_cli.tool_call")
	rec.Attributes().PutStr("function_name", "search_docs")
	rec.Attributes().PutStr("function_args", `{"query":"otel"}`)
	rec.Attributes().PutStr("tool_type", "mcp")
	rec.Attributes().PutStr("mcp_server_name", "docs")
	rec.Attributes().PutStr("decision", "accept")
	rec.Attributes().PutInt("duration_ms", 42)

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
	if event.Event.Action != "mcp.tool_invoked" || event.Event.Category != "mcp" {
		t.Fatalf("unexpected event action/category: %#v", event.Event)
	}
	if event.Tool == nil || event.Tool.Name != "search_docs" || event.Tool.Command != `{"query":"otel"}` {
		t.Fatalf("tool mapping missing: %#v", event.Tool)
	}
	if event.MCP == nil || event.MCP.Server != "docs" || event.MCP.Tool != "search_docs" {
		t.Fatalf("mcp mapping missing: %#v", event.MCP)
	}
	if event.Approval == nil || event.Approval.Decision != "accept" {
		t.Fatalf("approval mapping missing: %#v", event.Approval)
	}
}

func TestConsumeLogsMapsGeminiFileOperation(t *testing.T) {
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

	logs := plog.NewLogs()
	rec := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("service.name", "gemini-cli")
	rec.Attributes().PutStr("event.name", "gemini_cli.file_operation")
	rec.Attributes().PutStr("file_path", "/tmp/main.go")
	rec.Attributes().PutStr("operation", "create")

	event := exp.eventFromLog(nil, rec)
	if event.Event.Action != "file.created" || event.Event.Category != "file" {
		t.Fatalf("unexpected event action/category: %#v", event.Event)
	}
	if event.File == nil || event.File.Path != "/tmp/main.go" || event.File.Operation != "create" {
		t.Fatalf("file mapping missing: %#v", event.File)
	}
}

func TestInferActionMapsGeminiApprovalEvents(t *testing.T) {
	for _, name := range []string{"approval_mode_switch", "approval_mode_duration", "plan_execution"} {
		if got := inferAction(map[string]interface{}{"service.name": "gemini-cli"}, name); got != "approval.requested" {
			t.Fatalf("inferAction(%q) = %q, want approval.requested", name, got)
		}
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
	exp := &beaconExporter{cfg: &Config{}}

	if !exp.shouldDropSpan(codexAttrs, testSpan("FramedRead::poll_next")) {
		t.Fatal("expected Codex transport span to be dropped")
	}
	if !exp.shouldDropSpan(codexAttrs, testSpan("session_task.turn")) {
		t.Fatal("expected Codex session_task span to be dropped")
	}
	if !exp.shouldDropSpan(codexAttrs, testSpan("op.dispatch.user_input_with_turn_context")) {
		t.Fatal("expected Codex user input span to be dropped")
	}
	if exp.shouldDropSpan(map[string]interface{}{"service.name": "other-agent"}, testSpan("FramedRead::poll_next")) {
		t.Fatal("expected non-Codex span to be kept")
	}
	debugExp := &beaconExporter{cfg: &Config{IncludeCodexSpans: true}}
	if debugExp.shouldDropSpan(codexAttrs, testSpan("session_task.turn")) {
		t.Fatal("expected Codex span to be kept when configured")
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
	spans.AppendEmpty().SetName("run_sampling_request")
	spans.AppendEmpty().SetName("handle_responses")
	spans.AppendEmpty().SetName("op.dispatch.user_input_with_turn_context")

	if err := exp.consumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("consumeTraces returned error: %v", err)
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		t.Fatalf("Codex spans should have been dropped, wrote: %s", string(data))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime log: %v", err)
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
		{
			name:  "openclaw gateway service",
			attrs: map[string]interface{}{"service.name": "openclaw-gateway"},
			want:  "openclaw_gateway",
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
