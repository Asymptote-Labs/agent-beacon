package copilotsession

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type mapper struct {
	ref     SessionRef
	opts    MapOptions
	out     []MappedEvent
	calls   map[string]toolCall
	session sessionContext
	model   string

	lastUsage             map[string]usageTotals
	outputSinceLastTotals map[string]int64
}

type sessionContext struct {
	ID         string
	Workspace  string
	Repository string
	Branch     string
	Title      string
	Version    string
}

type toolCall struct {
	ID        string
	Name      string
	Arguments map[string]interface{}
}

type toolResult struct {
	CallID    string
	Name      string
	Text      string
	Success   bool
	ErrorText string
	Raw       map[string]interface{}
}

type usageTotals struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
}

func MapSession(ref SessionRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{
		ref:                   ref,
		opts:                  opts,
		calls:                 map[string]toolCall{},
		session:               sessionFromRef(ref),
		lastUsage:             map[string]usageTotals{},
		outputSinceLastTotals: map[string]int64{},
	}
	for _, record := range records {
		if record.Line <= opts.MinLine {
			m.consumeContext(record)
			continue
		}
		m.consume(record)
	}
	return m.out
}

func sessionFromRef(ref SessionRef) sessionContext {
	ctx := sessionContext{ID: ref.ID}
	if ref.Meta != nil {
		ctx.ID = firstNonEmpty(ref.Meta.ID, ctx.ID)
		ctx.Workspace = firstNonEmpty(ref.Meta.CWD, ref.Meta.GitRoot)
		ctx.Repository = ref.Meta.Repository
		ctx.Branch = ref.Meta.Branch
		ctx.Title = ref.Meta.Name
	}
	return ctx
}

func (m *mapper) consumeContext(record Record) {
	switch record.Type {
	case "session.start", "session.resume":
		m.applySessionData(record.Data)
	case "session.model_change":
		if model := firstString(record.Data, "newModel", "selectedModel"); model != "" {
			m.model = asymptoteobserve.NormalizeModelName(model)
		}
	case "assistant.message":
		if model := firstString(record.Data, "model"); model != "" {
			m.model = asymptoteobserve.NormalizeModelName(model)
		}
		m.rememberAssistantOutput(record)
		for _, call := range toolRequests(record.Data) {
			if call.ID != "" {
				m.calls[call.ID] = call
			}
		}
	case "tool.execution_start":
		call := parseToolCall(record)
		if call.ID != "" {
			m.calls[call.ID] = call
		}
	case "session.shutdown":
		m.rememberShutdownTotals(record)
	}
}

func (m *mapper) consume(record Record) {
	switch record.Type {
	case "session.start":
		m.consumeContext(record)
		if !m.opts.SkipSessionStarted {
			m.emitSessionStarted(record)
		}
	case "session.resume":
		m.consumeContext(record)
		m.emitSessionContext(record, "resume", "GitHub Copilot CLI session resumed")
	case "session.model_change":
		m.consumeContext(record)
		m.emitSessionContext(record, "model_change", "GitHub Copilot CLI model changed")
	case "user.message":
		m.emitPrompt(record)
	case "assistant.message":
		m.emitAssistant(record)
	case "tool.execution_start":
		m.emitToolCall(record)
	case "tool.execution_complete":
		m.emitToolResult(record)
	case "session.shutdown":
		m.emitShutdownUsage(record)
	case "abort":
		m.emitAbort(record)
	}
}

func (m *mapper) applySessionData(data map[string]interface{}) {
	if id := firstString(data, "sessionId", "session_id", "id"); id != "" {
		m.session.ID = id
	}
	if version := firstString(data, "copilotVersion", "version"); version != "" {
		m.session.Version = version
	}
	if model := firstString(data, "selectedModel", "currentModel"); model != "" {
		m.model = asymptoteobserve.NormalizeModelName(model)
	}
	ctx := firstMap(data, "context")
	if cwd := firstString(ctx, "cwd", "workingDirectory", "git_root"); cwd != "" {
		m.session.Workspace = cwd
	}
	if repo := firstString(ctx, "repository"); repo != "" {
		m.session.Repository = repo
	}
	if branch := firstString(ctx, "branch"); branch != "" {
		m.session.Branch = branch
	}
}

func (m *mapper) base(record Record, action, category string, severity schema.Severity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			Version:          m.session.Version,
			CollectionMethod: schema.CollectionMethodPoll,
		},
		Origin:   schema.OriginLocal,
		Fidelity: schema.FidelityObserved,
		Message:  message,
	})
	if ts := timestampMS(record.Time, m.ref.ModTimeUnixMS); ts > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(ts))
	}
	ev.Session = &schema.SessionInfo{ID: firstNonEmpty(m.session.ID, m.ref.ID), WorkingDirectory: m.session.Workspace}
	if model := firstNonEmpty(m.model); model != "" {
		ev.Model = model
	}
	return ev
}

func (m *mapper) emitSessionStarted(record Record) {
	m.applySessionData(record.Data)
	ev := m.base(record, "session.started", "session", schema.SeverityInfo, "GitHub Copilot CLI session started")
	raw := map[string]interface{}{
		"source_path": recordSourcePath(m.ref),
		"line":        record.Line,
	}
	if m.session.Repository != "" {
		raw["repository"] = m.session.Repository
	}
	if m.session.Branch != "" {
		raw["branch"] = m.session.Branch
	}
	ev.Raw = map[string]interface{}{"copilot": raw}
	m.append(record, "session.started", ev)
}

func (m *mapper) emitSessionContext(record Record, change, message string) {
	ev := m.base(record, "session.context", "session", schema.SeverityInfo, message)
	raw := map[string]interface{}{"change": change, "source_path": recordSourcePath(m.ref), "line": record.Line}
	if model := firstString(record.Data, "newModel", "selectedModel", "currentModel"); model != "" {
		raw["model"] = model
	}
	if effort := firstString(record.Data, "reasoningEffort"); effort != "" {
		raw["reasoning_effort"] = effort
	}
	ev.Raw = map[string]interface{}{"copilot": raw}
	m.append(record, change, ev)
}

func (m *mapper) emitPrompt(record Record) {
	text := firstStringRaw(record.Data, "content", "transformedContent")
	if strings.TrimSpace(text) == "" {
		return
	}
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, "GitHub Copilot CLI prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Input = &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}
	})
	ev.Raw = m.raw(record, nil)
	m.append(record, "prompt.submitted", ev)
	if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
		link := m.base(record, "session.handoff", "session", schema.SeverityInfo, "Session continued from a "+info.SourceHarness+" session")
		link.Handoff = &info
		m.append(record, "prompt.submitted.handoff", link)
	}
}

func (m *mapper) emitAssistant(record Record) {
	if model := firstString(record.Data, "model"); model != "" {
		m.model = asymptoteobserve.NormalizeModelName(model)
	}
	m.rememberAssistantOutput(record)
	for _, call := range toolRequests(record.Data) {
		if call.ID != "" {
			m.calls[call.ID] = call
		}
	}
	if reasoning := firstStringRaw(record.Data, "reasoningText"); strings.TrimSpace(reasoning) != "" {
		ev := m.base(record, "agent.reasoning", "agent", schema.SeverityInfo, "GitHub Copilot CLI assistant reasoning")
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Output = &schema.GenAIOutputInfo{Messages: []map[string]interface{}{{
				"role":    "assistant",
				"content": []map[string]string{{"type": "reasoning", "text": reasoning}},
			}}}
		})
		ev.Content = asymptoteobserve.RetainedContent(reasoning, asymptoteobserve.DefaultRawStringLimit)
		ev.Raw = m.raw(record, nil)
		m.append(record, "assistant.reasoning", ev)
	}
	if text := firstStringRaw(record.Data, "content"); strings.TrimSpace(text) != "" {
		ev := m.base(record, "agent.message", "agent", schema.SeverityInfo, "GitHub Copilot CLI assistant message")
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}
			if id := firstString(record.Data, "messageId", "requestId", "serviceRequestId"); id != "" {
				genAI.Response = &schema.GenAIResponseInfo{ID: id, Model: firstNonEmpty(m.model, firstString(record.Data, "model"))}
			}
		})
		ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
		ev.Raw = m.raw(record, nil)
		m.append(record, "assistant.message", ev)
	}
	m.emitAssistantUsage(record)
}

func (m *mapper) emitToolCall(record Record) {
	call := parseToolCall(record)
	if call.ID != "" {
		m.calls[call.ID] = call
	}
	if call.Name == "" {
		return
	}
	ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, "GitHub Copilot CLI tool invoked")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	applyCall(&ev, call)
	ev.Raw = m.raw(record, nil)
	m.append(record, "tool.call", ev)
}

func (m *mapper) emitToolResult(record Record) {
	result := parseToolResult(record)
	call := m.calls[result.CallID]
	if result.Name != "" {
		call.Name = result.Name
	}
	if call.ID == "" {
		call.ID = result.CallID
	}
	if call.Name == "" {
		return
	}
	action, category, severity, message := classifyTool(call, result)
	ev := m.base(record, action, category, severity, message)
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	applyCall(&ev, call)
	switch action {
	case "command.executed":
		applyCommand(&ev, call, result)
	case "file.read", "file.created", "file.modified":
		applyFile(&ev, call, result, action)
	case "mcp.tool_invoked":
		ev.MCP = mcpInfo(call.Name)
	}
	if !result.Success {
		ev.Error = &schema.ErrorInfo{Type: "tool_execution_failed"}
	}
	ev.Raw = m.raw(record, map[string]interface{}{"tool_result": result.Raw})
	m.append(record, "tool.result", ev)
}

func (m *mapper) emitAbort(record Record) {
	ev := m.base(record, "agent.error", "session", schema.SeverityHigh, "GitHub Copilot CLI request interrupted")
	ev.Error = &schema.ErrorInfo{Type: firstNonEmpty(firstString(record.Data, "reason"), "aborted")}
	ev.Raw = m.raw(record, nil)
	m.append(record, "abort", ev)
}

func (m *mapper) emitAssistantUsage(record Record) {
	output, ok := int64Value(record.Data["outputTokens"])
	if !ok || output <= 0 {
		return
	}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, "GitHub Copilot CLI assistant output token usage")
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Usage = &schema.GenAIUsageInfo{OutputTokens: &output}
	})
	ev.Raw = m.raw(record, map[string]interface{}{"token_source": "assistant_message_output_tokens"})
	m.append(record, "assistant.usage", ev)
}

func (m *mapper) rememberAssistantOutput(record Record) {
	output, ok := int64Value(record.Data["outputTokens"])
	if !ok || output <= 0 {
		return
	}
	model := firstNonEmpty(asymptoteobserve.NormalizeModelName(firstString(record.Data, "model")), m.model, "_unknown")
	m.outputSinceLastTotals[model] += output
}

func (m *mapper) rememberShutdownTotals(record Record) {
	for model, total := range shutdownUsage(record.Data) {
		m.lastUsage[model] = total
		m.outputSinceLastTotals[model] = 0
	}
}

func (m *mapper) emitShutdownUsage(record Record) {
	models := shutdownUsage(record.Data)
	for model, current := range models {
		previous := m.lastUsage[model]
		outputAlreadyReported := m.outputSinceLastTotals[model]
		usage := &schema.GenAIUsageInfo{}
		if n := delta(current.Input, previous.Input); n > 0 {
			usage.InputTokens = &n
		}
		if n := delta(current.Output, previous.Output) - outputAlreadyReported; n > 0 {
			usage.OutputTokens = &n
		}
		if n := delta(current.CacheRead, previous.CacheRead); n > 0 {
			usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &n}
		}
		if n := delta(current.CacheWrite, previous.CacheWrite); n > 0 {
			usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &n}
		}
		if n := delta(current.Reasoning, previous.Reasoning); n > 0 {
			usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &n}
		}
		m.lastUsage[model] = current
		m.outputSinceLastTotals[model] = 0
		if emptyUsage(usage) {
			continue
		}
		ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, "GitHub Copilot CLI shutdown token usage")
		ev.Model = model
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) { genAI.Usage = usage })
		ev.Raw = m.raw(record, map[string]interface{}{
			"token_source":                      "session_shutdown_model_metrics_delta",
			"output_tokens_already_reported":    outputAlreadyReported,
			"cumulative_total_premium_requests": numberValue(record.Data["totalPremiumRequests"]),
			"cumulative_total_nano_aiu":         numberValue(record.Data["totalNanoAiu"]),
		})
		m.append(record, "shutdown.usage."+model, ev)
	}
}

func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	dedupID := fmt.Sprintf("%s:%s:%d:%s", recordSourcePath(m.ref), firstNonEmpty(m.session.ID, m.ref.ID), record.Line, suffix)
	ev.Event.ID = copilotEventID(dedupID)
	m.out = append(m.out, MappedEvent{SourceLine: record.Line, DedupID: dedupID, Event: ev})
}

func parseToolCall(record Record) toolCall {
	return toolCall{
		ID:        firstString(record.Data, "toolCallId", "tool_call_id", "id"),
		Name:      firstString(record.Data, "toolName", "tool_name", "name"),
		Arguments: argumentsFrom(record.Data["arguments"]),
	}
}

func toolRequests(data map[string]interface{}) []toolCall {
	items, _ := data["toolRequests"].([]interface{})
	out := make([]toolCall, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]interface{})
		if len(m) == 0 {
			continue
		}
		out = append(out, toolCall{
			ID:        firstString(m, "toolCallId", "tool_call_id", "id"),
			Name:      firstString(m, "name", "toolName", "tool_name"),
			Arguments: argumentsFrom(m["arguments"]),
		})
	}
	return out
}

func parseToolResult(record Record) toolResult {
	success := true
	if value, ok := record.Data["success"].(bool); ok {
		success = value
	}
	result := firstMap(record.Data, "result")
	errObj := firstMap(record.Data, "error")
	text := firstNonEmpty(firstStringRaw(result, "content", "detailedContent"), stringifyOutput(result))
	errText := firstNonEmpty(firstStringRaw(errObj, "message"), firstStringRaw(record.Data, "message"))
	if !success && errText != "" {
		text = errText
	}
	return toolResult{
		CallID:    firstString(record.Data, "toolCallId", "tool_call_id", "id"),
		Name:      firstString(record.Data, "toolName", "tool_name", "name"),
		Text:      text,
		Success:   success,
		ErrorText: errText,
		Raw:       record.Data,
	}
}

func classifyTool(call toolCall, result toolResult) (action, category string, severity schema.Severity, message string) {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	switch {
	case strings.HasPrefix(name, "mcp__"):
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "GitHub Copilot CLI MCP tool invoked"
	case name == "bash":
		return "command.executed", "command", schema.SeverityInfo, "GitHub Copilot CLI command executed"
	case isFileReadTool(name):
		return "file.read", "file", schema.SeverityInfo, "GitHub Copilot CLI file read"
	case isFileCreateTool(name):
		return "file.created", "file", schema.SeverityInfo, "GitHub Copilot CLI file created"
	case isFileModifyTool(name):
		return "file.modified", "file", schema.SeverityInfo, "GitHub Copilot CLI file modified"
	case !result.Success:
		return "tool.failed", "tool", schema.SeverityHigh, "GitHub Copilot CLI tool failed"
	default:
		return "tool.completed", "tool", schema.SeverityInfo, "GitHub Copilot CLI tool completed"
	}
}

func isFileReadTool(name string) bool {
	switch name {
	case "view", "read", "read_file", "glob", "rg", "grep", "search":
		return true
	default:
		return false
	}
}

func isFileCreateTool(name string) bool {
	switch name {
	case "create", "write":
		return true
	default:
		return false
	}
}

func isFileModifyTool(name string) bool {
	switch name {
	case "edit", "str_replace_editor", "replace", "patch":
		return true
	default:
		return false
	}
}

func applyCall(ev *schema.Event, call toolCall) {
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
		genAI.Tool = &schema.GenAIToolInfo{
			Name: call.Name,
			Call: &schema.GenAIToolCallInfo{
				ID:        call.ID,
				Arguments: call.Arguments,
			},
		}
	})
	if path := pathFromCall(call); path != "" {
		ev.Tool.Path = path
	}
	if command := firstString(call.Arguments, "command", "cmd"); command != "" {
		ev.Tool.Command = command
	}
}

func applyCommand(ev *schema.Event, call toolCall, result toolResult) {
	info := &schema.CommandInfo{Command: firstString(call.Arguments, "command", "cmd")}
	if result.Text != "" {
		info.Output = result.Text
		ev.Content = asymptoteobserve.RetainedContent(result.Text, asymptoteobserve.DefaultStringLimit)
	}
	ev.Command = info
}

func applyFile(ev *schema.Event, call toolCall, result toolResult, action string) {
	path := pathFromCall(call)
	info := &schema.FileInfo{Path: path}
	switch action {
	case "file.created":
		info.Operation = "create"
	case "file.modified":
		info.Operation = "modify"
	default:
		info.Operation = "read"
	}
	if path != "" {
		info.Language = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	if diff := diffFromCall(call, path); diff != "" && action != "file.read" {
		info.Diff = diff
		info.DiffBytes = len(diff)
		sum := sha256.Sum256([]byte(diff))
		info.DiffHash = hex.EncodeToString(sum[:])
		ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
	} else if result.Text != "" {
		ev.Content = asymptoteobserve.RetainedContent(result.Text, asymptoteobserve.DefaultStringLimit)
	}
	ev.File = info
}

func pathFromCall(call toolCall) string {
	if path := firstString(call.Arguments, "path", "file_path", "target_path", "absolute_path"); path != "" {
		return path
	}
	if paths, ok := call.Arguments["paths"].([]interface{}); ok && len(paths) > 0 {
		if path, ok := paths[0].(string); ok {
			return path
		}
	}
	if paths := firstString(call.Arguments, "paths"); paths != "" {
		return paths
	}
	return ""
}

func diffFromCall(call toolCall, path string) string {
	content := firstStringRaw(call.Arguments, "content", "file_text")
	if isFileCreateTool(strings.ToLower(call.Name)) && path != "" && content != "" {
		return "--- /dev/null\n+++ " + path + "\n+" + strings.ReplaceAll(content, "\n", "\n+") + "\n"
	}
	oldText := firstStringRaw(call.Arguments, "old_string", "old_str")
	newText := firstStringRaw(call.Arguments, "new_string", "new_str", "insert")
	if oldText != "" || newText != "" {
		return "-" + strings.ReplaceAll(oldText, "\n", "\n-") + "\n+" + strings.ReplaceAll(newText, "\n", "\n+") + "\n"
	}
	return ""
}

func mcpInfo(name string) *schema.MCPInfo {
	trimmed := strings.TrimPrefix(name, "mcp__")
	parts := strings.SplitN(trimmed, "__", 2)
	if len(parts) == 2 {
		return &schema.MCPInfo{Server: parts[0], Tool: parts[1]}
	}
	return &schema.MCPInfo{Tool: name}
}

func shutdownUsage(data map[string]interface{}) map[string]usageTotals {
	metrics := firstMap(data, "modelMetrics")
	out := map[string]usageTotals{}
	for model, value := range metrics {
		modelData, _ := value.(map[string]interface{})
		usageData := firstMap(modelData, "usage")
		if len(usageData) == 0 {
			continue
		}
		out[asymptoteobserve.NormalizeModelName(model)] = usageTotals{
			Input:      int64OrZero(firstPresent(usageData, "inputTokens", "input_tokens")),
			Output:     int64OrZero(firstPresent(usageData, "outputTokens", "output_tokens")),
			CacheRead:  int64OrZero(firstPresent(usageData, "cacheReadTokens", "cache_read_tokens")),
			CacheWrite: int64OrZero(firstPresent(usageData, "cacheWriteTokens", "cache_write_tokens")),
			Reasoning:  int64OrZero(firstPresent(usageData, "reasoningTokens", "reasoning_tokens")),
		}
	}
	return out
}

func emptyUsage(usage *schema.GenAIUsageInfo) bool {
	return usage.InputTokens == nil && usage.OutputTokens == nil && usage.CacheRead == nil && usage.CacheCreation == nil && usage.Reasoning == nil && usage.CostUSD == nil
}

func delta(current, previous int64) int64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

func (m *mapper) raw(record Record, extra map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{
		"source_path": recordSourcePath(m.ref),
		"line":        record.Line,
		"type":        record.Type,
	}
	if record.ID != "" {
		raw["record_id"] = record.ID
	}
	for k, v := range extra {
		raw[k] = v
	}
	return map[string]interface{}{"copilot": raw}
}

func withGenAI(genAI *schema.GenAIInfo, edit func(*schema.GenAIInfo)) *schema.GenAIInfo {
	if genAI == nil {
		genAI = &schema.GenAIInfo{}
	}
	edit(genAI)
	return genAI
}

func argumentsFrom(value interface{}) map[string]interface{} {
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	if text, ok := value.(string); ok {
		var args map[string]interface{}
		if json.Unmarshal([]byte(text), &args) == nil {
			return args
		}
		return map[string]interface{}{"raw": text}
	}
	return map[string]interface{}{}
}

func firstMap(data map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		if child, ok := data[key].(map[string]interface{}); ok {
			return child
		}
	}
	return nil
}

func firstPresent(data map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}

func firstString(data map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			if text := strings.TrimSpace(textValue(value)); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstStringRaw(data map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			if text := textValue(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func textValue(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := textValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		return firstNonEmpty(firstString(v, "text"), firstString(v, "content"), firstString(v, "value"))
	default:
		return fmt.Sprint(v)
	}
}

func stringifyOutput(value interface{}) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case map[string]interface{}:
		if len(v) == 0 {
			return ""
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func int64Value(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), v >= 0
	case int64:
		return v, v >= 0
	case int:
		return int64(v), v >= 0
	case json.Number:
		n, err := v.Int64()
		return n, err == nil && n >= 0
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n, err == nil && n >= 0
	default:
		return 0, false
	}
}

func int64OrZero(value interface{}) int64 {
	n, _ := int64Value(value)
	return n
}

func numberValue(value interface{}) interface{} {
	switch v := value.(type) {
	case nil:
		return nil
	case float64, int64, int, json.Number:
		return v
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
	}
	return nil
}

func timestampMS(value interface{}, fallback int64) int64 {
	switch v := value.(type) {
	case float64:
		if v < 1000000000000 {
			return int64(v * 1000)
		}
		return int64(v)
	case int64:
		if v < 1000000000000 {
			return v * 1000
		}
		return v
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			if n < 1000000000000 {
				return n * 1000
			}
			return n
		}
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return ts.UnixMilli()
		}
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			return ts.UnixMilli()
		}
	}
	return fallback
}

func recordSourcePath(ref SessionRef) string {
	if ref.Path != "" {
		return ref.Path
	}
	return filepath.Join(ref.Dir, EventsFile)
}

var copilotIDNamespace = [16]byte{
	0x91, 0x35, 0x6c, 0x20, 0x2f, 0x43, 0x4f, 0xb8,
	0xa7, 0x8e, 0xb2, 0x59, 0x15, 0x2c, 0x71, 0x09,
}

func copilotEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(copilotIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
