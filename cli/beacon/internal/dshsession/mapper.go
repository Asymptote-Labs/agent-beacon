package dshsession

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

func MapSession(ref SessionRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{
		ref:      ref,
		opts:     opts,
		session:  sessionMetaFromRef(ref),
		calls:    map[string]toolCall{},
		lastTime: ref.ModTimeUnixMS,
	}
	for _, record := range records {
		m.consumeContext(record)
		if record.Line <= opts.MinLine {
			continue
		}
		m.consume(record)
	}
	return m.out
}

type mapper struct {
	ref      SessionRef
	opts     MapOptions
	session  SessionMeta
	model    string
	calls    map[string]toolCall
	out      []MappedEvent
	lastTime int64
}

type toolCall struct {
	ID        string
	Name      string
	Arguments map[string]interface{}
	Line      int
}

func sessionMetaFromRef(ref SessionRef) SessionMeta {
	if ref.Meta != nil {
		return *ref.Meta
	}
	return SessionMeta{ID: ref.ID}
}

func (m *mapper) consumeContext(record Record) {
	if record.TimeMS > 0 {
		m.lastTime = record.TimeMS
	}
	switch record.Kind {
	case "session":
		if id := firstString(record.Data, "id", "sessionId", "session_id"); id != "" {
			m.session.ID = id
		}
		if cwd := firstString(record.Data, "cwd", "workingDirectory", "working_directory"); cwd != "" {
			m.session.CWD = cwd
		}
		if parent := firstString(record.Data, "parentSession", "parent_session", "parentSessionId"); parent != "" {
			m.session.ParentSessionID = parent
		}
	case "request/header":
		if model := requestHeaderModel(record.Data); model != "" {
			m.model = model
			m.session.Model = model
		}
	case "session/title":
		if title := firstString(record.Data, "title"); title != "" {
			m.session.Title = title
		}
	case "assistant/message":
		if model := assistantModel(record.Data); model != "" {
			m.model = model
		}
	case "tool/call", "tool/code-dispatch-start":
		call := parseToolCall(record)
		if call.ID != "" {
			m.calls[call.ID] = call
		}
	}
}

func (m *mapper) consume(record Record) {
	m.consumeContext(record)
	switch record.Kind {
	case "session":
		if !m.opts.SkipSessionStarted {
			m.emitSessionStarted(record)
		}
	case "user/message":
		m.emitPrompt(record)
	case "assistant/message":
		m.emitAssistant(record)
	case "tool/call", "tool/code-dispatch-start":
		m.emitToolCall(record)
	case "tool/result", "tool/code-dispatch":
		m.emitToolResult(record)
	case "turn/end":
		m.emitTurnEnd(record)
	}
}

func (m *mapper) emitSessionStarted(record Record) {
	ev := m.base(record, "session.started", "session", schema.SeverityInfo, "DeepSeek Harness session started")
	ev.Raw = m.rawDsh(record, map[string]interface{}{"source_path": m.ref.Path})
	m.append(record, "session.started", ev)
}

func (m *mapper) emitPrompt(record Record) {
	text := userMessageText(record.Data)
	if text == "" {
		return
	}
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, "DeepSeek prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Input = &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}
	})
	ev.Raw = m.rawDsh(record, nil)
	m.append(record, "prompt.submitted", ev)
}

func (m *mapper) emitAssistant(record Record) {
	model := firstNonEmpty(assistantModel(record.Data), m.model, m.session.Model)
	for i, part := range assistantParts(record.Data) {
		switch part.kind {
		case "reasoning":
			ev := m.base(record, "agent.reasoning", "agent", schema.SeverityInfo, "DeepSeek assistant reasoning")
			ev.Model = model
			ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
				genAI.Output = &schema.GenAIOutputInfo{Messages: []map[string]interface{}{{
					"role":    "assistant",
					"content": []map[string]string{{"type": "reasoning", "text": part.text}},
				}}}
			})
			ev.Content = asymptoteobserve.RetainedContent(part.text, asymptoteobserve.DefaultRawStringLimit)
			ev.Raw = m.rawDsh(record, nil)
			m.append(record, fmt.Sprintf("assistant.reasoning.%d", i), ev)
		case "text":
			ev := m.base(record, "agent.message", "agent", schema.SeverityInfo, "DeepSeek assistant message")
			ev.Model = model
			ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
				genAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(part.text)}
			})
			ev.Content = asymptoteobserve.RetainedContent(part.text, asymptoteobserve.DefaultRawStringLimit)
			ev.Raw = m.rawDsh(record, nil)
			m.append(record, fmt.Sprintf("assistant.text.%d", i), ev)
		}
	}
	m.emitUsage(record)
}

func (m *mapper) emitToolCall(record Record) {
	call := parseToolCall(record)
	if call.ID != "" {
		m.calls[call.ID] = call
	}
	if call.Name == "" {
		return
	}
	ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, "DeepSeek tool invoked")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	applyCall(&ev, call)
	ev.Raw = m.rawDsh(record, nil)
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
	action, category, severity, message := classifyDsh(call, result)
	ev := m.base(record, action, category, severity, message)
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	applyCall(&ev, call)
	switch action {
	case "command.executed":
		applyCommand(&ev, call, result)
	case "file.read", "file.modified":
		applyFile(&ev, call, result)
	case "mcp.tool_invoked":
		ev.MCP = mcpInfo(call.Name)
	}
	if result.IsError {
		ev.Error = &schema.ErrorInfo{Type: "tool_execution_failed"}
	}
	ev.Raw = m.rawDsh(record, map[string]interface{}{"tool_result": result.Raw})
	m.append(record, "tool.result", ev)
}

func (m *mapper) emitTurnEnd(record Record) {
	reason := childMap(record.Data, "reason")
	kind := firstString(reason, "kind", "type")
	if kind != "error" && kind != "interrupted" {
		return
	}
	ev := m.base(record, "agent.error", "session", schema.SeverityHigh, "DeepSeek turn ended with error")
	ev.Error = &schema.ErrorInfo{Type: kind}
	ev.Raw = m.rawDsh(record, nil)
	m.append(record, "turn.end."+kind, ev)
}

func (m *mapper) emitUsage(record Record) {
	usage := usageFromAssistant(record.Data)
	if usage == nil {
		return
	}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, "DeepSeek token usage")
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) { genAI.Usage = usage })
	ev.Model = firstNonEmpty(assistantModel(record.Data), m.model, m.session.Model)
	ev.Raw = m.rawDsh(record, map[string]interface{}{"token_source": "assistant_message"})
	m.append(record, "usage", ev)
}

func (m *mapper) base(record Record, action, category string, severity schema.Severity, message string) schema.Event {
	ts := m.lastTime
	if record.TimeMS > 0 {
		ts = record.TimeMS
	}
	if ts <= 0 {
		ts = m.ref.ModTimeUnixMS
	}
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
		Origin:   schema.OriginLocal,
		Fidelity: schema.FidelityObserved,
		Message:  message,
	})
	if ts > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(ts))
	}
	ev.Session = &schema.SessionInfo{ID: firstNonEmpty(m.session.ID, m.ref.ID), WorkingDirectory: m.session.CWD}
	if model := firstNonEmpty(m.model, m.session.Model); model != "" {
		ev.Model = model
	}
	return ev
}

func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	dedupID := fmt.Sprintf("%s:%s:%d:%s", m.ref.Path, firstNonEmpty(m.session.ID, m.ref.ID), record.Line, suffix)
	ev.Event.ID = dshEventID(dedupID)
	if m.session.ParentSessionID != "" {
		ev.Raw = mergeRaw(ev.Raw, map[string]interface{}{"parent_session": m.session.ParentSessionID})
	}
	m.out = append(m.out, MappedEvent{SourceLine: record.Line, DedupID: dedupID, Event: ev})
}

type assistantPart struct {
	kind string
	text string
}

type toolResult struct {
	CallID  string
	Name    string
	Text    string
	IsError bool
	Raw     interface{}
}

func parseToolCall(record Record) toolCall {
	data := unwrap(record.Data, "call", "toolCall", "tool_call")
	args := argumentsFrom(data)
	return toolCall{
		ID:        firstString(data, "callId", "callID", "id", "tool_call_id", "tool_use_id"),
		Name:      firstString(data, "name", "toolName", "tool_name"),
		Arguments: args,
		Line:      record.Line,
	}
}

func parseToolResult(record Record) toolResult {
	data := unwrap(record.Data, "result", "toolResult", "tool_result")
	callID := firstString(data, "callId", "callID", "id", "tool_call_id", "toolUseId", "tool_use_id")
	name := firstString(data, "name", "toolName", "tool_name")
	isError := boolValue(data["isError"]) || boolValue(data["is_error"]) || boolValue(data["error"])
	text := resultText(data)
	return toolResult{CallID: callID, Name: name, Text: text, IsError: isError, Raw: data}
}

func classifyDsh(call toolCall, result toolResult) (action, category string, severity schema.Severity, message string) {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	if strings.HasPrefix(name, "mcp__") {
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "DeepSeek MCP tool invoked"
	}
	if result.IsError {
		return "tool.failed", "tool", schema.SeverityHigh, "DeepSeek tool failed"
	}
	switch dshToolAction(call.Name, call.Arguments) {
	case "command.executed":
		return "command.executed", "command", schema.SeverityInfo, "DeepSeek command executed"
	case "file.read":
		return "file.read", "file", schema.SeverityInfo, "DeepSeek file read"
	case "file.modified":
		return "file.modified", "file", schema.SeverityInfo, "DeepSeek file modified"
	default:
		return "tool.completed", "tool", schema.SeverityInfo, "DeepSeek tool completed"
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
	if path := firstString(call.Arguments, "path", "file_path", "target_path"); path != "" {
		ev.Tool.Path = path
	}
	if command := commandFromCall(call); command != "" {
		ev.Tool.Command = command
	}
}

func applyCommand(ev *schema.Event, call toolCall, result toolResult) {
	info := &schema.CommandInfo{Command: commandFromCall(call)}
	if result.Text != "" {
		info.Output = result.Text
		ev.Content = asymptoteobserve.RetainedContent(result.Text, asymptoteobserve.DefaultStringLimit)
	}
	if code, ok := dshExitCode(result.Text); ok {
		info.ExitCode = &code
	}
	ev.Command = info
}

func applyFile(ev *schema.Event, call toolCall, result toolResult) {
	path := firstString(call.Arguments, "file_path", "path")
	info := &schema.FileInfo{Path: path, Operation: dshFileOperation(call.Name, call.Arguments)}
	if path != "" {
		info.Language = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	if diff := diffFromCall(call); diff != "" && !result.IsError {
		info.Diff = diff
		info.DiffBytes = len(diff)
		sum := sha256.Sum256([]byte(diff))
		info.DiffHash = hex.EncodeToString(sum[:])
		ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
	}
	ev.File = info
}

func diffFromCall(call toolCall) string {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	path := firstString(call.Arguments, "file_path", "path")
	switch name {
	case "write":
		content := firstStringRaw(call.Arguments, "content", "file_text")
		if path != "" && content != "" {
			return "--- /dev/null\n+++ " + path + "\n+" + strings.ReplaceAll(content, "\n", "\n+") + "\n"
		}
	case "edit":
		oldText := firstStringRaw(call.Arguments, "old_string", "old_str")
		newText := firstStringRaw(call.Arguments, "new_string", "new_str")
		if oldText != "" || newText != "" {
			return "-" + strings.ReplaceAll(oldText, "\n", "\n-") + "\n+" + strings.ReplaceAll(newText, "\n", "\n+") + "\n"
		}
	case "str_replace_editor":
		op := dshEditorOperation(call.Name, call.Arguments)
		if op == "create" {
			content := firstStringRaw(call.Arguments, "file_text")
			if content != "" {
				return "--- /dev/null\n+++ " + path + "\n+" + strings.ReplaceAll(content, "\n", "\n+") + "\n"
			}
		}
		if op == "str_replace" || op == "insert" {
			oldText := firstStringRaw(call.Arguments, "old_str")
			newText := firstStringRaw(call.Arguments, "new_str", "insert")
			if oldText != "" || newText != "" {
				return "-" + strings.ReplaceAll(oldText, "\n", "\n-") + "\n+" + strings.ReplaceAll(newText, "\n", "\n+") + "\n"
			}
		}
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

func userMessageText(data map[string]interface{}) string {
	data = unwrap(data, "message")
	return textValue(firstPresent(data, "text", "content", "message"))
}

func assistantParts(data map[string]interface{}) []assistantPart {
	msg := unwrap(data, "message")
	raw := firstPresent(msg, "content", "parts")
	var out []assistantPart
	switch v := raw.(type) {
	case []interface{}:
		for _, item := range v {
			m, _ := item.(map[string]interface{})
			kind := firstNonEmpty(firstString(m, "type"), "text")
			if kind == "tool-call" || kind == "tool_call" {
				continue
			}
			text := textValue(firstPresent(m, "text", "content"))
			if text != "" {
				out = append(out, assistantPart{kind: normalizePartKind(kind), text: text})
			}
		}
	default:
		if text := textValue(raw); text != "" {
			out = append(out, assistantPart{kind: "text", text: text})
		}
	}
	if reasoning := firstString(msg, "reasoning", "reasoningText", "reasoning_content"); reasoning != "" {
		out = append([]assistantPart{{kind: "reasoning", text: reasoning}}, out...)
	}
	return out
}

func usageFromAssistant(data map[string]interface{}) *schema.GenAIUsageInfo {
	msg := unwrap(data, "message")
	u := childMap(firstMap(msg, "usage"), "usage")
	if len(u) == 0 {
		u = firstMap(msg, "usage")
	}
	if len(u) == 0 {
		u = firstMap(data, "usage")
	}
	usage := &schema.GenAIUsageInfo{}
	if n, ok := int64Value(firstPresent(u, "inputTokens", "input_tokens", "promptTokens")); ok && n > 0 {
		usage.InputTokens = &n
	}
	if n, ok := int64Value(firstPresent(u, "outputTokens", "output_tokens", "completionTokens")); ok && n > 0 {
		usage.OutputTokens = &n
	}
	if n, ok := int64Value(firstPresent(u, "cacheReadTokens", "cache_read_tokens")); ok && n > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &n}
	}
	if n, ok := int64Value(firstPresent(u, "cacheWriteTokens", "cache_write_tokens")); ok && n > 0 {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &n}
	}
	if n, ok := int64Value(firstPresent(u, "reasoningTokens", "reasoning_tokens")); ok && n > 0 {
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &n}
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CacheRead == nil && usage.CacheCreation == nil && usage.Reasoning == nil {
		return nil
	}
	return usage
}

func requestHeaderModel(data map[string]interface{}) string {
	header := childMap(data, "header")
	config := childMap(header, "config")
	return firstString(config, "model")
}

func assistantModel(data map[string]interface{}) string {
	msg := unwrap(data, "message")
	source := childMap(msg, "source")
	return firstNonEmpty(firstString(source, "model"), firstString(msg, "model"))
}

func (m *mapper) rawDsh(record Record, extra map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	if len(record.Raw) > 0 {
		var decoded map[string]interface{}
		if json.Unmarshal(record.Raw, &decoded) == nil {
			raw["record"] = decoded
		}
	}
	if m.session.ParentSessionID != "" {
		raw["parent_session"] = m.session.ParentSessionID
	}
	for k, v := range extra {
		raw[k] = v
	}
	return map[string]interface{}{"dsh": raw}
}

func mergeRaw(existing map[string]interface{}, fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return existing
	}
	if existing == nil {
		existing = map[string]interface{}{}
	}
	dsh, _ := existing["dsh"].(map[string]interface{})
	if dsh == nil {
		dsh = map[string]interface{}{}
	}
	for k, v := range fields {
		dsh[k] = v
	}
	existing["dsh"] = dsh
	return existing
}

func withGenAI(genAI *schema.GenAIInfo, edit func(*schema.GenAIInfo)) *schema.GenAIInfo {
	if genAI == nil {
		genAI = &schema.GenAIInfo{}
	}
	edit(genAI)
	return genAI
}

func unwrap(data map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		if child, ok := data[key].(map[string]interface{}); ok {
			return child
		}
	}
	return data
}

func argumentsFrom(data map[string]interface{}) map[string]interface{} {
	if args := firstMap(data, "arguments", "args", "input", "tool_input"); len(args) > 0 {
		return args
	}
	if text := firstString(data, "argumentsJson", "arguments_json"); text != "" {
		var args map[string]interface{}
		if json.Unmarshal([]byte(text), &args) == nil {
			return args
		}
		return map[string]interface{}{"value": text}
	}
	return map[string]interface{}{}
}

func resultText(data map[string]interface{}) string {
	return textValue(firstPresent(data, "text", "output", "content", "result"))
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

func firstPresent(data map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}

func firstMap(data map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		if child, ok := data[key].(map[string]interface{}); ok {
			return child
		}
	}
	return nil
}

func childMap(data map[string]interface{}, key string) map[string]interface{} {
	child, _ := data[key].(map[string]interface{})
	if child == nil {
		return map[string]interface{}{}
	}
	return child
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
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n, err == nil && n >= 0
	default:
		return 0, false
	}
}

func boolValue(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true") || strings.EqualFold(v, "error")
	default:
		return false
	}
}

func normalizePartKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "reasoning", "thinking":
		return "reasoning"
	default:
		return "text"
	}
}

var dshIDNamespace = [16]byte{
	0x4d, 0x73, 0x68, 0x21, 0x18, 0xa5, 0x4e, 0x6f,
	0x99, 0x0a, 0xdf, 0x82, 0x33, 0x52, 0x1c, 0x0d,
}

func dshEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(dshIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
