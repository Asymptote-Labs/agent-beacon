package beaconevent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	CodexConversationStarts = "codex.conversation_starts"
	CodexUserPrompt         = "codex.user_prompt"
	CodexToolDecision       = "codex.tool_decision"
	CodexToolResult         = "codex.tool_result"
)

var allowedCodexLogEvents = map[string]struct{}{
	CodexConversationStarts: {},
	CodexUserPrompt:         {},
	CodexToolDecision:       {},
	CodexToolResult:         {},
}

var noisyCodexLogMessages = []string{
	"runtime metrics reset skipped",
	"flushing otel metrics",
}

type Options struct {
	ContentRetention      string
	IncludeRuntimeMetrics bool
	IncludeCodexSpans     bool
}

type Converter struct {
	opts Options
}

func NewConverter(opts Options) Converter {
	if opts.ContentRetention == "" {
		opts.ContentRetention = "full"
	}
	return Converter{opts: opts}
}

func (c Converter) EventsFromLogs(logs plog.Logs) []Event {
	var events []Event
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLogs := logs.ResourceLogs().At(i)
		resourceAttrs := AttrsToMap(resourceLogs.Resource().Attributes())
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			scopeLogs := resourceLogs.ScopeLogs().At(j)
			for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
				record := scopeLogs.LogRecords().At(k)
				if ShouldDropLog(resourceAttrs, record) {
					continue
				}
				events = append(events, c.EventFromLog(resourceAttrs, record))
			}
		}
	}
	return events
}

func (c Converter) EventsFromTraces(traces ptrace.Traces) []Event {
	var events []Event
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		resourceSpans := traces.ResourceSpans().At(i)
		resourceAttrs := AttrsToMap(resourceSpans.Resource().Attributes())
		for j := 0; j < resourceSpans.ScopeSpans().Len(); j++ {
			scopeSpans := resourceSpans.ScopeSpans().At(j)
			for k := 0; k < scopeSpans.Spans().Len(); k++ {
				span := scopeSpans.Spans().At(k)
				if c.ShouldDropSpan(resourceAttrs, span) {
					continue
				}
				events = append(events, c.EventFromSpan(resourceAttrs, span))
			}
		}
	}
	return events
}

func (c Converter) EventsFromMetrics(metrics pmetric.Metrics) []Event {
	var events []Event
	for i := 0; i < metrics.ResourceMetrics().Len(); i++ {
		resourceMetrics := metrics.ResourceMetrics().At(i)
		resourceAttrs := AttrsToMap(resourceMetrics.Resource().Attributes())
		for j := 0; j < resourceMetrics.ScopeMetrics().Len(); j++ {
			scopeMetrics := resourceMetrics.ScopeMetrics().At(j)
			for k := 0; k < scopeMetrics.Metrics().Len(); k++ {
				metric := scopeMetrics.Metrics().At(k)
				if ShouldDropMetric(resourceAttrs, metric.Name(), c.opts.IncludeRuntimeMetrics) {
					continue
				}
				events = append(events, c.EventFromMetric(resourceAttrs, metric))
			}
		}
	}
	return events
}

func ShouldDropLog(resourceAttrs map[string]interface{}, record plog.LogRecord) bool {
	attrs := MergeMaps(resourceAttrs, AttrsToMap(record.Attributes()))
	if HarnessName(attrs, record.Body().AsString()) != "codex_cli" {
		return false
	}
	return isNoisyCodexLog(attrs, record.Body().AsString())
}

func isNoisyCodexLog(attrs map[string]interface{}, body string) bool {
	eventName := CodexLogEventName(attrs)
	if eventName == "" {
		message := strings.ToLower(FirstNonEmpty(body, FirstString(attrs, "message", "log.message")))
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
	return strings.HasPrefix(eventName, "codex.")
}

func ShouldDropMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics bool) bool {
	if shouldDropCodexMetric(resourceAttrs, name) {
		return true
	}
	if shouldDropOpenClawMetric(resourceAttrs, name, includeRuntimeMetrics) {
		return true
	}
	if shouldDropCopilotMetric(resourceAttrs, name, includeRuntimeMetrics) {
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
	dropPrefixes := []string{"process.", "nodejs.", "runtime.nodejs.", "v8js."}
	for _, prefix := range dropPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func shouldDropCodexMetric(resourceAttrs map[string]interface{}, name string) bool {
	if HarnessName(resourceAttrs, name) != "codex_cli" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(normalized, "codex.")
}

func shouldDropOpenClawMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics bool) bool {
	if includeRuntimeMetrics {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	if HarnessName(resourceAttrs, name) != "openclaw_gateway" {
		return false
	}
	return true
}

func shouldDropCopilotMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics bool) bool {
	if includeRuntimeMetrics {
		return false
	}
	if HarnessName(resourceAttrs, name) != "copilot_cli" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	return true
}

func (c Converter) EventFromLog(resourceAttrs map[string]interface{}, record plog.LogRecord) Event {
	attrs := MergeMaps(resourceAttrs, AttrsToMap(record.Attributes()))
	ts := Timestamp(record.Timestamp().AsTime())
	action := FirstString(attrs, "beacon.event.action", "event.action", "gen_ai.agent.action", "ai.agent.action")
	if action == "" {
		action = InferAction(attrs, record.Body().AsString())
	}
	message := FirstNonEmpty(record.Body().AsString(), FirstString(attrs, "message", "log.message", "event.name"))
	event := NewEvent(action, EventCategory(action, FirstString(attrs, "beacon.event.category", "event.category", "category")), Severity(record.SeverityText(), record.SeverityNumber().String()), HarnessName(attrs, message), ts)
	event.Message = message
	c.PopulateCommon(&event, attrs)
	event.Raw = c.RawPayload(attrs, map[string]interface{}{
		"otel_signal": "logs",
		"severity":    record.SeverityText(),
	})
	c.NormalizeCodexLogEvent(&event, attrs)
	return event
}

func (c Converter) EventFromSpan(resourceAttrs map[string]interface{}, span ptrace.Span) Event {
	attrs := MergeMaps(resourceAttrs, AttrsToMap(span.Attributes()))
	action := FirstString(attrs, "beacon.event.action", "event.action", "gen_ai.agent.action", "ai.agent.action")
	if action == "" {
		action = InferAction(attrs, span.Name())
	}
	message := FirstNonEmpty(FirstString(attrs, "message", "gen_ai.prompt", "gen_ai.response"), span.Name())
	event := NewEvent(action, EventCategory(action, FirstString(attrs, "beacon.event.category", "event.category", "tool")), SpanSeverity(span.Status().Code().String()), HarnessName(attrs, message, span.Name()), Timestamp(span.StartTimestamp().AsTime()))
	event.Message = message
	c.PopulateCommon(&event, attrs)
	event.Raw = c.RawPayload(attrs, map[string]interface{}{
		"otel_signal": "traces",
		"span_name":   span.Name(),
		"span_kind":   span.Kind().String(),
		"status":      span.Status().Code().String(),
	})
	return event
}

func (c Converter) ShouldDropSpan(resourceAttrs map[string]interface{}, span ptrace.Span) bool {
	attrs := MergeMaps(resourceAttrs, AttrsToMap(span.Attributes()))
	if HarnessName(attrs, span.Name()) != "codex_cli" {
		return false
	}
	return !c.opts.IncludeCodexSpans
}

func (c Converter) NormalizeCodexLogEvent(event *Event, attrs map[string]interface{}) {
	if event == nil || event.Harness.Name != "codex_cli" {
		return
	}
	switch CodexLogEventName(attrs) {
	case CodexConversationStarts:
		event.Event.Action = "session.started"
		event.Event.Category = "session"
		event.Message = "Codex session started"
	case CodexUserPrompt:
		event.Event.Action = "prompt.submitted"
		event.Event.Category = "prompt"
		event.Message = "Codex prompt submitted"
	case CodexToolDecision:
		decision := FirstString(attrs, "decision")
		if strings.EqualFold(decision, "denied") || strings.EqualFold(decision, "deny") {
			event.Event.Action = "approval.denied"
		} else {
			event.Event.Action = "approval.requested"
		}
		event.Event.Category = "approval"
		event.Message = "Codex tool decision"
		if event.Approval == nil {
			event.Approval = &ApprovalInfo{}
		}
		event.Approval.Required = true
		event.Approval.Decision = decision
		event.Approval.Reason = FirstString(attrs, "source", "approval_mode", "active_approval_mode")
	case CodexToolResult:
		NormalizeCodexToolResult(event, attrs)
	}
}

func CodexLogEventName(attrs map[string]interface{}) string {
	return strings.ToLower(FirstString(attrs, "event.name"))
}

func NormalizeCodexToolResult(event *Event, attrs map[string]interface{}) {
	toolName := FirstString(attrs, "tool.name", "tool_name", "function_name", "tool", "mcp_server")
	args := FirstString(attrs, "arguments", "function_args", "tool.command", "command")
	event.Event.Action = "tool.invoked"
	event.Event.Category = "tool"
	if event.Tool == nil {
		event.Tool = &ToolInfo{}
	}
	event.Tool.Name = toolName
	event.Tool.Command = args
	if command := codexShellCommand(toolName, args); command != "" {
		event.Event.Action = "command.executed"
		event.Event.Category = "command"
		event.Command = &CommandInfo{Command: command}
	}
	event.Message = FirstNonEmpty(toolName, "Codex tool result")
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

func (c Converter) EventFromMetric(resourceAttrs map[string]interface{}, metric pmetric.Metric) Event {
	attrs := MergeMaps(resourceAttrs, map[string]interface{}{})
	action := FirstString(attrs, "beacon.event.action", "event.action")
	if action == "" {
		action = "metric.observed"
	}
	event := NewEvent(action, "metric", "info", HarnessName(attrs, metric.Name()), time.Now().UTC())
	event.Message = metric.Name()
	event.Tokens = ExtractTokensFromMetric(metric)
	c.PopulateCommon(&event, attrs)
	event.Raw = c.RawPayload(attrs, map[string]interface{}{
		"otel_signal":        "metrics",
		"metric_name":        metric.Name(),
		"metric_description": metric.Description(),
		"metric_unit":        metric.Unit(),
	})
	return event
}

func (c Converter) PopulateCommon(event *Event, attrs map[string]interface{}) {
	event.Model = FirstString(attrs, "gen_ai.request.model", "gen_ai.response.model", "model", "ai.model")
	event.Repository = FirstString(attrs, "vcs.repository.url", "repository", "repo.path", "workspace.repository")
	event.Branch = FirstString(attrs, "vcs.branch.name", "git.branch", "branch")
	if id := FirstString(attrs, "session.id", "conversation.id", "conversation_id", "gen_ai.conversation.id"); id != "" || FirstString(attrs, "cwd", "working_directory", "workspace") != "" {
		event.Session = &SessionInfo{
			ID:               id,
			WorkingDirectory: FirstString(attrs, "cwd", "working_directory", "process.command_args.cwd", "workspace"),
		}
	}
	if name := FirstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name", "function_name", "tool_name"); name != "" || FirstString(attrs, "tool.command", "command", "function_args") != "" {
		event.Tool = &ToolInfo{
			Name:    name,
			Command: FirstString(attrs, "tool.command", "command", "process.command_line", "function_args"),
			Path:    FirstString(attrs, "tool.path", "file.path", "file_path"),
		}
	}
	if path := FirstString(attrs, "file.path", "file_path", "code.filepath"); path != "" {
		event.File = &FileInfo{
			Path:      path,
			Operation: FirstString(attrs, "file.operation", "operation"),
			Language:  FirstString(attrs, "code.language", "language"),
		}
	}
	if command := FirstString(attrs, "command", "process.command_line", "shell.command"); command != "" {
		event.Command = &CommandInfo{Command: command}
		if exitCode, ok := IntAttr(attrs, "exit_code", "process.exit_code", "command.exit_code"); ok {
			event.Command.ExitCode = &exitCode
		}
		if duration, ok := Int64Attr(attrs, "duration_ms", "command.duration_ms"); ok {
			event.Command.DurationMS = duration
		}
	}
	if server := FirstString(attrs, "mcp.server.name", "mcp.server", "gen_ai.mcp.server", "mcp_server_name"); server != "" || FirstString(attrs, "mcp.tool.name") != "" || FirstString(attrs, "tool_type") == "mcp" {
		event.MCP = &MCPInfo{
			Server: server,
			Tool:   FirstString(attrs, "mcp.tool.name", "tool.name", "function_name"),
		}
	}
	if decision := FirstString(attrs, "approval.decision", "policy.decision", "decision"); decision != "" {
		event.Approval = &ApprovalInfo{
			Required: true,
			Decision: decision,
			Reason:   FirstString(attrs, "approval.reason", "policy.reason", "approval_mode", "active_approval_mode"),
		}
	}
	if c.opts.ContentRetention != "metadata" && event.Event.Category == "prompt" {
		if text := FirstString(attrs, "gen_ai.prompt", "prompt", "user_prompt", "input.prompt"); text != "" {
			event.Prompt = &PromptInfo{Text: text}
		}
	}
	event.Content = &ContentInfo{Retention: c.opts.ContentRetention, Included: c.opts.ContentRetention != "metadata", Redacted: c.opts.ContentRetention == "redacted"}
	if event.Tokens == nil {
		event.Tokens = ExtractTokensFromAttrs(attrs)
	}
}

func (c Converter) RawPayload(attrs map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	for k, v := range extra {
		raw[k] = v
	}
	if c.opts.ContentRetention == "metadata" {
		raw["attribute_count"] = len(attrs)
		return raw
	}
	raw["attributes"] = attrs
	return raw
}

func AttrsToMap(attrs pcommon.Map) map[string]interface{} {
	out := make(map[string]interface{}, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		out[k] = v.AsRaw()
		return true
	})
	return out
}

func MergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func FirstString(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if str := strings.TrimSpace(fmt.Sprint(value)); str != "" && str != "<nil>" {
				return str
			}
		}
	}
	return ""
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func IntAttr(attrs map[string]interface{}, keys ...string) (int, bool) {
	value, ok := Int64Attr(attrs, keys...)
	return int(value), ok
}

func Int64Attr(attrs map[string]interface{}, keys ...string) (int64, bool) {
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

func Timestamp(ts time.Time) time.Time {
	if ts.IsZero() || ts.UnixNano() == 0 {
		return time.Now().UTC()
	}
	return ts
}

func Severity(text, number string) string {
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

func SpanSeverity(status string) string {
	if strings.Contains(strings.ToLower(status), "error") {
		return "high"
	}
	return "info"
}

func HarnessName(attrs map[string]interface{}, hints ...string) string {
	name := FirstString(attrs, "beacon.harness.name", "harness.name", "service.name", "telemetry.sdk.name")
	if explicit := FirstString(attrs, "beacon.harness.name", "harness.name"); explicit != "" {
		return NormalizeHarnessName(explicit)
	}
	candidates := append([]string{name}, hints...)
	for _, candidate := range candidates {
		if normalized := NormalizeHarnessName(candidate); normalized != "" {
			return normalized
		}
	}
	if name != "" {
		return name
	}
	return "otel"
}

func NormalizeHarnessName(name string) string {
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
	case strings.Contains(lower, "antigravity") || strings.Contains(lower, "anti-gravity"):
		return "antigravity_cli"
	case strings.Contains(lower, "codex"):
		return "codex_cli"
	case strings.Contains(lower, "gemini"):
		return "gemini_cli"
	case strings.Contains(lower, "github-copilot") || strings.Contains(lower, "copilot-chat") || strings.Contains(lower, "copilot_cli") || strings.Contains(lower, "copilot"):
		return "copilot_cli"
	case name != "":
		return name
	default:
		return ""
	}
}

func InferAction(attrs map[string]interface{}, fallback string) string {
	tool := strings.ToLower(FirstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name", "function_name", "tool_name"))
	operation := strings.ToLower(FirstString(attrs, "gen_ai.operation.name"))
	harness := HarnessName(attrs, fallback)
	text := strings.ToLower(strings.Join([]string{
		fallback,
		tool,
		operation,
		FirstString(attrs, "event.name", "codex.op", "rpc.method"),
	}, " "))
	switch {
	case harness == "copilot_cli":
		return CopilotAction(attrs, operation, text)
	case strings.Contains(text, "gemini_cli.user_prompt"):
		return "prompt.submitted"
	case strings.Contains(text, "gemini_cli.tool_call"):
		return GeminiToolAction(attrs)
	case strings.Contains(text, "gemini_cli.file_operation"):
		return GeminiFileAction(attrs)
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

func CopilotAction(attrs map[string]interface{}, operation, text string) string {
	switch {
	case operation == "invoke_agent":
		return "session.activity"
	case operation == "chat":
		initiator := strings.ToLower(FirstString(attrs, "github.copilot.initiator"))
		turnID := FirstString(attrs, "github.copilot.turn_id")
		if initiator == "agent" || (turnID != "" && turnID != "0") {
			return "session.activity"
		}
		return "prompt.submitted"
	case operation == "execute_tool":
		return "tool.invoked"
	case strings.Contains(text, "permission"):
		return "approval.requested"
	default:
		return "tool.invoked"
	}
}

func GeminiToolAction(attrs map[string]interface{}) string {
	if FirstString(attrs, "tool_type") == "mcp" || FirstString(attrs, "mcp_server_name") != "" {
		return "mcp.tool_invoked"
	}
	return "tool.invoked"
}

func GeminiFileAction(attrs map[string]interface{}) string {
	switch strings.ToLower(FirstString(attrs, "operation")) {
	case "read":
		return "file.read"
	case "create":
		return "file.created"
	default:
		return "file.modified"
	}
}

func EventCategory(action, explicit string) string {
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
	case strings.HasPrefix(action, "session."):
		return "session"
	case strings.HasPrefix(action, "metric."):
		return "metric"
	case strings.HasPrefix(action, "tool."):
		return "tool"
	default:
		return ""
	}
}
