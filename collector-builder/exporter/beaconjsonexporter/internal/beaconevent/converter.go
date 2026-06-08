package beaconevent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
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
				events = append(events, c.EventFromMetric(resourceAttrs, metric))
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
	populateRunContext(event, attrs)
	event.GenAI = GenAIFromAttrs(attrs)
	event.Model = FirstString(attrs, "gen_ai.request.model", "gen_ai.response.model", "model", "ai.model")
	event.Repository = FirstString(attrs, "vcs.repository.url", "repository", "repo.path", "workspace.repository")
	event.Branch = FirstString(attrs, "vcs.branch.name", "git.branch", "branch")
	if id := FirstString(attrs, "gen_ai.conversation.id", "copilot_chat.session_id", "copilot_chat.chat_session_id", "conversation.id", "conversation_id", "session.id"); id != "" || FirstString(attrs, "cwd", "working_directory", "workspace") != "" {
		event.Session = &SessionInfo{
			ID:               id,
			WorkingDirectory: FirstString(attrs, "cwd", "working_directory", "process.command_args.cwd", "workspace"),
		}
	}
	if name := FirstString(attrs, "tool.name", "gen_ai.tool.name", "mcp.tool.name", "function_name", "tool_name"); name != "" || ToolCommandString(attrs) != "" {
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
		if path != "" && operation == "" && event.Event.Action == "file.read" {
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

func GenAIFromAttrs(attrs map[string]interface{}) *GenAIInfo {
	genai := &GenAIInfo{}
	if description := FirstString(attrs, "gen_ai.agent.description"); description != "" || FirstString(attrs, "gen_ai.agent.id", "gen_ai.agent.name", "gen_ai.agent.version") != "" {
		genai.Agent = &GenAIAgentInfo{
			Description: description,
			ID:          FirstString(attrs, "gen_ai.agent.id"),
			Name:        FirstString(attrs, "gen_ai.agent.name"),
			Version:     FirstString(attrs, "gen_ai.agent.version"),
		}
	}
	if id := FirstString(attrs, "gen_ai.conversation.id"); id != "" {
		genai.Conversation = &GenAIConversationInfo{ID: id}
	}
	if id := FirstString(attrs, "gen_ai.data_source.id"); id != "" {
		genai.DataSource = &GenAIDataSourceInfo{ID: id}
	}
	if count, ok := IntAttr(attrs, "gen_ai.embeddings.dimension.count"); ok {
		genai.Embeddings = &GenAIEmbeddingsInfo{DimensionCount: &count}
	}
	if explanation := FirstString(attrs, "gen_ai.evaluation.explanation"); explanation != "" || FirstString(attrs, "gen_ai.evaluation.name", "gen_ai.evaluation.score.label") != "" || HasAttr(attrs, "gen_ai.evaluation.score.value") {
		genai.Evaluation = &GenAIEvaluationInfo{
			Explanation: explanation,
			Name:        FirstString(attrs, "gen_ai.evaluation.name"),
		}
		if label := FirstString(attrs, "gen_ai.evaluation.score.label"); label != "" || HasAttr(attrs, "gen_ai.evaluation.score.value") {
			genai.Evaluation.Score = &GenAIEvaluationScoreInfo{Label: label}
			if value, ok := FloatAttr(attrs, "gen_ai.evaluation.score.value"); ok {
				genai.Evaluation.Score.Value = &value
			}
		}
	}
	if messages, ok := AnyAttr(attrs, "gen_ai.input.messages"); ok {
		genai.Input = &GenAIInputInfo{Messages: messages}
	} else if messages := LegacyMessages(attrs, "gen_ai.prompt.", "user"); len(messages) > 0 {
		genai.Input = &GenAIInputInfo{Messages: messages}
	} else if messages, ok := AnyAttr(attrs, "llm.prompts", "gen_ai.prompts"); ok {
		genai.Input = &GenAIInputInfo{Messages: messages}
	}
	if name := FirstString(attrs, "gen_ai.operation.name"); name != "" {
		genai.Operation = &GenAIOperationInfo{Name: name}
	}
	if messages, ok := AnyAttr(attrs, "gen_ai.output.messages"); ok {
		genai.Output = &GenAIOutputInfo{Messages: messages, Type: FirstString(attrs, "gen_ai.output.type")}
	} else if messages := LegacyMessages(attrs, "gen_ai.completion.", "assistant"); len(messages) > 0 {
		genai.Output = &GenAIOutputInfo{Messages: messages, Type: FirstString(attrs, "gen_ai.output.type")}
	} else if messages, ok := AnyAttr(attrs, "llm.completions", "gen_ai.completions"); ok {
		genai.Output = &GenAIOutputInfo{Messages: messages, Type: FirstString(attrs, "gen_ai.output.type")}
	} else if outputType := FirstString(attrs, "gen_ai.output.type"); outputType != "" {
		genai.Output = &GenAIOutputInfo{Type: outputType}
	}
	if name := FirstString(attrs, "gen_ai.prompt.name"); name != "" {
		genai.Prompt = &GenAIPromptInfo{Name: name}
	}
	if name := FirstString(attrs, "gen_ai.provider.name", "gen_ai.system"); name != "" {
		genai.Provider = &GenAIProviderInfo{Name: name}
	}
	if request := GenAIRequestFromAttrs(attrs); request != nil {
		genai.Request = request
	}
	if response := GenAIResponseFromAttrs(attrs); response != nil {
		genai.Response = response
	}
	if documents, ok := AnyAttr(attrs, "gen_ai.retrieval.documents"); ok {
		genai.Retrieval = &GenAIRetrievalInfo{Documents: documents, QueryText: FirstString(attrs, "gen_ai.retrieval.query.text")}
	} else if query := FirstString(attrs, "gen_ai.retrieval.query.text"); query != "" {
		genai.Retrieval = &GenAIRetrievalInfo{QueryText: query}
	}
	if instructions, ok := AnyAttr(attrs, "gen_ai.system_instructions"); ok {
		genai.SystemInstructions = instructions
	}
	if tokenType := FirstString(attrs, "gen_ai.token.type"); tokenType != "" {
		genai.Token = &GenAITokenInfo{Type: tokenType}
	}
	if tool := GenAIToolFromAttrs(attrs); tool != nil {
		genai.Tool = tool
	}
	if usage := GenAIUsageFromAttrs(attrs); usage != nil {
		genai.Usage = usage
	}
	if name := FirstString(attrs, "gen_ai.workflow.name"); name != "" {
		genai.Workflow = &GenAIWorkflowInfo{Name: name}
	}
	if IsZeroJSON(genai) {
		return nil
	}
	return genai
}

func GenAIRequestFromAttrs(attrs map[string]interface{}) *GenAIRequestInfo {
	request := &GenAIRequestInfo{
		Model:           FirstString(attrs, "gen_ai.request.model", "llm.request.model"),
		EncodingFormats: StringSliceAttr(attrs, "gen_ai.request.encoding_formats"),
		StopSequences:   StringSliceAttr(attrs, "gen_ai.request.stop_sequences"),
	}
	if value, ok := IntAttr(attrs, "gen_ai.request.choice.count"); ok {
		request.ChoiceCount = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.frequency_penalty"); ok {
		request.FrequencyPenalty = &value
	}
	if value, ok := IntAttr(attrs, "gen_ai.request.max_tokens", "llm.request.max_tokens"); ok {
		request.MaxTokens = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.presence_penalty"); ok {
		request.PresencePenalty = &value
	}
	if value, ok := IntAttr(attrs, "gen_ai.request.seed"); ok {
		request.Seed = &value
	}
	if value, ok := BoolAttr(attrs, "gen_ai.request.stream"); ok {
		request.Stream = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.temperature", "llm.request.temperature"); ok {
		request.Temperature = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.top_k"); ok {
		request.TopK = &value
	}
	if value, ok := FloatAttr(attrs, "gen_ai.request.top_p"); ok {
		request.TopP = &value
	}
	if IsZeroJSON(request) {
		return nil
	}
	return request
}

func GenAIResponseFromAttrs(attrs map[string]interface{}) *GenAIResponseInfo {
	response := &GenAIResponseInfo{
		FinishReasons: StringSliceAttr(attrs, "gen_ai.response.finish_reasons"),
		ID:            FirstString(attrs, "gen_ai.response.id"),
		Model:         FirstString(attrs, "gen_ai.response.model", "llm.response.model"),
	}
	if value, ok := FloatAttr(attrs, "gen_ai.response.time_to_first_chunk"); ok {
		response.TimeToFirstChunk = &value
	}
	if IsZeroJSON(response) {
		return nil
	}
	return response
}

func GenAIToolFromAttrs(attrs map[string]interface{}) *GenAIToolInfo {
	tool := &GenAIToolInfo{
		Description: FirstString(attrs, "gen_ai.tool.description"),
		Name:        FirstString(attrs, "gen_ai.tool.name", "tool.name"),
		Type:        FirstString(attrs, "gen_ai.tool.type"),
	}
	if definitions, ok := AnyAttr(attrs, "gen_ai.tool.definitions"); ok {
		tool.Definitions = definitions
	}
	if args, ok := AnyAttr(attrs, "gen_ai.tool.call.arguments", "function_args", "arguments"); ok {
		tool.Call = &GenAIToolCallInfo{Arguments: args, ID: FirstString(attrs, "gen_ai.tool.call.id")}
	} else if id := FirstString(attrs, "gen_ai.tool.call.id"); id != "" {
		tool.Call = &GenAIToolCallInfo{ID: id}
	}
	if result, ok := AnyAttr(attrs, "gen_ai.tool.call.result"); ok {
		if tool.Call == nil {
			tool.Call = &GenAIToolCallInfo{}
		}
		tool.Call.Result = result
	}
	if IsZeroJSON(tool) {
		return nil
	}
	return tool
}

func GenAIUsageFromAttrs(attrs map[string]interface{}) *GenAIUsageInfo {
	usage := &GenAIUsageInfo{}
	if value, ok := IntAttr(attrs, "gen_ai.usage.cache_creation.input_tokens"); ok {
		usage.CacheCreation = &GenAIUsageCacheCreationInfo{InputTokens: &value}
	}
	if value, ok := IntAttr(attrs, "gen_ai.usage.cache_read.input_tokens"); ok {
		usage.CacheRead = &GenAIUsageCacheReadInfo{InputTokens: &value}
	}
	if value, ok := IntAttr(attrs, "gen_ai.usage.input_tokens", "llm.usage.prompt_tokens", "gen_ai.usage.prompt_tokens"); ok {
		usage.InputTokens = &value
	}
	if value, ok := IntAttr(attrs, "gen_ai.usage.output_tokens", "llm.usage.completion_tokens", "gen_ai.usage.completion_tokens"); ok {
		usage.OutputTokens = &value
	}
	if value, ok := IntAttr(attrs, "gen_ai.usage.reasoning.output_tokens"); ok {
		usage.Reasoning = &GenAIUsageReasoningInfo{OutputTokens: &value}
	}
	if IsZeroJSON(usage) {
		return nil
	}
	return usage
}

func MCPFromAttrs(attrs map[string]interface{}) *MCPInfo {
	server := FirstString(attrs, "mcp.server.name", "mcp.server", "gen_ai.mcp.server", "mcp_server_name")
	tool := FirstString(attrs, "mcp.tool.name", "tool.name", "function_name")
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
	switch FirstString(attrs, asymptoteobserve.AttributeOrigin) {
	case string(asymptoteobserve.OriginLocal):
		event.Origin = asymptoteobserve.OriginLocal
	case string(asymptoteobserve.OriginCloud):
		event.Origin = asymptoteobserve.OriginCloud
	case string(asymptoteobserve.OriginCI):
		event.Origin = asymptoteobserve.OriginCI
	}
	run := RunInfo{
		Provider:   FirstString(attrs, asymptoteobserve.AttributeRunProvider),
		RunID:      FirstString(attrs, asymptoteobserve.AttributeRunID),
		RunAttempt: FirstString(attrs, asymptoteobserve.AttributeRunAttempt),
		Workflow:   FirstString(attrs, asymptoteobserve.AttributeRunWorkflow),
		Job:        FirstString(attrs, asymptoteobserve.AttributeRunJob),
		EventName:  FirstString(attrs, asymptoteobserve.AttributeRunEventName),
		Commit:     FirstString(attrs, asymptoteobserve.AttributeRunCommit),
		Repository: FirstString(attrs, asymptoteobserve.AttributeRunRepository),
		Branch:     FirstString(attrs, asymptoteobserve.AttributeRunBranch),
		PR:         FirstString(attrs, asymptoteobserve.AttributeRunPR),
		PRNumber:   FirstString(attrs, asymptoteobserve.AttributeRunPRNumber),
		Actor:      FirstString(attrs, asymptoteobserve.AttributeRunActor),
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

func ToolCommandString(attrs map[string]interface{}) string {
	if command := FirstString(attrs, "tool.command", "command", "function_args"); command != "" {
		return command
	}
	return FirstStringAttr(attrs, "gen_ai.tool.call.arguments")
}

func FirstStringAttr(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if str, ok := value.(string); ok {
				if trimmed := strings.TrimSpace(str); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func FirstTextAttr(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if text := firstTextFromAny(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func HasAttr(attrs map[string]interface{}, key string) bool {
	_, ok := attrs[key]
	return ok
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
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case int:
			return typed, true
		case int64:
			if !FitsInInt(typed) {
				continue
			}
			return int(typed), true
		case float64:
			value := int64(typed)
			if !FitsInInt(value) {
				continue
			}
			return int(value), true
		case string:
			value, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func FitsInInt(value int64) bool {
	if strconv.IntSize == 32 {
		return value >= -1<<31 && value <= 1<<31-1
	}
	return true
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
		case string:
			value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func FloatAttr(attrs map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case float32:
			return float64(typed), true
		case float64:
			return typed, true
		case int:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case string:
			value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func BoolAttr(attrs map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case bool:
			return typed, true
		case string:
			value, err := strconv.ParseBool(strings.TrimSpace(typed))
			if err == nil {
				return value, true
			}
		}
	}
	return false, false
}

func StringSliceAttr(attrs map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		value, ok := attrs[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			return typed
		case []interface{}:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if str := strings.TrimSpace(fmt.Sprint(item)); str != "" && str != "<nil>" {
					out = append(out, str)
				}
			}
			if len(out) > 0 {
				return out
			}
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" {
				continue
			}
			var parsed []string
			if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
				return parsed
			}
			if strings.Contains(trimmed, ",") {
				parts := strings.Split(trimmed, ",")
				out := make([]string, 0, len(parts))
				for _, part := range parts {
					if part = strings.TrimSpace(part); part != "" {
						out = append(out, part)
					}
				}
				if len(out) > 0 {
					return out
				}
			}
			return []string{trimmed}
		}
	}
	return nil
}

func AnyAttr(attrs map[string]interface{}, keys ...string) (interface{}, bool) {
	for _, key := range keys {
		value, ok := attrs[key]
		if !ok || value == nil {
			continue
		}
		if str, ok := value.(string); ok {
			trimmed := strings.TrimSpace(str)
			if trimmed == "" {
				continue
			}
			if decoded, ok := DecodeJSONValue(trimmed); ok {
				return decoded, true
			}
			return trimmed, true
		}
		return value, true
	}
	return nil, false
}

func DecodeJSONValue(value string) (interface{}, bool) {
	if value == "" {
		return nil, false
	}
	first := value[0]
	if first != '{' && first != '[' && first != '"' {
		return nil, false
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func LegacyMessages(attrs map[string]interface{}, prefix, role string) []interface{} {
	type messagePart struct {
		index int
		text  string
	}
	var parts []messagePart
	for key, value := range attrs {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".content") {
			continue
		}
		indexText := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".content")
		index, err := strconv.Atoi(indexText)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" || text == "<nil>" {
			continue
		}
		parts = append(parts, messagePart{index: index, text: text})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].index < parts[j].index })
	out := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		out = append(out, map[string]interface{}{
			"role": role,
			"parts": []interface{}{
				map[string]interface{}{"type": "text", "content": part.text},
			},
		})
	}
	return out
}

func FirstMessageText(genai *GenAIInfo) string {
	if genai == nil || genai.Input == nil || genai.Input.Messages == nil {
		return ""
	}
	return firstTextFromAny(genai.Input.Messages)
}

func firstTextFromAny(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return meaningfulText(typed)
	case []interface{}:
		for _, item := range typed {
			if text := firstTextFromAny(item); text != "" {
				return text
			}
		}
	case map[string]interface{}:
		if content, ok := typed["content"]; ok {
			if text := firstTextFromAny(content); text != "" {
				return text
			}
		}
		if parts, ok := typed["parts"]; ok {
			return firstTextFromAny(parts)
		}
		if messages, ok := typed["messages"]; ok {
			return firstTextFromAny(messages)
		}
	}
	return ""
}

func meaningfulText(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "<nil>", "{}", "[]", "null":
		return ""
	default:
		return trimmed
	}
}

func IsZeroJSON(value interface{}) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return string(data) == "{}"
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
	case strings.Contains(lower, "claude_agent_sdk") || strings.Contains(lower, "claude-agent-sdk") || strings.Contains(lower, "claude agent sdk"):
		return "claude_agent_sdk"
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
	case strings.Contains(lower, "copilot-chat"):
		return "vscode_copilot"
	case strings.Contains(lower, "github-copilot") || strings.Contains(lower, "copilot_cli") || strings.Contains(lower, "copilot"):
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
	mcpMethod := strings.ToLower(FirstString(attrs, "mcp.method.name"))
	harness := HarnessName(attrs, fallback)
	text := strings.ToLower(strings.Join([]string{
		fallback,
		tool,
		operation,
		mcpMethod,
		FirstString(attrs, "event.name", "codex.op", "rpc.method"),
	}, " "))
	switch {
	case harness == "copilot_cli" || harness == "vscode_copilot":
		return CopilotAction(attrs, operation, text)
	case mcpMethod == "tools/call":
		return "mcp.tool_invoked"
	case mcpMethod == "resources/read" && IsFileURI(FirstString(attrs, "mcp.resource.uri")):
		return "file.read"
	case operation == "execute_tool":
		return "tool.invoked"
	case (operation == "chat" || operation == "generate_content" || operation == "text_completion") && HasPromptLikeContent(attrs):
		return "prompt.submitted"
	case HasToolCall(attrs):
		return "tool.invoked"
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
			return "session.activity"
		case "copilot_chat.tool.call":
			if strings.EqualFold(FirstString(attrs, "success"), "false") || FirstString(attrs, "error.type") != "" {
				return "tool.failed"
			}
			return "tool.invoked"
		case "copilot_chat.edit.feedback", "copilot_chat.edit.hunk.action", "copilot_chat.inline.done":
			return "file.modified"
		}
	}
	switch {
	case operation == "invoke_agent" && FirstString(attrs, "copilot_chat.user_request") != "":
		return "prompt.submitted"
	case operation == "invoke_agent":
		return "session.activity"
	case operation == "execute_hook":
		return "approval.requested"
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
