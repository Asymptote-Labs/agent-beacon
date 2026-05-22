package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type beaconExporter struct {
	cfg    *Config
	writer jsonlWriter
	logger *zap.Logger
}

const (
	codexConversationStarts = "codex.conversation_starts"
	codexUserPrompt         = "codex.user_prompt"
	codexToolDecision       = "codex.tool_decision"
	codexToolResult         = "codex.tool_result"
)

var allowedCodexLogEvents = map[string]struct{}{
	codexConversationStarts: {},
	codexUserPrompt:         {},
	codexToolDecision:       {},
	codexToolResult:         {},
}

var noisyCodexLogMessages = []string{
	"runtime metrics reset skipped",
	"flushing otel metrics",
}

func newExporter(raw component.Config, set exporter.Settings) (*beaconExporter, error) {
	cfg, ok := raw.(*Config)
	if !ok {
		return nil, fmt.Errorf("unexpected config type %T", raw)
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = defaultMaxEventBytes
	}
	if cfg.RotateBytes == 0 {
		cfg.RotateBytes = defaultRotateBytes
	}
	if cfg.ContentRetention == "" {
		cfg.ContentRetention = "full"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &beaconExporter{
		cfg: cfg,
		writer: jsonlWriter{
			path:          cfg.Path,
			maxEventBytes: cfg.MaxEventBytes,
			rotateBytes:   cfg.RotateBytes,
			redactSecrets: cfg.RedactSecrets,
		},
		logger: set.Logger,
	}, nil
}

func (e *beaconExporter) consumeLogs(ctx context.Context, logs plog.Logs) error {
	_ = ctx
	var firstErr error
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLogs := logs.ResourceLogs().At(i)
		resourceAttrs := attrsToMap(resourceLogs.Resource().Attributes())
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			scopeLogs := resourceLogs.ScopeLogs().At(j)
			for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
				record := scopeLogs.LogRecords().At(k)
				if shouldDropLog(resourceAttrs, record) {
					continue
				}
				event := e.eventFromLog(resourceAttrs, record)
				if err := e.writer.append(event); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (e *beaconExporter) consumeTraces(ctx context.Context, traces ptrace.Traces) error {
	_ = ctx
	var firstErr error
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		resourceSpans := traces.ResourceSpans().At(i)
		resourceAttrs := attrsToMap(resourceSpans.Resource().Attributes())
		for j := 0; j < resourceSpans.ScopeSpans().Len(); j++ {
			scopeSpans := resourceSpans.ScopeSpans().At(j)
			for k := 0; k < scopeSpans.Spans().Len(); k++ {
				span := scopeSpans.Spans().At(k)
				if e.shouldDropSpan(resourceAttrs, span) {
					continue
				}
				event := e.eventFromSpan(resourceAttrs, span)
				if err := e.writer.append(event); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (e *beaconExporter) consumeMetrics(ctx context.Context, metrics pmetric.Metrics) error {
	_ = ctx
	var firstErr error
	for i := 0; i < metrics.ResourceMetrics().Len(); i++ {
		resourceMetrics := metrics.ResourceMetrics().At(i)
		resourceAttrs := attrsToMap(resourceMetrics.Resource().Attributes())
		for j := 0; j < resourceMetrics.ScopeMetrics().Len(); j++ {
			scopeMetrics := resourceMetrics.ScopeMetrics().At(j)
			for k := 0; k < scopeMetrics.Metrics().Len(); k++ {
				metric := scopeMetrics.Metrics().At(k)
				if shouldDropMetric(resourceAttrs, metric.Name(), e.cfg.IncludeRuntimeMetrics, e.cfg.IncludeOpenClawRuntimeMetrics) {
					continue
				}
				event := e.eventFromMetric(resourceAttrs, metric)
				if err := e.writer.append(event); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func shouldDropLog(resourceAttrs map[string]interface{}, record plog.LogRecord) bool {
	attrs := mergeMaps(resourceAttrs, attrsToMap(record.Attributes()))
	if harnessName(attrs, record.Body().AsString()) != "codex_cli" {
		return false
	}
	return isNoisyCodexLog(attrs, record.Body().AsString())
}

func isNoisyCodexLog(attrs map[string]interface{}, body string) bool {
	eventName := codexLogEventName(attrs)
	if eventName == "" {
		message := strings.ToLower(firstNonEmpty(body, firstString(attrs, "message", "log.message")))
		for _, noisy := range noisyCodexLogMessages {
			if strings.Contains(message, noisy) {
				return true
			}
		}
		return false
	}
	if _, ok := allowedCodexLogEvents[eventName]; ok {
		return false
	}
	// Codex adds new observability events over time. Keep endpoint activity quiet
	// unless a Codex log event is explicitly mapped into Beacon's stable schema.
	return strings.HasPrefix(eventName, "codex.")
}

func shouldDropMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics, includeOpenClawRuntimeMetrics bool) bool {
	if shouldDropCodexMetric(resourceAttrs, name) {
		return true
	}
	if shouldDropOpenClawMetric(resourceAttrs, name, includeOpenClawRuntimeMetrics) {
		return true
	}
	if !includeRuntimeMetrics && shouldDropRuntimeMetric(name) {
		return true
	}
	return false
}

func shouldDropRuntimeMetric(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	dropPrefixes := []string{
		"process.",
		"nodejs.",
		"runtime.nodejs.",
		"v8js.",
	}
	for _, prefix := range dropPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func shouldDropCodexMetric(resourceAttrs map[string]interface{}, name string) bool {
	if harnessName(resourceAttrs, name) != "codex_cli" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(normalized, "codex.")
}

func shouldDropOpenClawMetric(resourceAttrs map[string]interface{}, name string, includeOpenClawRuntimeMetrics bool) bool {
	if includeOpenClawRuntimeMetrics {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	if harnessName(resourceAttrs, name) != "openclaw_gateway" {
		return false
	}
	if slices.Contains(openClawKeptMetricNames, normalized) {
		return false
	}
	if slices.Contains(openClawDroppedMetricNames, normalized) {
		return true
	}
	for _, prefix := range openClawDroppedMetricPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

var openClawKeptMetricNames = []string{
	"gen_ai.client.token.usage",
	"openclaw.context.tokens",
	"openclaw.tokens",
}

var openClawDroppedMetricNames = []string{
	"openclaw.harness.duration_ms",
	"openclaw.run.duration_ms",
	"openclaw.session.state",
	"openclaw.tool.execution.duration_ms",
}

var openClawDroppedMetricPrefixes = []string{
	"openclaw.liveness.",
	"openclaw.memory.",
	"openclaw.queue.",
	"openclaw.session.recovery.",
	"openclaw.telemetry.exporter.",
}

func (e *beaconExporter) eventFromLog(resourceAttrs map[string]interface{}, record plog.LogRecord) beaconEvent {
	attrs := mergeMaps(resourceAttrs, attrsToMap(record.Attributes()))
	ts := timestamp(record.Timestamp().AsTime())
	action := firstString(attrs, "beacon.event.action", "event.action", "gen_ai.agent.action", "ai.agent.action")
	if action == "" {
		action = inferAction(attrs, record.Body().AsString())
	}
	message := firstNonEmpty(record.Body().AsString(), firstString(attrs, "message", "log.message", "event.name"))
	event := newBeaconEvent(action, eventCategory(action, firstString(attrs, "beacon.event.category", "event.category", "category")), severity(record.SeverityText(), record.SeverityNumber().String()), harnessName(attrs, message), ts)
	event.Message = message
	e.populateCommon(&event, attrs)
	event.Raw = e.rawPayload(attrs, map[string]interface{}{
		"otel_signal": "logs",
		"severity":    record.SeverityText(),
	})
	e.normalizeCodexLogEvent(&event, attrs)
	return event
}

func (e *beaconExporter) eventFromSpan(resourceAttrs map[string]interface{}, span ptrace.Span) beaconEvent {
	attrs := mergeMaps(resourceAttrs, attrsToMap(span.Attributes()))
	action := firstString(attrs, "beacon.event.action", "event.action", "gen_ai.agent.action", "ai.agent.action")
	if action == "" {
		action = inferAction(attrs, span.Name())
	}
	message := firstNonEmpty(firstString(attrs, "message", "gen_ai.prompt", "gen_ai.response"), span.Name())
	event := newBeaconEvent(action, eventCategory(action, firstString(attrs, "beacon.event.category", "event.category", "tool")), spanSeverity(span.Status().Code().String()), harnessName(attrs, message, span.Name()), timestamp(span.StartTimestamp().AsTime()))
	event.Message = message
	e.populateCommon(&event, attrs)
	event.Raw = e.rawPayload(attrs, map[string]interface{}{
		"otel_signal": "traces",
		"span_name":   span.Name(),
		"span_kind":   span.Kind().String(),
		"status":      span.Status().Code().String(),
	})
	return event
}

func (e *beaconExporter) shouldDropSpan(resourceAttrs map[string]interface{}, span ptrace.Span) bool {
	attrs := mergeMaps(resourceAttrs, attrsToMap(span.Attributes()))
	if harnessName(attrs, span.Name()) != "codex_cli" {
		return false
	}
	// Codex spans are high-volume internals that duplicate the semantic Codex log
	// events Beacon uses for session, prompt, approval, and tool activity. Keep
	// them disabled by default, but allow opt-in troubleshooting capture.
	return !e.cfg.IncludeCodexSpans
}

func (e *beaconExporter) normalizeCodexLogEvent(event *beaconEvent, attrs map[string]interface{}) {
	if event == nil || event.Harness.Name != "codex_cli" {
		return
	}
	switch codexLogEventName(attrs) {
	case codexConversationStarts:
		event.Event.Action = "session.started"
		event.Event.Category = "session"
		event.Message = "Codex session started"
	case codexUserPrompt:
		event.Event.Action = "prompt.submitted"
		event.Event.Category = "prompt"
		event.Message = "Codex prompt submitted"
	case codexToolDecision:
		decision := firstString(attrs, "decision")
		if strings.EqualFold(decision, "denied") || strings.EqualFold(decision, "deny") {
			event.Event.Action = "approval.denied"
		} else {
			event.Event.Action = "approval.requested"
		}
		event.Event.Category = "approval"
		event.Message = "Codex tool decision"
		if event.Approval == nil {
			event.Approval = &approvalInfo{}
		}
		event.Approval.Required = true
		event.Approval.Decision = decision
		event.Approval.Reason = firstString(attrs, "source", "approval_mode", "active_approval_mode")
	case codexToolResult:
		normalizeCodexToolResult(event, attrs)
	}
}

func codexLogEventName(attrs map[string]interface{}) string {
	return strings.ToLower(firstString(attrs, "event.name"))
}

func normalizeCodexToolResult(event *beaconEvent, attrs map[string]interface{}) {
	toolName := firstString(attrs, "tool.name", "tool_name", "function_name", "tool", "mcp_server")
	args := firstString(attrs, "arguments", "function_args", "tool.command", "command")
	event.Event.Action = "tool.invoked"
	event.Event.Category = "tool"
	if event.Tool == nil {
		event.Tool = &toolInfo{}
	}
	event.Tool.Name = toolName
	event.Tool.Command = args
	if command := codexShellCommand(toolName, args); command != "" {
		event.Event.Action = "command.executed"
		event.Event.Category = "command"
		event.Command = &commandInfo{Command: command}
	}
	event.Message = firstNonEmpty(toolName, "Codex tool result")
}

func codexShellCommand(toolName, args string) string {
	if strings.EqualFold(toolName, "shell") {
		if cmd := codexArgumentCommand(args); cmd != "" {
			return cmd
		}
		return args
	}
	return codexArgumentCommand(args)
}

func codexArgumentCommand(args string) string {
	var payload struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Cmd)
}

func (e *beaconExporter) eventFromMetric(resourceAttrs map[string]interface{}, metric pmetric.Metric) beaconEvent {
	attrs := mergeMaps(resourceAttrs, map[string]interface{}{})
	action := firstString(attrs, "beacon.event.action", "event.action")
	if action == "" {
		action = "metric.observed"
	}
	event := newBeaconEvent(action, "metric", "info", harnessName(attrs, metric.Name()), time.Now().UTC())
	event.Message = metric.Name()
	e.populateCommon(&event, attrs)
	event.Raw = e.rawPayload(attrs, map[string]interface{}{
		"otel_signal":        "metrics",
		"metric_name":        metric.Name(),
		"metric_description": metric.Description(),
		"metric_unit":        metric.Unit(),
	})
	return event
}

func (e *beaconExporter) populateCommon(event *beaconEvent, attrs map[string]interface{}) {
	event.Model = firstString(attrs, "gen_ai.request.model", "gen_ai.response.model", "model", "ai.model")
	event.Repository = firstString(attrs, "vcs.repository.url", "repository", "repo.path", "workspace.repository")
	event.Branch = firstString(attrs, "vcs.branch.name", "git.branch", "branch")
	if id := firstString(attrs, "session.id", "conversation.id", "conversation_id", "gen_ai.conversation.id"); id != "" || firstString(attrs, "cwd", "working_directory", "workspace") != "" {
		event.Session = &sessionInfo{
			ID:               id,
			WorkingDirectory: firstString(attrs, "cwd", "working_directory", "process.command_args.cwd", "workspace"),
		}
	}
	if name := firstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name", "function_name", "tool_name"); name != "" || firstString(attrs, "tool.command", "command", "function_args") != "" {
		event.Tool = &toolInfo{
			Name:    name,
			Command: firstString(attrs, "tool.command", "command", "process.command_line", "function_args"),
			Path:    firstString(attrs, "tool.path", "file.path", "file_path"),
		}
	}
	if path := firstString(attrs, "file.path", "file_path", "code.filepath"); path != "" {
		event.File = &fileInfo{
			Path:      path,
			Operation: firstString(attrs, "file.operation", "operation"),
			Language:  firstString(attrs, "code.language", "language"),
		}
	}
	if command := firstString(attrs, "command", "process.command_line", "shell.command"); command != "" {
		event.Command = &commandInfo{Command: command}
		if exitCode, ok := intAttr(attrs, "exit_code", "process.exit_code", "command.exit_code"); ok {
			event.Command.ExitCode = &exitCode
		}
		if duration, ok := int64Attr(attrs, "duration_ms", "command.duration_ms"); ok {
			event.Command.DurationMS = duration
		}
	}
	if server := firstString(attrs, "mcp.server.name", "mcp.server", "gen_ai.mcp.server", "mcp_server_name"); server != "" || firstString(attrs, "mcp.tool.name") != "" || firstString(attrs, "tool_type") == "mcp" {
		event.MCP = &mcpInfo{
			Server: server,
			Tool:   firstString(attrs, "mcp.tool.name", "tool.name", "function_name"),
		}
	}
	if decision := firstString(attrs, "approval.decision", "policy.decision", "decision"); decision != "" {
		event.Approval = &approvalInfo{
			Required: true,
			Decision: decision,
			Reason:   firstString(attrs, "approval.reason", "policy.reason", "approval_mode", "active_approval_mode"),
		}
	}
	if e.cfg.ContentRetention != "metadata" && event.Event.Category == "prompt" {
		if text := firstString(attrs, "gen_ai.prompt", "prompt", "user_prompt", "input.prompt"); text != "" {
			event.Prompt = &promptInfo{Text: text}
		}
	}
	event.Content = &contentInfo{Retention: e.cfg.ContentRetention, Included: e.cfg.ContentRetention != "metadata", Redacted: e.cfg.ContentRetention == "redacted"}
}

func (e *beaconExporter) rawPayload(attrs map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	for k, v := range extra {
		raw[k] = v
	}
	if e.cfg.ContentRetention == "metadata" {
		raw["attribute_count"] = len(attrs)
		return raw
	}
	raw["attributes"] = attrs
	return raw
}

func attrsToMap(attrs pcommon.Map) map[string]interface{} {
	out := make(map[string]interface{}, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		out[k] = v.AsRaw()
		return true
	})
	return out
}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func firstString(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if str := strings.TrimSpace(fmt.Sprint(value)); str != "" && str != "<nil>" {
				return str
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func intAttr(attrs map[string]interface{}, keys ...string) (int, bool) {
	value, ok := int64Attr(attrs, keys...)
	return int(value), ok
}

func int64Attr(attrs map[string]interface{}, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case int:
			return int64(typed), true
		case int64:
			return typed, true
		case float64:
			return int64(typed), true
		}
	}
	return 0, false
}

func timestamp(ts time.Time) time.Time {
	if ts.IsZero() || ts.UnixNano() == 0 {
		return time.Now().UTC()
	}
	return ts
}

func severity(text, number string) string {
	lower := strings.ToLower(text + " " + number)
	switch {
	case strings.Contains(lower, "fatal") || strings.Contains(lower, "critical"):
		return "critical"
	case strings.Contains(lower, "error"):
		return "high"
	case strings.Contains(lower, "warn"):
		return "medium"
	default:
		return "info"
	}
}

func spanSeverity(status string) string {
	if strings.Contains(strings.ToLower(status), "error") {
		return "high"
	}
	return "info"
}

func harnessName(attrs map[string]interface{}, hints ...string) string {
	name := firstString(attrs, "beacon.harness.name", "harness.name", "service.name", "telemetry.sdk.name")
	if explicit := firstString(attrs, "beacon.harness.name", "harness.name"); explicit != "" {
		return normalizeHarnessName(explicit)
	}
	candidates := append([]string{name}, hints...)
	for _, candidate := range candidates {
		if normalized := normalizeHarnessName(candidate); normalized != "" {
			return normalized
		}
	}
	if name != "" {
		return name
	}
	return "otel"
}

func normalizeHarnessName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "cowork") || strings.Contains(lower, "co-work"):
		return "claude_cowork"
	case strings.Contains(lower, "claude_code") || strings.Contains(lower, "claude-code") || strings.Contains(lower, "claude code") || strings.HasPrefix(lower, "claude_code."):
		return "claude_code"
	case lower == "claude" || strings.Contains(lower, "claude"):
		return "claude_code"
	case strings.Contains(lower, "openclaw") || strings.Contains(lower, "open-claw"):
		return "openclaw_gateway"
	case strings.Contains(lower, "codex"):
		return "codex_cli"
	case strings.Contains(lower, "gemini"):
		return "gemini_cli"
	case name != "":
		return name
	default:
		return ""
	}
}

func inferAction(attrs map[string]interface{}, fallback string) string {
	tool := strings.ToLower(firstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name", "function_name", "tool_name"))
	text := strings.ToLower(strings.Join([]string{
		fallback,
		tool,
		firstString(attrs, "event.name", "codex.op", "rpc.method"),
	}, " "))
	switch {
	case strings.Contains(text, "gemini_cli.user_prompt"):
		return "prompt.submitted"
	case strings.Contains(text, "gemini_cli.tool_call"):
		return geminiToolAction(attrs)
	case strings.Contains(text, "gemini_cli.file_operation"):
		return geminiFileAction(attrs)
	case strings.Contains(text, "approval_mode_switch") || strings.Contains(text, "approval_mode_duration") || strings.Contains(text, "plan_execution"):
		return "approval.requested"
	case strings.Contains(text, "prompt") || strings.Contains(text, "user_input"):
		return "prompt.submitted"
	case strings.Contains(text, "mcp"):
		return "mcp.tool_invoked"
	case strings.Contains(text, "command") || strings.Contains(text, "shell") || strings.Contains(text, "exec"):
		return "command.executed"
	case strings.Contains(text, "file") || strings.Contains(text, "write") || strings.Contains(text, "edit"):
		return "file.modified"
	case strings.Contains(text, "approval"):
		return "approval.requested"
	default:
		return "tool.invoked"
	}
}

func geminiToolAction(attrs map[string]interface{}) string {
	if firstString(attrs, "tool_type") == "mcp" || firstString(attrs, "mcp_server_name") != "" {
		return "mcp.tool_invoked"
	}
	return "tool.invoked"
}

func geminiFileAction(attrs map[string]interface{}) string {
	switch strings.ToLower(firstString(attrs, "operation")) {
	case "read":
		return "file.read"
	case "create":
		return "file.created"
	default:
		return "file.modified"
	}
}

func eventCategory(action, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch {
	case strings.HasPrefix(action, "prompt."):
		return "prompt"
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	case strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return "approval"
	case strings.HasPrefix(action, "metric."):
		return "metric"
	case strings.HasPrefix(action, "tool."):
		return "tool"
	default:
		return ""
	}
}
