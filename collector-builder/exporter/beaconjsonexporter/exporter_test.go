package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
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

func TestNewExporterAcceptsDeprecatedContentRetention(t *testing.T) {
	cfg := &Config{
		Path:             filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "metadata",
	}
	exp, err := newExporter(cfg, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	if exp.cfg.ContentRetention != "metadata" {
		t.Fatalf("ContentRetention = %q, want deprecated value preserved", exp.cfg.ContentRetention)
	}
	record := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.Attributes().PutStr("beacon.event.action", "prompt.submitted")
	record.Attributes().PutStr("gen_ai.prompt", "hello")
	event := exp.eventFromLog(nil, record)
	if event.Content != nil {
		t.Fatalf("deprecated content retention should not emit content marker: %#v", event.Content)
	}
}

func TestDefaultConfigUsesBoundedRotation(t *testing.T) {
	cfg := createDefaultConfig()
	if cfg.RotateBytes != defaultRotateBytes {
		t.Fatalf("RotateBytes = %d, want %d", cfg.RotateBytes, defaultRotateBytes)
	}
	if cfg.RotateArchives != defaultRotateArchives {
		t.Fatalf("RotateArchives = %d, want %d", cfg.RotateArchives, defaultRotateArchives)
	}
}

func TestIntAttrRejectsOutOfRangeString(t *testing.T) {
	attrs := map[string]interface{}{"value": "9223372036854775808"}
	if got, ok := intAttr(attrs, "value"); ok {
		t.Fatalf("intAttr() = %d, true; want false for out-of-range value", got)
	}
}

func TestNewExporterNormalizesRotationDefaults(t *testing.T) {
	cfg := &Config{
		Path:           filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes:  defaultMaxEventBytes,
		RotateBytes:    -1,
		RotateArchives: -1,
		RedactSecrets:  true,
	}
	exp, err := newExporter(cfg, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	if exp.writer.rotateBytes != defaultRotateBytes {
		t.Fatalf("writer rotateBytes = %d, want %d", exp.writer.rotateBytes, defaultRotateBytes)
	}
	if exp.writer.rotateArchives != defaultRotateArchives {
		t.Fatalf("writer rotateArchives = %d, want %d", exp.writer.rotateArchives, defaultRotateArchives)
	}
}

func TestJSONLWriterRotatesAndPrunesArchives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	for i := 0; i <= 3; i++ {
		target := path
		if i > 0 {
			target = path + "." + strconv.Itoa(i)
		}
		if err := os.WriteFile(target, []byte("log-"+strconv.Itoa(i)), 0644); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	writer := jsonlWriter{
		path:           path,
		maxEventBytes:  defaultMaxEventBytes,
		rotateBytes:    1,
		rotateArchives: 2,
		redactSecrets:  true,
	}
	event := newBeaconEvent("tool.invoked", "tool", "info", "test", time.Now())
	event.Message = "new event"

	if err := writer.append(event); err != nil {
		t.Fatalf("append returned error: %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("expected .3 archive to be pruned, err=%v", err)
	}
	if data, err := os.ReadFile(path + ".1"); err != nil || string(data) != "log-0" {
		t.Fatalf(".1 = %q err=%v, want prior active log", string(data), err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "new event") {
		t.Fatalf("current log = %q err=%v, want new event", string(data), err)
	}
}

func TestConsumeLogsWritesBeaconJSONL(t *testing.T) {
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

func TestConsumeLogsMapsCIRunResourceAttributes(t *testing.T) {
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

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	resourceAttrs := rl.Resource().Attributes()
	resourceAttrs.PutStr("service.name", "claude-code")
	resourceAttrs.PutStr("repository", "local/repo")
	resourceAttrs.PutStr("branch", "local-branch")
	resourceAttrs.PutStr(asymptoteobserve.AttributeOrigin, string(asymptoteobserve.OriginCI))
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunProvider, "github_actions")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunID, "123")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunAttempt, "2")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunWorkflow, "CI / build, smoke")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunJob, "telemetry")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunEventName, "pull_request")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunCommit, "deadbeef")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunRepository, "asymptote-labs/agent-beacon")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunBranch, "feature/ci telemetry")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunPR, "refs/pull/12/merge")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunPRNumber, "12")
	resourceAttrs.PutStr(asymptoteobserve.AttributeRunActor, "octocat")
	resourceAttrs.PutBool(asymptoteobserve.AttributeRunEphemeral, true)
	rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Body().SetStr("ci telemetry event")
	rec.Attributes().PutStr("beacon.event.action", "tool.invoked")

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
	if event.Origin != asymptoteobserve.OriginCI {
		t.Fatalf("Origin = %q, want ci", event.Origin)
	}
	if event.Run == nil {
		t.Fatal("Run missing from event")
	}
	if event.Run.Provider != "github_actions" || event.Run.RunID != "123" || event.Run.Workflow != "CI / build, smoke" || !event.Run.Ephemeral {
		t.Fatalf("unexpected run context: %#v", event.Run)
	}
	if event.Run.Repository != "asymptote-labs/agent-beacon" || event.Run.Branch != "feature/ci telemetry" {
		t.Fatalf("run repository/branch = %q/%q", event.Run.Repository, event.Run.Branch)
	}
	if event.Repository != "local/repo" || event.Branch != "local-branch" {
		t.Fatalf("top-level repository/branch = %q/%q", event.Repository, event.Branch)
	}
	if event.Run.PR != "refs/pull/12/merge" || event.Run.PRNumber != "12" || event.Run.Actor != "octocat" {
		t.Fatalf("pull request context missing: %#v", event.Run)
	}
}

func TestConsumeLogsMapsPrompt(t *testing.T) {
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

func TestEventFromLogKeepsTypedPrompt(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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
	if event.Prompt == nil || event.Prompt.Text != "summarize this file" {
		t.Fatalf("legacy metadata retention should not omit prompt: %#v", event.Prompt)
	}
}

func TestEventFromLogDoesNotPromoteEmptyGenAIToolArgumentsToTool(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	rec := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Body().SetStr("chat gpt-4o")
	rec.Attributes().PutStr("service.name", "openai")
	rec.Attributes().PutStr("gen_ai.operation.name", "chat")
	rec.Attributes().PutStr("gen_ai.input.messages", `[{"content":"hello"}]`)
	rec.Attributes().PutEmptyMap("gen_ai.tool.call.arguments")
	rec.Attributes().PutStr("gen_ai.tool.call.id", "{}")
	rec.Attributes().PutStr("gen_ai.request.model", "gpt-4o")

	event := exp.eventFromLog(nil, rec)
	if event.Event.Action != "prompt.submitted" {
		t.Fatalf("action = %q, want prompt.submitted", event.Event.Action)
	}
	if event.Tool != nil {
		t.Fatalf("empty structured tool arguments should not create top-level tool: %#v", event.Tool)
	}
}

func TestConsumeLogsMapsOTelGenAIAttributes(t *testing.T) {
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

	logs := plog.NewLogs()
	rec := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("service.name", "otel")
	rec.Attributes().PutStr("gen_ai.operation.name", "chat")
	rec.Attributes().PutStr("gen_ai.provider.name", "openai")
	rec.Attributes().PutStr("gen_ai.conversation.id", "conv-1")
	rec.Attributes().PutStr("gen_ai.request.model", "gpt-4o")
	rec.Attributes().PutInt("gen_ai.request.max_tokens", 100)
	rec.Attributes().PutDouble("gen_ai.request.temperature", 0.2)
	rec.Attributes().PutStr("gen_ai.response.id", "chatcmpl-1")
	rec.Attributes().PutStr("gen_ai.response.model", "gpt-4o-2026")
	rec.Attributes().PutInt("gen_ai.usage.input_tokens", 12)
	rec.Attributes().PutInt("gen_ai.usage.output_tokens", 34)
	rec.Attributes().PutStr("gen_ai.input.messages", `[{"role":"user","parts":[{"type":"text","content":"summarize token=genai-secret"}]}]`)
	reasons := rec.Attributes().PutEmptySlice("gen_ai.response.finish_reasons")
	reasons.AppendEmpty().SetStr("stop")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if strings.Contains(string(data), "genai-secret") {
		t.Fatalf("GenAI content was not redacted: %s", string(data))
	}
	var event beaconEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.GenAI == nil || event.GenAI.Provider == nil || event.GenAI.Provider.Name != "openai" {
		t.Fatalf("provider missing from GenAI: %#v", event.GenAI)
	}
	if event.GenAI.Request == nil || event.GenAI.Request.Model != "gpt-4o" || event.GenAI.Request.MaxTokens == nil || *event.GenAI.Request.MaxTokens != 100 {
		t.Fatalf("request missing from GenAI: %#v", event.GenAI.Request)
	}
	if event.GenAI.Response == nil || event.GenAI.Response.ID != "chatcmpl-1" || len(event.GenAI.Response.FinishReasons) != 1 || event.GenAI.Response.FinishReasons[0] != "stop" {
		t.Fatalf("response missing from GenAI: %#v", event.GenAI.Response)
	}
	if event.GenAI.Usage == nil || event.GenAI.Usage.InputTokens == nil || *event.GenAI.Usage.InputTokens != 12 || event.GenAI.Usage.OutputTokens == nil || *event.GenAI.Usage.OutputTokens != 34 {
		t.Fatalf("usage missing from GenAI: %#v", event.GenAI.Usage)
	}
	if event.Session == nil || event.Session.ID != "conv-1" {
		t.Fatalf("conversation not promoted to session: %#v", event.Session)
	}
	if event.Model != "gpt-4o" {
		t.Fatalf("model = %q, want request model", event.Model)
	}
	if event.Prompt == nil || !strings.Contains(event.Prompt.Text, "[REDACTED]") {
		t.Fatalf("prompt was not populated and redacted: %#v", event.Prompt)
	}
}

func TestEventFromLogMapsLegacyOpenLLMetryMessages(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	rec := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("gen_ai.operation.name", "chat")
	rec.Attributes().PutStr("gen_ai.prompt.0.content", "hello")
	rec.Attributes().PutStr("gen_ai.completion.0.content", "hi there")
	rec.Attributes().PutStr("llm.usage.prompt_tokens", "3")
	rec.Attributes().PutStr("llm.usage.completion_tokens", "4")

	event := exp.eventFromLog(nil, rec)
	if event.GenAI == nil || event.GenAI.Input == nil || event.GenAI.Output == nil {
		t.Fatalf("legacy messages not mapped: %#v", event.GenAI)
	}
	if text := testFirstTextFromAny(event.GenAI.Input.Messages); text != "hello" {
		t.Fatalf("input message text = %q, want hello", text)
	}
	if text := testFirstTextFromAny(event.GenAI.Output.Messages); text != "hi there" {
		t.Fatalf("output message text = %q, want hi there", text)
	}
	if event.GenAI.Usage == nil || event.GenAI.Usage.InputTokens == nil || *event.GenAI.Usage.InputTokens != 3 || event.GenAI.Usage.OutputTokens == nil || *event.GenAI.Usage.OutputTokens != 4 {
		t.Fatalf("legacy token usage not mapped: %#v", event.GenAI.Usage)
	}
}

func testFirstTextFromAny(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		for _, item := range typed {
			if text := testFirstTextFromAny(item); text != "" {
				return text
			}
		}
	case map[string]interface{}:
		if content, ok := typed["content"]; ok {
			return strings.TrimSpace(fmt.Sprint(content))
		}
		if parts, ok := typed["parts"]; ok {
			return testFirstTextFromAny(parts)
		}
	}
	return ""
}

func TestEventFromLogMapsOTelMCPAttributes(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}
	rec := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Attributes().PutStr("mcp.method.name", "resources/read")
	rec.Attributes().PutStr("mcp.protocol.version", "2025-06-18")
	rec.Attributes().PutStr("mcp.resource.uri", "file:///tmp/report.md")
	rec.Attributes().PutStr("mcp.session.id", "mcp-session-1")
	rec.Attributes().PutStr("mcp.server.name", "filesystem")
	rec.Attributes().PutStr("mcp.tool.name", "read_file")

	event := exp.eventFromLog(nil, rec)
	if event.Event.Action != "file.read" || event.Event.Category != "file" {
		t.Fatalf("MCP resource action/category = %#v, want file.read/file", event.Event)
	}
	if event.File == nil || event.File.Path != "/tmp/report.md" || event.File.Operation != "read" {
		t.Fatalf("MCP resource file mapping missing: %#v", event.File)
	}
	if event.MCP == nil || event.MCP.Method == nil || event.MCP.Method.Name != "resources/read" || event.MCP.Protocol == nil || event.MCP.Protocol.Version != "2025-06-18" {
		t.Fatalf("MCP method/protocol missing: %#v", event.MCP)
	}
	if event.MCP.Resource == nil || event.MCP.Resource.URI != "file:///tmp/report.md" || event.MCP.Session == nil || event.MCP.Session.ID != "mcp-session-1" {
		t.Fatalf("MCP resource/session missing: %#v", event.MCP)
	}
	if event.MCP.Server != "filesystem" || event.MCP.Tool != "read_file" {
		t.Fatalf("Beacon MCP server/tool missing: %#v", event.MCP)
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
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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
		Path:          path,
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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

func TestConsumeMetricsDropsOpenClawMetricsByDefault(t *testing.T) {
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

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "openclaw-gateway")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{
		"gen_ai.client.token.usage",
		"openclaw.context.tokens",
		"openclaw.harness.duration_ms",
		"openclaw.liveness.cpu_core_ratio",
		"openclaw.memory.rss_bytes",
		"openclaw.queue.depth",
		"openclaw.session.recovery.completed",
		"openclaw.session.state",
		"openclaw.telemetry.exporter.events",
		"openclaw.tool.execution.duration_ms",
		"openclaw.tokens",
	} {
		metric := sm.Metrics().AppendEmpty()
		metric.SetName(name)
		metric.SetEmptySum().DataPoints().AppendEmpty().SetIntValue(1)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data = nil
		} else {
			t.Fatalf("read runtime log: %v", err)
		}
	}
	text := strings.TrimSpace(string(data))
	for _, dropped := range []string{
		"gen_ai.client.token.usage",
		"openclaw.context.tokens",
		"openclaw.harness.duration_ms",
		"openclaw.liveness.cpu_core_ratio",
		"openclaw.memory.rss_bytes",
		"openclaw.queue.depth",
		"openclaw.session.recovery.completed",
		"openclaw.session.state",
		"openclaw.telemetry.exporter.events",
		"openclaw.tool.execution.duration_ms",
		"openclaw.tokens",
	} {
		if strings.Contains(text, dropped) {
			t.Fatalf("OpenClaw metric %q should have been dropped, wrote: %s", dropped, text)
		}
	}
	if text != "" {
		t.Fatalf("OpenClaw metrics should have been dropped by default, wrote: %s", text)
	}
}

func TestConsumeMetricsIncludesOpenClawMetricsWhenConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:                  path,
		MaxEventBytes:         defaultMaxEventBytes,
		RotateBytes:           defaultRotateBytes,
		RedactSecrets:         true,
		IncludeRuntimeMetrics: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "openclaw-gateway")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{
		"gen_ai.client.token.usage",
		"openclaw.context.tokens",
		"openclaw.memory.rss_bytes",
		"openclaw.tokens",
	} {
		metric := sm.Metrics().AppendEmpty()
		metric.SetName(name)
		metric.SetEmptySum().DataPoints().AppendEmpty().SetIntValue(1)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	for _, kept := range []string{"gen_ai.client.token.usage", "openclaw.context.tokens", "openclaw.memory.rss_bytes", "openclaw.tokens"} {
		if !strings.Contains(string(data), kept) {
			t.Fatalf("OpenClaw metric %q should have been kept: %s", kept, string(data))
		}
	}
}

func TestConsumeMetricsDropsCodexMetricsByDefault(t *testing.T) {
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

func TestConsumeMetricsDropsCopilotMetricsByDefault(t *testing.T) {
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

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "github-copilot")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{
		"gen_ai.client.operation.duration",
		"gen_ai.client.token.usage",
		"github.copilot.mcp.server.connection.count",
		"github.copilot.agent.turn.count",
		"github.copilot.tool.call.duration",
		"github.copilot.tool.call.count",
		"github.copilot.new.internal.metric",
		"copilot_chat.tool.call.count",
		"copilot_chat.agent.invocation.duration",
	} {
		sm.Metrics().AppendEmpty().SetName(name)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data = nil
		} else {
			t.Fatalf("read runtime log: %v", err)
		}
	}
	text := strings.TrimSpace(string(data))
	for _, dropped := range []string{
		"gen_ai.client.operation.duration",
		"gen_ai.client.token.usage",
		"github.copilot.mcp.server.connection.count",
		"github.copilot.agent.turn.count",
		"github.copilot.tool.call.duration",
		"github.copilot.tool.call.count",
		"github.copilot.new.internal.metric",
		"copilot_chat.tool.call.count",
		"copilot_chat.agent.invocation.duration",
	} {
		if strings.Contains(text, dropped) {
			t.Fatalf("Copilot operational metric %q should have been dropped, wrote: %s", dropped, text)
		}
	}
	if text != "" {
		t.Fatalf("Copilot metrics should have been dropped by default, wrote: %s", text)
	}
}

func TestConsumeMetricsIncludesCopilotOperationalMetricsWhenConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	exp, err := newExporter(&Config{
		Path:                  path,
		MaxEventBytes:         defaultMaxEventBytes,
		RotateBytes:           defaultRotateBytes,
		RedactSecrets:         true,
		IncludeRuntimeMetrics: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "github-copilot")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{
		"gen_ai.client.operation.duration",
		"gen_ai.client.token.usage",
		"github.copilot.tool.call.count",
		"copilot_chat.tool.call.count",
	} {
		sm.Metrics().AppendEmpty().SetName(name)
	}

	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	for _, kept := range []string{"gen_ai.client.operation.duration", "gen_ai.client.token.usage", "github.copilot.tool.call.count", "copilot_chat.tool.call.count"} {
		if !strings.Contains(string(data), kept) {
			t.Fatalf("Copilot operational metric %q should have been kept: %s", kept, string(data))
		}
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

func TestInferActionMapsGenAIChatWithEmptyToolArgumentsToPrompt(t *testing.T) {
	attrs := map[string]interface{}{
		"gen_ai.operation.name":      "chat",
		"gen_ai.input.messages":      []interface{}{map[string]interface{}{"content": "hello"}},
		"gen_ai.tool.call.arguments": map[string]interface{}{},
		"gen_ai.tool.call.id":        "{}",
		"gen_ai.request.model":       "gpt-4o",
		"service.name":               "openai",
	}
	if got := inferAction(attrs, "chat gpt-4o"); got != "prompt.submitted" {
		t.Fatalf("inferAction() = %q, want prompt.submitted", got)
	}
}

func TestInferActionDoesNotMapEmptyGenAIInputMessagesToPrompt(t *testing.T) {
	cases := map[string]interface{}{
		"empty map":          map[string]interface{}{},
		"empty array":        []interface{}{},
		"json empty map":     "{}",
		"json empty array":   "[]",
		"null string":        "null",
		"nil placeholder":    "<nil>",
		"blank content":      []interface{}{map[string]interface{}{"content": "   "}},
		"non-text content":   []interface{}{map[string]interface{}{"content": false}},
		"empty nested parts": []interface{}{map[string]interface{}{"parts": []interface{}{map[string]interface{}{"content": "\t"}}}},
	}

	for name, messages := range cases {
		t.Run(name, func(t *testing.T) {
			attrs := map[string]interface{}{
				"gen_ai.operation.name": "chat",
				"gen_ai.input.messages": messages,
				"gen_ai.request.model":  "gpt-4o",
				"service.name":          "openai",
			}
			if got := inferAction(attrs, "chat gpt-4o"); got == "prompt.submitted" {
				t.Fatalf("inferAction() = %q, want non-prompt action", got)
			}
		})
	}
}

func TestInferActionDoesNotMapEmptyGenAIPromptToPrompt(t *testing.T) {
	for _, prompt := range []interface{}{"{}", "[]", "null", "<nil>", map[string]interface{}{}, []interface{}{}} {
		attrs := map[string]interface{}{
			"gen_ai.operation.name": "chat",
			"gen_ai.prompt":         prompt,
			"gen_ai.request.model":  "gpt-4o",
			"service.name":          "openai",
		}
		if got := inferAction(attrs, "chat gpt-4o"); got == "prompt.submitted" {
			t.Fatalf("inferAction(%#v) = %q, want non-prompt action", prompt, got)
		}
	}
}

func TestInferActionMapsGenAIGenerateContentWithStructuredToolArgumentsToPrompt(t *testing.T) {
	attrs := map[string]interface{}{
		"gen_ai.operation.name":      "generate_content",
		"gen_ai.prompt":              "summarize this file",
		"gen_ai.tool.call.arguments": map[string]interface{}{"path": "README.md"},
		"gen_ai.request.model":       "gemini-2.5-pro",
		"service.name":               "genai-client",
	}
	if got := inferAction(attrs, "generate_content"); got != "prompt.submitted" {
		t.Fatalf("inferAction() = %q, want prompt.submitted", got)
	}
}

func TestInferActionMapsMeaningfulToolCallArgumentsToTool(t *testing.T) {
	attrs := map[string]interface{}{
		"gen_ai.tool.call.arguments": map[string]interface{}{"path": "README.md"},
		"gen_ai.request.model":       "gpt-4o",
		"service.name":               "openai",
	}
	if got := inferAction(attrs, "tool call"); got != "tool.invoked" {
		t.Fatalf("inferAction() = %q, want tool.invoked", got)
	}
}

func TestConsumeTracesDropsCodexUserInputSpan(t *testing.T) {
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
		Path:          path,
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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
			if event.Message != "Codex prompt submitted" {
				t.Fatalf("prompt message = %q, want generic placeholder", event.Message)
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

func TestCodexPromptMessageKeepsTypedPrompt(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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
	if event.Prompt == nil || !strings.Contains(event.Prompt.Text, "do not leak") {
		t.Fatalf("legacy metadata retention should not omit prompt: %#v", event.Prompt)
	}
	if strings.Contains(event.Message, "codex-secret") {
		t.Fatalf("message leaked unredacted prompt secret: %q", event.Message)
	}
}

func TestConsumeLogsMapsGeminiPrompt(t *testing.T) {
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
		Path:          path,
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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

func TestLegacyMetadataRetentionKeepsRawAttributes(t *testing.T) {
	exp, err := newExporter(&Config{
		Path:          filepath.Join(t.TempDir(), "runtime.jsonl"),
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
	}, exporter.Settings{})
	if err != nil {
		t.Fatalf("newExporter returned error: %v", err)
	}

	raw := exp.rawPayload(map[string]interface{}{"prompt": "do a thing"}, map[string]interface{}{"otel_signal": "logs"})
	if attrs, ok := raw["attributes"].(map[string]interface{}); !ok || attrs["prompt"] != "do a thing" {
		t.Fatalf("legacy metadata retention should keep raw attributes: %#v", raw)
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
		Path:          path,
		MaxEventBytes: defaultMaxEventBytes,
		RotateBytes:   defaultRotateBytes,
		RedactSecrets: true,
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
			name:  "copilot service",
			attrs: map[string]interface{}{"service.name": "github-copilot"},
			want:  "copilot_cli",
		},
		{
			name:  "copilot chat wrapper",
			attrs: map[string]interface{}{"service.name": "copilot-chat"},
			want:  "vscode_copilot",
		},
		{
			name:  "openclaw gateway service",
			attrs: map[string]interface{}{"service.name": "openclaw-gateway"},
			want:  "openclaw_gateway",
		},
		{
			name:  "antigravity service",
			attrs: map[string]interface{}{"service.name": "antigravity-cli"},
			want:  "antigravity_cli",
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

func TestCopilotSpanActions(t *testing.T) {
	exp := &beaconExporter{cfg: &Config{}}
	tests := []struct {
		name      string
		spanName  string
		operation string
		attrs     map[string]string
		want      string
		category  string
	}{
		{name: "agent invocation", spanName: "invoke_agent", operation: "invoke_agent", want: "session.activity", category: "session"},
		{name: "user chat", spanName: "chat gpt-4o", operation: "chat", attrs: map[string]string{"github.copilot.initiator": "user", "github.copilot.turn_id": "0"}, want: "prompt.submitted", category: "prompt"},
		{name: "agent chat", spanName: "chat gpt-4o", operation: "chat", attrs: map[string]string{"github.copilot.initiator": "agent", "github.copilot.turn_id": "1"}, want: "session.activity", category: "session"},
		{name: "nonzero chat turn", spanName: "chat gpt-4o", operation: "chat", attrs: map[string]string{"github.copilot.turn_id": "2"}, want: "session.activity", category: "session"},
		{name: "legacy chat", spanName: "chat gpt-4o", operation: "chat", want: "prompt.submitted", category: "prompt"},
		{name: "tool", spanName: "execute_tool readFile", operation: "execute_tool", want: "tool.invoked", category: "tool"},
		{name: "permission", spanName: "permission", operation: "", want: "approval.requested", category: "approval"},
		{name: "hook", spanName: "hook postToolUse", operation: "", want: "tool.invoked", category: "tool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traces := ptrace.NewTraces()
			span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
			span.SetName(tt.spanName)
			if tt.operation != "" {
				span.Attributes().PutStr("gen_ai.operation.name", tt.operation)
			}
			for key, value := range tt.attrs {
				span.Attributes().PutStr(key, value)
			}
			event := exp.eventFromSpan(map[string]interface{}{"service.name": "github-copilot"}, span)
			if event.Harness.Name != "copilot_cli" {
				t.Fatalf("harness = %q, want copilot_cli", event.Harness.Name)
			}
			if event.Event.Action != tt.want {
				t.Fatalf("action = %q, want %q", event.Event.Action, tt.want)
			}
			if event.Event.Category != tt.category {
				t.Fatalf("category = %q, want %q", event.Event.Category, tt.category)
			}
		})
	}
}

func TestVSCodeCopilotDropsNoisySpansByDefault(t *testing.T) {
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

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "copilot-chat")
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	chat := spans.AppendEmpty()
	chat.SetName("chat gpt-4o")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	tool := spans.AppendEmpty()
	tool.SetName("execute_tool readFile")
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.tool.name", "readFile")

	if err := exp.consumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("consumeTraces returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "chat gpt-4o") {
		t.Fatalf("chat span should be dropped by default: %s", text)
	}
	if !strings.Contains(text, "execute_tool readFile") || !strings.Contains(text, "vscode_copilot") {
		t.Fatalf("normalized tool span missing: %s", text)
	}
}

func TestVSCodeCopilotDropsNoisyMetricsByDefault(t *testing.T) {
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
	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "copilot-chat")
	sm := rm.ScopeMetrics().AppendEmpty()
	for _, name := range []string{"gen_ai.client.token.usage", "gen_ai.client.operation.duration", "copilot_chat.time_to_first_token"} {
		sm.Metrics().AppendEmpty().SetName(name)
	}
	if err := exp.consumeMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("consumeMetrics returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime log: %v", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("VS Code Copilot metrics should be dropped by default, wrote: %s", string(data))
	}
}

func TestVSCodeCopilotKeepsActivityLogs(t *testing.T) {
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
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "copilot-chat")
	rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	rec.Body().SetStr("copilot_chat.tool.call")
	rec.Attributes().PutStr("event.name", "copilot_chat.tool.call")
	rec.Attributes().PutStr("gen_ai.tool.name", "runCommand")
	rec.Attributes().PutStr("success", "true")

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "vscode_copilot") || !strings.Contains(text, "tool.invoked") {
		t.Fatalf("activity log not normalized: %s", text)
	}
}

func TestVSCodeCopilotDropsRepeatedSessionStartLogs(t *testing.T) {
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
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "copilot-chat")
	for i := 0; i < 3; i++ {
		rec := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		rec.Body().SetStr("copilot_chat.session.start")
		rec.Attributes().PutStr("event.name", "copilot_chat.session.start")
		rec.Attributes().PutStr("session.id", "same-session")
	}

	if err := exp.consumeLogs(context.Background(), logs); err != nil {
		t.Fatalf("consumeLogs returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime log: %v", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("session start logs should be dropped, wrote: %s", string(data))
	}
}

func TestVSCodeCopilotInvokeAgentUserRequestBecomesPrompt(t *testing.T) {
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

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "copilot-chat")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("invoke_agent GitHub Copilot Chat")
	span.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	span.Attributes().PutStr("gen_ai.agent.name", "GitHub Copilot Chat")
	span.Attributes().PutStr("gen_ai.request.model", "gpt-4.1")
	span.Attributes().PutStr("copilot_chat.user_request", "please read CLAUDE.md for me")
	span.Attributes().PutStr("copilot_chat.session_id", "chat-session")
	span.Attributes().PutStr("session.id", "vscode-window")

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
	if event.Harness.Name != "vscode_copilot" {
		t.Fatalf("harness = %q, want vscode_copilot", event.Harness.Name)
	}
	if event.Event.Action != "prompt.submitted" || event.Event.Category != "prompt" {
		t.Fatalf("event = %#v, want prompt.submitted/prompt", event.Event)
	}
	if event.Prompt == nil || event.Prompt.Text != "please read CLAUDE.md for me" {
		t.Fatalf("prompt = %#v, want user request", event.Prompt)
	}
	if event.Session == nil || event.Session.ID != "chat-session" {
		t.Fatalf("session = %#v, want Copilot chat session", event.Session)
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
