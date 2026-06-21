package beaconevent

import (
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
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

// Beacon event action vocabulary. These are the canonical "<category>.<verb>" action
// values the converter emits and matches against; they are de-facto schema identifiers,
// so reference these constants rather than repeating the string literals.
const (
	ActionSessionStarted    = "session.started"
	ActionSessionActivity   = "session.activity"
	ActionPromptSubmitted   = "prompt.submitted"
	ActionApprovalRequested = "approval.requested"
	ActionApprovalDenied    = "approval.denied"
	ActionToolInvoked       = "tool.invoked"
	ActionToolFailed        = "tool.failed"
	ActionCommandExecuted   = "command.executed"
	ActionFileRead          = "file.read"
	ActionFileModified      = "file.modified"
	ActionFileCreated       = "file.created"
	ActionMCPToolInvoked    = "mcp.tool_invoked"
)

var allowedCodexLogEvents = map[string]struct{}{
	CodexConversationStarts: {},
	CodexUserPrompt:         {},
	CodexToolDecision:       {},
	CodexToolResult:         {},
}

var allowedVSCodeCopilotLogEvents = map[string]struct{}{
	"copilot_chat.tool.call":            {},
	"copilot_chat.edit.feedback":        {},
	"copilot_chat.edit.hunk.action":     {},
	"copilot_chat.inline.done":          {},
	"copilot_chat.cloud.session.invoke": {},
}

var noisyCodexLogMessages = []string{
	"runtime metrics reset skipped",
	"flushing otel metrics",
}

type Options struct {
	IncludeRuntimeMetrics bool
	IncludeCodexSpans     bool
}

type Converter struct {
	opts Options
}

func NewConverter(opts Options) Converter {
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
				events = append(events, c.EventsFromMetric(resourceAttrs, metric)...)
			}
		}
	}
	return events
}

func ShouldDropLog(resourceAttrs map[string]interface{}, record plog.LogRecord) bool {
	attrs := MergeMaps(resourceAttrs, AttrsToMap(record.Attributes()))
	switch HarnessName(attrs, record.Body().AsString()) {
	case "codex_cli":
		return isNoisyCodexLog(attrs, record.Body().AsString())
	case "vscode_copilot":
		return isNoisyVSCodeCopilotLog(attrs, record.Body().AsString())
	default:
		return false
	}
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

func isNoisyVSCodeCopilotLog(attrs map[string]interface{}, body string) bool {
	eventName := FirstString(attrs, "event.name", "name")
	if eventName == "" {
		eventName = strings.TrimSpace(body)
	}
	if _, ok := allowedVSCodeCopilotLogEvents[eventName]; ok {
		return false
	}
	return true
}

func ShouldDropMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics bool) bool {
	if shouldDropCodexMetric(resourceAttrs, name) {
		return true
	}
	if shouldDropVSCodeCopilotMetric(resourceAttrs, name, includeRuntimeMetrics) {
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
	if normalized == "codex.turn.token_usage" {
		// Codex reports per-turn token usage only on this metric, so keep it for
		// gen_ai.usage normalization. Other codex.* metrics (including the memory
		// *.token_usage phases) stay dropped as high-volume runtime noise.
		return false
	}
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

func shouldDropVSCodeCopilotMetric(resourceAttrs map[string]interface{}, name string, includeRuntimeMetrics bool) bool {
	if includeRuntimeMetrics {
		return false
	}
	if HarnessName(resourceAttrs, name) != "vscode_copilot" {
		return false
	}
	return true
}

func shouldDropVSCodeCopilotSpan(attrs map[string]interface{}, spanName string, includeRuntimeMetrics bool) bool {
	if includeRuntimeMetrics {
		return false
	}
	operation := strings.ToLower(FirstString(attrs, "gen_ai.operation.name"))
	name := strings.ToLower(spanName)
	switch operation {
	case "invoke_agent", "execute_tool", "execute_hook":
		return false
	case "chat", "embeddings":
		return true
	}
	if strings.Contains(name, "invoke_agent") || strings.Contains(name, "execute_tool") || strings.Contains(name, "execute_hook") {
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
	if !record.TraceID().IsEmpty() {
		event.Trace = &TraceInfo{ID: record.TraceID().String()}
		if !record.SpanID().IsEmpty() {
			event.Trace.SpanID = record.SpanID().String()
		}
	}
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
	if !span.TraceID().IsEmpty() {
		event.Trace = &TraceInfo{ID: span.TraceID().String()}
		if !span.SpanID().IsEmpty() {
			event.Trace.SpanID = span.SpanID().String()
		}
		if !span.ParentSpanID().IsEmpty() {
			event.Trace.ParentSpanID = span.ParentSpanID().String()
		}
	}
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
	switch HarnessName(attrs, span.Name()) {
	case "codex_cli":
		return !c.opts.IncludeCodexSpans
	case "vscode_copilot":
		return shouldDropVSCodeCopilotSpan(attrs, span.Name(), c.opts.IncludeRuntimeMetrics)
	default:
		return false
	}
}

func (c Converter) NormalizeCodexLogEvent(event *Event, attrs map[string]interface{}) {
	if event == nil || event.Harness.Name != "codex_cli" {
		return
	}
	switch CodexLogEventName(attrs) {
	case CodexConversationStarts:
		event.Event.Action = ActionSessionStarted
		event.Event.Category = "session"
		event.Message = "Codex session started"
	case CodexUserPrompt:
		event.Event.Action = ActionPromptSubmitted
		event.Event.Category = "prompt"
		event.Message = "Codex prompt submitted"
	case CodexToolDecision:
		decision := FirstString(attrs, "decision")
		if strings.EqualFold(decision, "denied") || strings.EqualFold(decision, "deny") {
			event.Event.Action = ActionApprovalDenied
		} else {
			event.Event.Action = ActionApprovalRequested
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
	toolName := FirstString(attrs, codexToolNameKeys...)
	args := FirstString(attrs, "arguments", "function_args", "tool.command", "command")
	event.Event.Action = ActionToolInvoked
	event.Event.Category = "tool"
	if event.Tool == nil {
		event.Tool = &ToolInfo{}
	}
	event.Tool.Name = toolName
	event.Tool.Command = args
	if command := codexShellCommand(toolName, args); command != "" {
		event.Event.Action = ActionCommandExecuted
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
	c.PopulateCommon(&event, attrs)
	event.Raw = c.RawPayload(attrs, map[string]interface{}{
		"otel_signal":        "metrics",
		"metric_name":        metric.Name(),
		"metric_description": metric.Description(),
		"metric_unit":        metric.Unit(),
	})
	return event
}

// IsTokenUsageMetric reports whether a metric carries token counts as
// datapoint values, such as Claude Code's claude_code.token.usage counter or
// the semconv gen_ai.client.token.usage histogram. Matching is deliberately
// tight so unrelated metrics keep the generic metric.observed conversion.
func IsTokenUsageMetric(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "gen_ai.client.token.usage" ||
		normalized == "codex.turn.token_usage" ||
		strings.HasSuffix(normalized, ".token.usage")
}

// IsCostUsageMetric reports whether a metric carries runtime-reported cost as
// datapoint values, such as Claude Code's claude_code.cost.usage counter.
func IsCostUsageMetric(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".cost.usage")
}

// EventsFromMetric expands token and cost usage metrics into one event per
// datapoint so values and datapoint attributes (token type, model, session)
// survive into JSONL. Other metrics, and usage metrics without datapoints,
// keep the single metric.observed event.
func (c Converter) EventsFromMetric(resourceAttrs map[string]interface{}, metric pmetric.Metric) []Event {
	if IsTokenUsageMetric(metric.Name()) || IsCostUsageMetric(metric.Name()) {
		if events := c.eventsFromUsageMetric(resourceAttrs, metric); len(events) > 0 {
			return events
		}
	}
	return []Event{c.EventFromMetric(resourceAttrs, metric)}
}

func (c Converter) eventsFromUsageMetric(resourceAttrs map[string]interface{}, metric pmetric.Metric) []Event {
	var events []Event
	// Codex reports input tokens inclusive of the cached_input subset (input =
	// uncached + cached, and total = input + output). Beacon's gen_ai.usage keeps
	// input_tokens and cache_read disjoint like Claude Code, so reduce input by
	// the per-turn cached_input before normalizing; otherwise totals double-count
	// cached prompt tokens. Returns nil (no-op) for every other usage metric.
	cachedInputByTurn := codexCachedInputByTimestamp(metric)
	adjustValue := func(dpAttrs pcommon.Map, ts pcommon.Timestamp, value float64) float64 {
		if cachedInputByTurn == nil {
			return value
		}
		if tt := FirstString(AttrsToMap(dpAttrs), "token_type"); strings.EqualFold(tt, "input") {
			if value -= cachedInputByTurn[ts.AsTime().UnixNano()]; value < 0 {
				value = 0
			}
		}
		return value
	}
	switch metric.Type() {
	case pmetric.MetricTypeSum:
		sum := metric.Sum()
		extra := map[string]interface{}{
			"metric_type":        metric.Type().String(),
			"metric_temporality": sum.AggregationTemporality().String(),
			"metric_monotonic":   sum.IsMonotonic(),
		}
		for i := 0; i < sum.DataPoints().Len(); i++ {
			dp := sum.DataPoints().At(i)
			events = append(events, c.usageEventFromDataPoint(resourceAttrs, metric, dp.Attributes(), dp.Timestamp(), adjustValue(dp.Attributes(), dp.Timestamp(), numberDataPointValue(dp)), extra))
		}
	case pmetric.MetricTypeGauge:
		gauge := metric.Gauge()
		extra := map[string]interface{}{"metric_type": metric.Type().String()}
		for i := 0; i < gauge.DataPoints().Len(); i++ {
			dp := gauge.DataPoints().At(i)
			events = append(events, c.usageEventFromDataPoint(resourceAttrs, metric, dp.Attributes(), dp.Timestamp(), adjustValue(dp.Attributes(), dp.Timestamp(), numberDataPointValue(dp)), extra))
		}
	case pmetric.MetricTypeHistogram:
		histogram := metric.Histogram()
		for i := 0; i < histogram.DataPoints().Len(); i++ {
			dp := histogram.DataPoints().At(i)
			if !dp.HasSum() {
				continue
			}
			extra := map[string]interface{}{
				"metric_type":        metric.Type().String(),
				"metric_temporality": histogram.AggregationTemporality().String(),
				"metric_count":       int64(dp.Count()),
			}
			events = append(events, c.usageEventFromDataPoint(resourceAttrs, metric, dp.Attributes(), dp.Timestamp(), adjustValue(dp.Attributes(), dp.Timestamp(), dp.Sum()), extra))
		}
	}
	return events
}

// codexCachedInputByTimestamp sums each turn's cached_input datapoints from the
// codex.turn.token_usage histogram, keyed by datapoint timestamp, so the input
// datapoint can be reduced to its uncached portion (Codex's input is inclusive
// of cached_input). Returns nil for any other metric so the generic usage path
// is unaffected.
func codexCachedInputByTimestamp(metric pmetric.Metric) map[int64]float64 {
	if !strings.EqualFold(strings.TrimSpace(metric.Name()), "codex.turn.token_usage") {
		return nil
	}
	if metric.Type() != pmetric.MetricTypeHistogram {
		return nil
	}
	out := map[int64]float64{}
	dps := metric.Histogram().DataPoints()
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		if !dp.HasSum() {
			continue
		}
		if tt := FirstString(AttrsToMap(dp.Attributes()), "token_type"); strings.EqualFold(tt, "cached_input") {
			out[dp.Timestamp().AsTime().UnixNano()] += dp.Sum()
		}
	}
	return out
}

func numberDataPointValue(dp pmetric.NumberDataPoint) float64 {
	if dp.ValueType() == pmetric.NumberDataPointValueTypeInt {
		return float64(dp.IntValue())
	}
	return dp.DoubleValue()
}

func (c Converter) usageEventFromDataPoint(resourceAttrs map[string]interface{}, metric pmetric.Metric, dpAttrs pcommon.Map, ts pcommon.Timestamp, value float64, extra map[string]interface{}) Event {
	attrs := MergeMaps(resourceAttrs, AttrsToMap(dpAttrs))
	action := "token.usage"
	if IsCostUsageMetric(metric.Name()) {
		action = "cost.usage"
	}
	event := NewEvent(action, "metric", "info", HarnessName(attrs, metric.Name()), Timestamp(ts.AsTime()))
	event.Message = metric.Name()
	c.PopulateCommon(&event, attrs)
	// The datapoint value is the authoritative usage for this event. Drop any
	// gen_ai.usage.* that PopulateCommon read from merged attributes so a stray
	// usage attribute on the resource or datapoint cannot ride along on every
	// expanded datapoint event and inflate aggregated totals.
	if event.GenAI != nil {
		event.GenAI.Usage = nil
	}
	if action == "cost.usage" {
		cost := value
		if event.GenAI == nil {
			event.GenAI = &GenAIInfo{}
		}
		if event.GenAI.Usage == nil {
			event.GenAI.Usage = &GenAIUsageInfo{}
		}
		event.GenAI.Usage.CostUSD = &cost
	} else {
		ApplyTokenUsage(&event, FirstString(attrs, "type", "token_type", "gen_ai.token.type"), int64(math.Round(value)))
	}
	rawExtra := map[string]interface{}{
		"otel_signal":        "metrics",
		"metric_name":        metric.Name(),
		"metric_description": metric.Description(),
		"metric_unit":        metric.Unit(),
		"metric_value":       value,
	}
	for k, v := range extra {
		rawExtra[k] = v
	}
	event.Raw = c.RawPayload(attrs, rawExtra)
	return event
}

// ApplyTokenUsage merges a typed token count into the event's canonical
// gen_ai.usage struct. Claude Code's cacheRead/cacheCreation and Codex's
// cachedInput/reasoningOutput token types extend the semconv input/output enum.
// Codex's "total" rollup type is left unmapped so it never sums on top of the
// per-type counts. Unknown types only record the type so the raw value stays
// inspectable without polluting usage totals.
// gen_ai.usage field setters centralize how each canonical token type maps onto the
// usage struct (including the nested cache/reasoning shapes and pointer handling). They
// are shared by ApplyTokenUsage (metric datapoints, one token type per call) and
// GenAIUsageFromAttrs (span/log attributes, all token types at once) so the mapping lives
// in exactly one place. Each takes value by copy, so &v is safe per call.
func (c Converter) PopulateCommon(event *Event, attrs map[string]interface{}) {
	populateRunContext(event, attrs)
	event.GenAI = GenAIFromAttrs(attrs)
	event.Model = FirstString(attrs, "gen_ai.request.model", "gen_ai.response.model", "model", "ai.model")
	event.Repository = FirstString(attrs, "vcs.repository.url", "repository", "repo.path", "workspace.repository")
	event.Branch = FirstString(attrs, "vcs.branch.name", "git.branch", "branch")
	if id := FirstString(attrs, "gen_ai.conversation.id", "beacon.session.id", "copilot_chat.session_id", "copilot_chat.chat_session_id", "conversation.id", "conversation_id", "session.id"); id != "" || FirstString(attrs, "cwd", "working_directory", "workspace") != "" {
		event.Session = &SessionInfo{
			ID:               id,
			WorkingDirectory: FirstString(attrs, "cwd", "working_directory", "beacon.session.working_directory", "process.command_args.cwd", "workspace"),
		}
	}
	if name := FirstString(attrs, toolNameKeys...); name != "" || ToolCommandString(attrs) != "" {
		event.Tool = &ToolInfo{
			Name:    name,
			Command: FirstNonEmpty(ToolCommandString(attrs), FirstString(attrs, "process.command_line")),
			Path:    FirstString(attrs, "tool.path", "file.path", "file_path"),
		}
	}
	path := FirstString(attrs, "file.path", "file_path", "code.filepath")
	operation := FirstString(attrs, "file.operation", "operation")
	if path == "" {
		path = FilePathFromURI(FirstString(attrs, "mcp.resource.uri"))
		if path != "" && operation == "" && event.Event.Action == ActionFileRead {
			operation = "read"
		}
	}
	if path != "" {
		event.File = &FileInfo{
			Path:      path,
			Operation: operation,
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
	if mcp := MCPFromAttrs(attrs); mcp != nil {
		event.MCP = mcp
	}
	if decision := FirstString(attrs, "approval.decision", "policy.decision", "decision"); decision != "" {
		event.Approval = &ApprovalInfo{
			Required: true,
			Decision: decision,
			Reason:   FirstString(attrs, "approval.reason", "policy.reason", "approval_mode", "active_approval_mode"),
		}
	}
	if event.Event.Category == "prompt" {
		if text := FirstNonEmpty(FirstTextAttr(attrs, "beacon.prompt.text", "gen_ai.prompt", "prompt", "user_prompt", "input.prompt", "copilot_chat.user_request"), FirstMessageText(event.GenAI)); text != "" {
			event.Prompt = &PromptInfo{Text: text}
		}
	}
}

func MCPFromAttrs(attrs map[string]interface{}) *MCPInfo {
	server := FirstString(attrs, "mcp.server.name", "mcp.server", "gen_ai.mcp.server", "mcp_server_name")
	tool := FirstString(attrs, mcpToolNameKeys...)
	method := FirstString(attrs, "mcp.method.name")
	protocol := FirstString(attrs, "mcp.protocol.version")
	resource := FirstString(attrs, "mcp.resource.uri")
	session := FirstString(attrs, "mcp.session.id")
	if server == "" && tool == "" && method == "" && protocol == "" && resource == "" && session == "" && FirstString(attrs, "tool_type") != "mcp" {
		return nil
	}
	out := &MCPInfo{Server: server, Tool: tool}
	if method != "" {
		out.Method = &MCPMethodInfo{Name: method}
	}
	if protocol != "" {
		out.Protocol = &MCPProtocolInfo{Version: protocol}
	}
	if resource != "" {
		out.Resource = &MCPResourceInfo{URI: resource}
	}
	if session != "" {
		out.Session = &MCPSessionInfo{ID: session}
	}
	return out
}

func populateRunContext(event *Event, attrs map[string]interface{}) {
	switch RunString(attrs, asymptoteobserve.AttributeOrigin) {
	case string(asymptoteobserve.OriginLocal):
		event.Origin = asymptoteobserve.OriginLocal
	case string(asymptoteobserve.OriginCloud):
		event.Origin = asymptoteobserve.OriginCloud
	case string(asymptoteobserve.OriginCI):
		event.Origin = asymptoteobserve.OriginCI
	}
	run := RunInfo{
		Provider:   RunString(attrs, asymptoteobserve.AttributeRunProvider),
		RunID:      RunString(attrs, asymptoteobserve.AttributeRunID),
		RunAttempt: RunString(attrs, asymptoteobserve.AttributeRunAttempt),
		Workflow:   RunString(attrs, asymptoteobserve.AttributeRunWorkflow),
		Job:        RunString(attrs, asymptoteobserve.AttributeRunJob),
		EventName:  RunString(attrs, asymptoteobserve.AttributeRunEventName),
		Commit:     RunString(attrs, asymptoteobserve.AttributeRunCommit),
		Repository: RunString(attrs, asymptoteobserve.AttributeRunRepository),
		Branch:     RunString(attrs, asymptoteobserve.AttributeRunBranch),
		PR:         RunString(attrs, asymptoteobserve.AttributeRunPR),
		PRNumber:   RunString(attrs, asymptoteobserve.AttributeRunPRNumber),
		Actor:      RunString(attrs, asymptoteobserve.AttributeRunActor),
	}
	if ephemeral, ok := BoolAttr(attrs, asymptoteobserve.AttributeRunEphemeral); ok {
		run.Ephemeral = ephemeral
	}
	if run.Provider == "" && run.RunID == "" && run.RunAttempt == "" && run.Workflow == "" && run.Job == "" && run.EventName == "" && run.Commit == "" && run.Repository == "" && run.Branch == "" && run.PR == "" && run.PRNumber == "" && run.Actor == "" && !run.Ephemeral {
		return
	}
	event.Run = &run
}

func (c Converter) RawPayload(attrs map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	for k, v := range extra {
		raw[k] = v
	}
	raw["attributes"] = attrs
	return raw
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

// harnessNameRules maps substring aliases to a canonical harness name. Rules are
// evaluated in order and the first whose lower-cased input contains any alias wins, so
// more specific names (e.g. "claude_code", "copilot-chat") must precede the broader
// catch-alls ("claude", "copilot"). This replaces a hand-written switch ladder; the
// classification characterization tests pin the resulting mappings.
var harnessNameRules = []struct {
	aliases []string
	name    string
}{
	{[]string{"cowork", "co-work"}, "claude_cowork"},
	{[]string{"claude_agent_sdk", "claude-agent-sdk", "claude agent sdk"}, "claude_agent_sdk"},
	{[]string{"claude_code", "claude-code", "claude code"}, "claude_code"},
	{[]string{"claude"}, "claude_code"},
	{[]string{"openclaw", "open-claw"}, "openclaw_gateway"},
	{[]string{"antigravity", "anti-gravity"}, "antigravity_cli"},
	{[]string{"codex"}, "codex_cli"},
	{[]string{"gemini"}, "gemini_cli"},
	{[]string{"copilot-chat"}, "vscode_copilot"},
	{[]string{"github-copilot", "copilot_cli", "copilot"}, "copilot_cli"},
}

func NormalizeHarnessName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return ""
	}
	for _, rule := range harnessNameRules {
		for _, alias := range rule.aliases {
			if strings.Contains(lower, alias) {
				return rule.name
			}
		}
	}
	// Unrecognized but non-empty: return the original (untrimmed) name verbatim.
	return name
}

func InferAction(attrs map[string]interface{}, fallback string) string {
	tool := strings.ToLower(FirstString(attrs, toolNameKeys...))
	operation := strings.ToLower(FirstString(attrs, "gen_ai.operation.name"))
	mcpMethod := strings.ToLower(FirstString(attrs, "mcp.method.name"))
	harness := HarnessName(attrs, fallback)
	text := strings.ToLower(strings.Join([]string{
		fallback,
		tool,
		operation,
		mcpMethod,
		FirstString(attrs, "event.name", "codex.op", "rpc.method"),
	}, " "))
	// Structured, heterogeneous matchers run first and short-circuit. Their order is
	// significant relative to the keyword fallback below: e.g. the gemini_cli.file_operation
	// case must precede the "file" keyword rule, which would otherwise claim it.
	switch {
	case harness == "copilot_cli" || harness == "vscode_copilot":
		return CopilotAction(attrs, operation, text)
	case mcpMethod == "tools/call":
		return ActionMCPToolInvoked
	case mcpMethod == "resources/read" && IsFileURI(FirstString(attrs, "mcp.resource.uri")):
		return ActionFileRead
	case operation == "execute_tool":
		return ActionToolInvoked
	case (operation == "chat" || operation == "generate_content" || operation == "text_completion") && HasPromptLikeContent(attrs):
		return ActionPromptSubmitted
	case HasToolCall(attrs):
		return ActionToolInvoked
	case strings.Contains(text, "gemini_cli.user_prompt"):
		return ActionPromptSubmitted
	case strings.Contains(text, "gemini_cli.tool_call"):
		return GeminiToolAction(attrs)
	case strings.Contains(text, "gemini_cli.file_operation"):
		return GeminiFileAction(attrs)
	}

	// Ordered substring fallback over the joined text. First rule with a matching keyword wins.
	for _, rule := range textKeywordActionRules {
		for _, keyword := range rule.keywords {
			if strings.Contains(text, keyword) {
				return rule.action
			}
		}
	}
	return ActionToolInvoked
}

// textKeywordActionRules is the ordered substring-match fallback for InferAction, applied
// after the structured matchers above. Order matters: earlier rules take precedence.
var textKeywordActionRules = []struct {
	keywords []string
	action   string
}{
	{[]string{"approval_mode_switch", "approval_mode_duration", "plan_execution"}, ActionApprovalRequested},
	{[]string{"prompt", "user_input"}, ActionPromptSubmitted},
	{[]string{"mcp"}, ActionMCPToolInvoked},
	{[]string{"command", "shell", "exec"}, ActionCommandExecuted},
	{[]string{"file", "write", "edit"}, ActionFileModified},
	{[]string{"approval"}, ActionApprovalRequested},
}

func HasToolCall(attrs map[string]interface{}) bool {
	if IsMeaningfulValue(attrs["gen_ai.tool.call.id"]) {
		return true
	}
	return IsMeaningfulValue(attrs["gen_ai.tool.call.arguments"])
}

func IsMeaningfulValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		trimmed := strings.TrimSpace(typed)
		return trimmed != "" && trimmed != "<nil>" && trimmed != "{}" && trimmed != "[]" && trimmed != "null"
	case map[string]interface{}:
		for _, item := range typed {
			if IsMeaningfulValue(item) {
				return true
			}
		}
		return false
	case []interface{}:
		for _, item := range typed {
			if IsMeaningfulValue(item) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func IsFileURI(value string) bool {
	return FilePathFromURI(value) != ""
}

func FilePathFromURI(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "//" + parsed.Host + parsed.Path
	}
	return parsed.Path
}

func HasPromptLikeContent(attrs map[string]interface{}) bool {
	if FirstTextAttr(attrs, "gen_ai.prompt", "prompt", "user_prompt", "input.prompt", "copilot_chat.user_request") != "" {
		return true
	}
	if v, ok := AnyAttr(attrs, "gen_ai.input.messages"); ok && firstTextFromAny(v) != "" {
		return true
	}
	return len(LegacyMessages(attrs, "gen_ai.prompt.", "user")) > 0
}

func CopilotAction(attrs map[string]interface{}, operation, text string) string {
	if eventName := FirstString(attrs, "event.name", "name"); eventName != "" {
		switch eventName {
		case "copilot_chat.session.start", "copilot_chat.cloud.session.invoke":
			return ActionSessionActivity
		case "copilot_chat.tool.call":
			if strings.EqualFold(FirstString(attrs, "success"), "false") || FirstString(attrs, "error.type") != "" {
				return ActionToolFailed
			}
			return ActionToolInvoked
		case "copilot_chat.edit.feedback", "copilot_chat.edit.hunk.action", "copilot_chat.inline.done":
			return ActionFileModified
		}
	}
	switch {
	case operation == "invoke_agent" && FirstString(attrs, "copilot_chat.user_request") != "":
		return ActionPromptSubmitted
	case operation == "invoke_agent":
		return ActionSessionActivity
	case operation == "execute_hook":
		return ActionApprovalRequested
	case operation == "chat":
		initiator := strings.ToLower(FirstString(attrs, "github.copilot.initiator"))
		turnID := FirstString(attrs, "github.copilot.turn_id")
		if initiator == "agent" || (turnID != "" && turnID != "0") {
			return ActionSessionActivity
		}
		return ActionPromptSubmitted
	case operation == "execute_tool":
		return ActionToolInvoked
	case strings.Contains(text, "permission"):
		return ActionApprovalRequested
	default:
		return ActionToolInvoked
	}
}

func GeminiToolAction(attrs map[string]interface{}) string {
	if FirstString(attrs, "tool_type") == "mcp" || FirstString(attrs, "mcp_server_name") != "" {
		return ActionMCPToolInvoked
	}
	return ActionToolInvoked
}

func GeminiFileAction(attrs map[string]interface{}) string {
	switch strings.ToLower(FirstString(attrs, "operation")) {
	case "read":
		return ActionFileRead
	case "create":
		return ActionFileCreated
	default:
		return ActionFileModified
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
