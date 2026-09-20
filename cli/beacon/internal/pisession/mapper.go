package pisession

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const Harness = "pi_cli"

var toolCallKinds = map[string]bool{
	"tool_call":     true,
	"tool_use":      true,
	"function_call": true,
	"toolcall":      true,
}

var toolResultKinds = map[string]bool{
	"tool_result":          true,
	"tool_output":          true,
	"function_call_output": true,
	"toolresult":           true,
}

type MappedEvent struct {
	SourceLine int
	Event      schema.Event
}

type MapOptions struct {
	MinLine     int
	SkipStarted bool
}

func MapSession(ref SessionRef, entries []Entry, opts MapOptions) []MappedEvent {
	m := &mapper{
		ref:   ref,
		opts:  opts,
		model: firstNonEmpty(stringValue(ref.Header["model"]), stringValue(ref.Header["modelId"])),
		calls: map[string]toolCallState{},
	}
	m.sessionID = firstNonEmpty(stringValue(ref.Header["id"]), ref.ID)
	m.cwd = stringValue(ref.Header["cwd"])
	if git, ok := ref.Header["git"].(map[string]interface{}); ok {
		m.repository = stringValue(git["repoUrl"])
		m.branch = stringValue(git["branch"])
	}
	if !opts.SkipStarted {
		m.emitSessionStarted()
	}
	for i := range entries {
		m.consume(entries[i])
	}
	return m.out
}

type toolCallState struct {
	ID        string
	Name      string
	Args      map[string]interface{}
	Timestamp time.Time
}

type mapper struct {
	ref  SessionRef
	opts MapOptions
	out  []MappedEvent

	sessionID  string
	cwd        string
	model      string
	repository string
	branch     string
	calls      map[string]toolCallState
}

func (m *mapper) consume(entry Entry) {
	data := entry.Data
	typ := strings.ToLower(stringValue(data["type"]))
	timestamp := timestampValue(firstNonNil(data["timestamp"], refTimestamp(m.ref)))
	emit := entry.Line > m.opts.MinLine

	switch {
	case typ == "session":
		if id := stringValue(data["id"]); id != "" {
			m.sessionID = id
		}
		if cwd := stringValue(data["cwd"]); cwd != "" {
			m.cwd = cwd
		}
	case typ == "model_change":
		if model := firstNonEmpty(stringValue(data["modelId"]), stringValue(data["model"])); model != "" {
			m.model = model
			if emit {
				ev := m.base(timestamp, "session.context", "session", schema.SeverityInfo, "Pi model changed")
				ev.Raw = m.raw(map[string]interface{}{"change": "model_change", "model_id": model})
				m.append(entry.Line, "model_change", ev)
			}
		}
	case typ == "compaction":
		if emit {
			ev := m.base(timestamp, "session.compacting", "session", schema.SeverityInfo, "Pi compacted session history")
			ev.Raw = m.raw(compactionRaw(data))
			m.append(entry.Line, "compaction", ev)
		}
	case typ == "thinking_level_change":
		return
	case typ == "custom":
		m.consumeCustom(entry.Line, data, timestamp, emit)
	case typ == "message":
		message, _ := data["message"].(map[string]interface{})
		if message == nil {
			return
		}
		m.consumeMessage(entry.Line, message, timestamp, emit)
	case toolCallKinds[typ]:
		state := m.toolCallFrom(entry.Line, 0, data, timestamp)
		m.calls[state.ID] = state
		if emit {
			ev := m.base(timestamp, "tool.invoked", "tool", schema.SeverityInfo, "Pi tool invoked")
			m.applyToolCall(&ev, state)
			m.append(entry.Line, "tool_call", ev)
		}
	case toolResultKinds[typ]:
		if emit {
			callID := firstNonEmpty(stringValue(data["toolCallId"]), stringValue(data["callId"]), stringValue(data["id"]), fmt.Sprintf("line-%d", entry.Line))
			m.emitToolResult(entry.Line, timestamp, callID, data)
		}
	case typ == "error":
		if emit {
			m.emitError(entry.Line, timestamp, data)
		}
	}
}

func (m *mapper) consumeCustom(line int, data map[string]interface{}, timestamp time.Time, emit bool) {
	customType := stringValue(data["customType"])
	if customType != "context:skill_loaded" {
		return
	}
	payload, _ := data["data"].(map[string]interface{})
	name := stringValue(payload["name"])
	if !emit || name == "" {
		return
	}
	path := stringValue(payload["path"])
	callID := firstNonEmpty(stringValue(data["id"]), fmt.Sprintf("line-%d-skill", line))
	args := map[string]interface{}{"name": name, "source_format": "pi-skill-loaded"}
	if path != "" {
		args["path"] = path
	}
	state := toolCallState{ID: callID, Name: "skill", Args: args, Timestamp: timestamp}
	call := m.base(timestamp, "tool.invoked", "tool", schema.SeverityInfo, "Pi skill loaded")
	m.applyToolCall(&call, state)
	m.append(line, "skill.loaded.call", call)

	result := m.base(timestamp, "tool.completed", "tool", schema.SeverityInfo, "Pi skill load completed")
	m.applyToolCall(&result, state)
	result.Raw = m.raw(map[string]interface{}{"custom_type": customType, "data": data["data"]})
	m.append(line, "skill.loaded.result", result)
}

func (m *mapper) consumeMessage(line int, message map[string]interface{}, fallback time.Time, emit bool) {
	role := strings.ToLower(stringValue(message["role"]))
	timestamp := timestampValue(firstNonNil(message["timestamp"], fallback))
	switch role {
	case "user":
		for i, text := range texts(message["content"]) {
			if emit {
				m.emitPrompt(line, i, timestamp, text)
			}
		}
	case "assistant":
		if model := firstNonEmpty(stringValue(message["model"]), stringValue(message["modelId"])); model != "" {
			m.model = model
		}
		content, _ := message["content"].([]interface{})
		start := len(m.out)
		for i, part := range content {
			m.consumeAssistantPart(line, i, timestamp, part, emit)
		}
		if emit {
			m.emitUsage(line, timestamp, message, start)
		}
	case "toolresult", "tool_result", "tool":
		callID := firstNonEmpty(stringValue(message["toolCallId"]), stringValue(message["callId"]), fmt.Sprintf("line-%d", line))
		if emit {
			m.emitToolResult(line, timestamp, callID, message)
		}
	case "bashexecution":
		if emit {
			m.emitBashExecution(line, timestamp, message)
		}
	case "error":
		if emit {
			m.emitError(line, timestamp, message)
		}
	}
}

func (m *mapper) consumeAssistantPart(line, index int, timestamp time.Time, part interface{}, emit bool) {
	item, _ := part.(map[string]interface{})
	if item == nil {
		return
	}
	kind := strings.ToLower(stringValue(item["type"]))
	switch {
	case kind == "thinking":
		text := firstNonEmpty(stringValue(item["thinking"]), stringValue(item["text"]), stringValue(item["content"]))
		if emit && text != "" {
			ev := m.base(timestamp, "agent.reasoning", "reasoning", schema.SeverityInfo, "Pi reasoning")
			ev.GenAI = ensureGenAI(ev.GenAI)
			ev.GenAI.Output = &schema.GenAIOutputInfo{Messages: []interface{}{map[string]interface{}{
				"role":  "assistant",
				"parts": []interface{}{map[string]interface{}{"type": "reasoning", "content": text}},
			}}}
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
			m.append(line, fmt.Sprintf("assistant.%d.thinking", index), ev)
		}
	case kind == "text":
		text := firstNonEmpty(stringValue(item["text"]), stringValue(item["content"]))
		if emit && text != "" {
			ev := m.base(timestamp, "agent.message", "session", schema.SeverityInfo, "Pi agent message")
			ev.GenAI = ensureGenAI(ev.GenAI)
			ev.GenAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
			m.append(line, fmt.Sprintf("assistant.%d.text", index), ev)
		}
	case toolCallKinds[kind]:
		state := m.toolCallFrom(line, index, item, timestamp)
		m.calls[state.ID] = state
		if emit {
			ev := m.base(timestamp, "tool.invoked", "tool", schema.SeverityInfo, "Pi tool invoked")
			m.applyToolCall(&ev, state)
			m.append(line, fmt.Sprintf("assistant.%d.tool_call", index), ev)
		}
	case toolResultKinds[kind]:
		callID := firstNonEmpty(stringValue(item["callId"]), stringValue(item["toolCallId"]), stringValue(item["id"]), fmt.Sprintf("line-%d-call-%d", line, index))
		if emit {
			m.emitToolResult(line, timestamp, callID, item)
		}
	}
}

func (m *mapper) toolCallFrom(line, index int, item map[string]interface{}, timestamp time.Time) toolCallState {
	nested := firstMap(item["toolCall"], item["functionCall"])
	callID := firstNonEmpty(
		stringValue(item["callId"]),
		stringValue(item["toolCallId"]),
		stringValue(nested["callId"]),
		stringValue(nested["id"]),
		stringValue(item["id"]),
		fmt.Sprintf("line-%d-call-%d", line, index),
	)
	name := firstNonEmpty(
		stringValue(item["name"]),
		stringValue(item["toolName"]),
		stringValue(nested["name"]),
		stringValue(nested["toolName"]),
		"unknown",
	)
	args := parseArgs(firstNonNil(item["args"], item["input"], item["arguments"], nested["args"], nested["input"], nested["arguments"]))
	return toolCallState{ID: callID, Name: name, Args: args, Timestamp: timestamp}
}

func (m *mapper) emitSessionStarted() {
	ts := timestampValue(firstNonNil(m.ref.Header["timestamp"], refTimestamp(m.ref)))
	ev := m.base(ts, "session.started", "session", schema.SeverityInfo, "Pi session started")
	ev.Raw = m.raw(map[string]interface{}{"source_path": m.ref.Path})
	m.append(0, "session.started", ev)
}

func (m *mapper) emitPrompt(line, index int, timestamp time.Time, text string) {
	ev := m.base(timestamp, "prompt.submitted", "prompt", schema.SeverityInfo, "Pi prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Input = &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}
	m.append(line, fmt.Sprintf("user.%d", index), ev)
}

func (m *mapper) emitToolResult(line int, timestamp time.Time, callID string, message map[string]interface{}) {
	state := m.calls[callID]
	if state.ID == "" {
		state = toolCallState{
			ID:   callID,
			Name: firstNonEmpty(stringValue(message["toolName"]), stringValue(message["name"]), "unknown"),
			Args: parseArgs(firstNonNil(message["args"], message["input"], message["arguments"])),
		}
	}
	output := extractTextContent(firstNonNil(message["output"], message["result"], message["content"], childMap(message["toolResult"])["output"]))
	if output == "" {
		output = "(empty result)"
	}
	failed := boolValue(firstNonNil(message["isError"], message["is_error"])) || isErrorStatus(firstNonNil(message["status"], childMap(message["details"])["status"]))
	action, category, severity := classifyToolResult(state.Name, state.Args, failed)
	ev := m.base(timestamp, action, category, severity, "Pi tool result")
	if failed {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
	}
	m.applyToolCall(&ev, state)
	if category == "command" {
		ev.Command = &schema.CommandInfo{Command: commandFor(state.Name, state.Args), Output: output}
	}
	if category == "file" {
		m.applyFileResult(&ev, state.Name, message)
	}
	if output != "" && output != "(empty result)" {
		if ev.Command != nil {
			ev.Command.Output = output
		}
		if ev.Content == nil {
			ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
		}
	}
	ev.Raw = m.raw(map[string]interface{}{"tool_result": message})
	m.append(line, "tool_result", ev)
}

func (m *mapper) emitBashExecution(line int, timestamp time.Time, message map[string]interface{}) {
	command := stringValue(message["command"])
	if command == "" {
		return
	}
	callID := fmt.Sprintf("line-%d-bash", line)
	state := toolCallState{ID: callID, Name: "bash", Args: map[string]interface{}{"command": command}, Timestamp: timestamp}
	output := extractTextContent(message["output"])
	ev := m.base(timestamp, "command.executed", "command", schema.SeverityInfo, "Pi bash command executed")
	m.applyToolCall(&ev, state)
	ev.Command = &schema.CommandInfo{Command: command, Output: output}
	if exitCode, ok := intValue(message["exitCode"]); ok {
		ev.Command.ExitCode = &exitCode
	}
	if boolValue(message["cancelled"]) || (ev.Command.ExitCode != nil && *ev.Command.ExitCode != 0) {
		ev.Error = &schema.ErrorInfo{Type: "command_failed"}
	}
	if output != "" {
		ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
	}
	ev.Raw = m.raw(map[string]interface{}{"bash_execution": message})
	m.append(line, "bash_execution", ev)
}

func (m *mapper) emitError(line int, timestamp time.Time, data map[string]interface{}) {
	text := firstNonEmpty(stringValue(data["errorMessage"]), stringValue(data["message"]), strings.Join(texts(data["content"]), "\n"), "Unknown error")
	ev := m.base(timestamp, "agent.error", "session", schema.SeverityHigh, "Pi error")
	ev.Error = &schema.ErrorInfo{Type: "runtime_error"}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.Raw = m.raw(map[string]interface{}{"error_message": text})
	m.append(line, "error", ev)
}

func (m *mapper) emitUsage(line int, timestamp time.Time, message map[string]interface{}, since int) {
	usage := piUsage(childMap(message["usage"]))
	if usage == nil {
		return
	}
	for i := len(m.out) - 1; i >= since; i-- {
		action := m.out[i].Event.Event.Action
		if action == "agent.message" || action == "agent.reasoning" || action == "tool.invoked" || action == "agent.error" {
			m.out[i].Event.GenAI = ensureGenAI(m.out[i].Event.GenAI)
			m.out[i].Event.GenAI.Usage = usage
			return
		}
	}
	ev := m.base(timestamp, "token.usage", "metric", schema.SeverityInfo, "Pi token usage")
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Usage = usage
	m.append(line, "usage", ev)
}

func (m *mapper) applyToolCall(ev *schema.Event, call toolCallState) {
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	if command := commandFor(call.Name, call.Args); command != "" {
		ev.Tool.Command = command
		ev.Command = &schema.CommandInfo{Command: command}
		ev.Content = asymptoteobserve.RetainedContent(command, asymptoteobserve.DefaultStringLimit)
	}
	if path := stringArg(call.Args, "path", "file_path", "target_path"); path != "" {
		ev.Tool.Path = path
		ev.File = &schema.FileInfo{Path: path, Operation: fileOperation(call.Name), Language: strings.TrimPrefix(filepath.Ext(path), ".")}
	}
	if server, tool := piMCPServerTool(call.Name); server != "" || tool != "" {
		ev.MCP = &schema.MCPInfo{Server: server, Tool: tool}
	}
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
	ev.GenAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Args}}
}

func (m *mapper) applyFileResult(ev *schema.Event, name string, message map[string]interface{}) {
	if ev.File == nil {
		ev.File = &schema.FileInfo{Operation: fileOperation(name)}
	}
	details := childMap(message["details"])
	diff := firstNonEmpty(stringValue(details["patch"]), stringValue(details["diff"]))
	if diff != "" {
		ev.File.Diff = diff
		ev.File.DiffBytes = len(diff)
		ev.File.DiffHash = sha256Hex(diff)
		ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
	}
}

func classifyToolResult(name string, args map[string]interface{}, failed bool) (string, string, schema.Severity) {
	lower := strings.ToLower(name)
	switch {
	case lower == "bash" || commandFor(name, args) != "":
		return "command.executed", "command", schema.SeverityInfo
	case lower == "read" && stringArg(args, "path", "file_path", "target_path") != "":
		return "file.read", "file", schema.SeverityInfo
	case lower == "write" && stringArg(args, "path", "file_path", "target_path") != "":
		return "file.created", "file", schema.SeverityInfo
	case lower == "edit" && stringArg(args, "path", "file_path", "target_path") != "":
		return "file.modified", "file", schema.SeverityInfo
	case isMCPTool(name):
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo
	case failed:
		return "tool.failed", "tool", schema.SeverityHigh
	default:
		return "tool.completed", "tool", schema.SeverityInfo
	}
}

func (m *mapper) base(timestamp time.Time, action, category string, severity schema.Severity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: schema.FidelityObserved,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	ev.Timestamp = schema.FormatTimestamp(timestamp)
	ev.Session = &schema.SessionInfo{ID: m.sessionID, WorkingDirectory: m.cwd}
	if m.model != "" {
		ev.Model = asymptoteobserve.NormalizeModelName(m.model)
	}
	if m.repository != "" {
		ev.Repository = m.repository
	}
	if m.branch != "" {
		ev.Branch = m.branch
	}
	return ev
}

func (m *mapper) raw(fields map[string]interface{}) map[string]interface{} {
	if fields == nil {
		fields = map[string]interface{}{}
	}
	if m.ref.Path != "" {
		fields["source_path"] = m.ref.Path
	}
	return map[string]interface{}{"pi": fields}
}

func (m *mapper) append(line int, suffix string, ev schema.Event) {
	ev.Event.ID = piEventID(fmt.Sprintf("%s:%s:%d:%s", m.sessionID, m.ref.Path, line, suffix))
	m.out = append(m.out, MappedEvent{SourceLine: line, Event: ev})
}

func commandFor(name string, args map[string]interface{}) string {
	if strings.EqualFold(name, "bash") {
		return stringArg(args, "command")
	}
	return stringArg(args, "command", "cmd")
}

func fileOperation(name string) string {
	switch strings.ToLower(name) {
	case "read":
		return "read"
	case "write":
		return "create"
	case "edit":
		return "modify"
	default:
		return ""
	}
}

func piMCPServerTool(toolName string) (string, string) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(toolName), "mcp__")
	if !ok {
		return "", ""
	}
	if server, tool, ok := strings.Cut(rest, "__"); ok && server != "" && tool != "" {
		return server, tool
	}
	server, tool, ok := strings.Cut(rest, "_")
	if !ok || server == "" || tool == "" {
		return "", ""
	}
	return server, tool
}

func isMCPTool(name string) bool {
	server, tool := piMCPServerTool(name)
	return server != "" || tool != ""
}

func piUsage(usage map[string]interface{}) *schema.GenAIUsageInfo {
	if len(usage) == 0 {
		return nil
	}
	out := &schema.GenAIUsageInfo{}
	if value, ok := int64Value(usage["input"]); ok {
		out.InputTokens = &value
	}
	if value, ok := int64Value(usage["output"]); ok {
		out.OutputTokens = &value
	}
	if value, ok := int64Value(usage["cacheRead"]); ok {
		out.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &value}
	}
	if value, ok := int64Value(usage["cacheWrite"]); ok {
		out.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &value}
	}
	if value, ok := int64Value(usage["reasoning"]); ok {
		out.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &value}
	}
	if cost := childMap(usage["cost"]); len(cost) > 0 {
		if value, ok := floatValue(cost["total"]); ok {
			out.CostUSD = &value
		}
	}
	if out.InputTokens == nil && out.OutputTokens == nil && out.CacheRead == nil && out.CacheCreation == nil && out.Reasoning == nil && out.CostUSD == nil {
		return nil
	}
	return out
}

func ensureGenAI(genAI *schema.GenAIInfo) *schema.GenAIInfo {
	if genAI == nil {
		return &schema.GenAIInfo{}
	}
	return genAI
}

func compactionRaw(data map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{"source_event_type": "compaction"}
	for _, key := range []string{"summary", "firstKeptEntryId", "tokensBefore", "details", "fromHook"} {
		if value, ok := data[key]; ok {
			out[key] = value
		}
	}
	return out
}

func texts(value interface{}) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []interface{}:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				if strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
				continue
			}
			m, _ := item.(map[string]interface{})
			if text := firstNonEmpty(stringValue(m["text"]), stringValue(m["content"]), stringValue(m["output"])); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func extractTextContent(value interface{}) string {
	if value == nil {
		return ""
	}
	if text := strings.Join(texts(value), "\n"); text != "" {
		return text
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	if string(data) == "null" {
		return ""
	}
	return string(data)
}

func childMap(value interface{}) map[string]interface{} {
	m, _ := value.(map[string]interface{})
	if m == nil {
		return map[string]interface{}{}
	}
	return m
}

func firstMap(values ...interface{}) map[string]interface{} {
	for _, value := range values {
		if m := childMap(value); len(m) > 0 {
			return m
		}
	}
	return map[string]interface{}{}
}

func parseArgs(value interface{}) map[string]interface{} {
	if m := childMap(value); len(m) > 0 {
		return m
	}
	if text := stringValue(value); text != "" {
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(text), &out); err == nil && out != nil {
			return out
		}
		return map[string]interface{}{"raw": text}
	}
	if value == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{"raw": value}
}

func stringArg(args map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(args[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func boolValue(value interface{}) bool {
	v, _ := value.(bool)
	return v
}

func intValue(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func int64Value(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		if v < 0 {
			return 0, false
		}
		return int64(v), true
	default:
		return 0, false
	}
}

func floatValue(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func isErrorStatus(value interface{}) bool {
	status := strings.ToLower(stringValue(value))
	return status == "error" || status == "failed" || status == "failure"
}

func timestampValue(value interface{}) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case float64:
		if v > 1_000_000_000_000 {
			return time.UnixMilli(int64(v)).UTC()
		}
		return time.Unix(int64(v), 0).UTC()
	case int64:
		if v > 1_000_000_000_000 {
			return time.UnixMilli(v).UTC()
		}
		return time.Unix(v, 0).UTC()
	case string:
		if n, ok := floatValueFromString(v); ok {
			if n > 1_000_000_000_000 {
				return time.UnixMilli(int64(n)).UTC()
			}
			return time.Unix(int64(n), 0).UTC()
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
		if t, err := schema.ParseTimestamp(v); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func floatValueFromString(value string) (float64, bool) {
	var out float64
	if _, err := fmt.Sscanf(strings.TrimSpace(value), "%f", &out); err != nil {
		return 0, false
	}
	return out, true
}

func refTimestamp(ref SessionRef) time.Time {
	if ref.ModTimeMS > 0 {
		return time.UnixMilli(ref.ModTimeMS).UTC()
	}
	return time.Now().UTC()
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var piIDNamespace = [16]byte{
	0x30, 0xa5, 0xe5, 0x5d, 0x8e, 0x20, 0x49, 0x24,
	0x9f, 0x81, 0x15, 0xb7, 0x59, 0x2a, 0x4f, 0x0d,
}

func piEventID(coordinate string) string {
	digest := sha1.New()
	digest.Write(piIDNamespace[:])
	io.WriteString(digest, coordinate)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
