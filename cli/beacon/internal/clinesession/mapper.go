package clinesession

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type MappedEvent struct {
	SourceOrder int
	Event       schema.Event
}

type MapOptions struct {
	MinOrder    int
	SkipStarted bool
}

func MapTrace(ref TraceRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{ref: ref, opts: opts}
	if !opts.SkipStarted {
		m.emitSessionStarted()
	}
	for i := range records {
		m.consume(records[i])
	}
	return m.out
}

type mapper struct {
	ref  TraceRef
	opts MapOptions
	out  []MappedEvent
}

func (m *mapper) consume(record Record) {
	if record.Order <= m.opts.MinOrder {
		return
	}
	switch record.Type {
	case "user_message":
		m.emitPrompt(record)
	case "agent_text":
		m.emitAgentMessage(record)
	case "agent_reasoning":
		m.emitReasoning(record)
	case "tool_call":
		m.emitToolCall(record)
	case "tool_result":
		m.emitToolResult(record)
	}
}

func (m *mapper) base(record Record, action, category string, severity schema.Severity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: schema.FidelityObserved,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness:  schema.HarnessInfo{Name: Harness, CollectionMethod: schema.CollectionMethodPoll},
	})
	if record.TimestampMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(record.TimestampMS))
	} else if m.ref.UpdatedAtUnixMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(m.ref.UpdatedAtUnixMS))
	}
	ev.Session = &schema.SessionInfo{ID: m.ref.ID, WorkingDirectory: m.ref.Directory}
	if record.ModelID != "" {
		ev.Model = asymptoteobserve.NormalizeModelName(record.ModelID)
	}
	return ev
}

func (m *mapper) emitSessionStarted() {
	record := Record{Order: 0, TimestampMS: m.ref.UpdatedAtUnixMS}
	ev := m.base(record, "session.started", "session", schema.SeverityInfo, "Cline session discovered")
	ev.Raw = map[string]interface{}{"cline": map[string]interface{}{"source_path": m.ref.SourcePath, "source_kind": m.ref.Kind}}
	if m.ref.RelatedTo != "" {
		ev.Raw["cline"].(map[string]interface{})["related_to"] = m.ref.RelatedTo
	}
	m.append(record, "session.started", ev)
}

func (m *mapper) emitPrompt(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, "Cline prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = &schema.GenAIInfo{Input: &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}}
	ev.Raw = m.raw(record)
	m.append(record, "prompt", ev)
}

func (m *mapper) emitAgentMessage(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "agent.message", "session", schema.SeverityInfo, "Cline agent message")
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}}
	applyUsage(&ev, record.Tokens)
	ev.Raw = m.raw(record)
	m.append(record, "assistant", ev)
}

func (m *mapper) emitReasoning(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "agent.reasoning", "reasoning", schema.SeverityInfo, "Cline agent reasoning")
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: []interface{}{map[string]interface{}{
		"role": "assistant",
		"parts": []interface{}{map[string]interface{}{
			"type":    "reasoning",
			"content": text,
		}},
	}}}}
	applyUsage(&ev, record.Tokens)
	ev.Raw = m.raw(record)
	m.append(record, "reasoning", ev)
}

func (m *mapper) emitToolCall(record Record) {
	name := firstNonEmpty(record.ToolName, "unknown")
	ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, "Cline tool invoked")
	ev.Tool = &schema.ToolInfo{Name: name}
	applyToolCall(&ev, name, record)
	applyUsage(&ev, record.Tokens)
	ev.Raw = m.raw(record)
	m.append(record, "tool.call", ev)
}

func (m *mapper) emitToolResult(record Record) {
	name := firstNonEmpty(record.ToolName, "unknown")
	action, category, severity, message := classifyTool(name, record)
	ev := m.base(record, action, category, severity, message)
	ev.Tool = &schema.ToolInfo{Name: name}
	applyToolCall(&ev, name, record)
	switch category {
	case "command":
		applyCommand(&ev, record)
	case "file":
		applyFile(&ev, name, record, m.ref.Directory)
	case "mcp":
		ev.MCP = &schema.MCPInfo{Tool: name}
	}
	if record.Status == "error" {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
	}
	ev.Raw = m.raw(record)
	m.append(record, "tool.result", ev)
}

func classifyTool(name string, record Record) (action, category string, severity schema.Severity, message string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "list_files" || lower == "search_files" || lower == "list_code_definition_names":
		return "file.read", "file", schema.SeverityInfo, "Cline file read"
	case toolNameHasToken(lower, "mcp") || lower == "use_mcp_tool" || lower == "access_mcp_resource":
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "Cline MCP tool invoked"
	case toolNameHasToken(lower, "command") || toolNameHasToken(lower, "commands") || toolNameHasToken(lower, "bash") || toolNameHasToken(lower, "shell") || toolNameHasToken(lower, "terminal"):
		return "command.executed", "command", schema.SeverityInfo, "Cline command executed"
	case toolNameHasToken(lower, "read"):
		return "file.read", "file", schema.SeverityInfo, "Cline file read"
	case toolNameHasToken(lower, "write") || toolNameHasToken(lower, "edit") || toolNameHasToken(lower, "replace") || toolNameHasToken(lower, "patch") || toolNameHasToken(lower, "create"):
		return "file.modified", "file", schema.SeverityInfo, "Cline file modified"
	case record.Status == "error":
		return "tool.failed", "tool", schema.SeverityHigh, "Cline tool failed"
	default:
		return "tool.completed", "tool", schema.SeverityInfo, "Cline tool completed"
	}
}

func applyToolCall(ev *schema.Event, name string, record Record) {
	call := &schema.GenAIToolCallInfo{ID: record.CallID}
	if len(record.Args) > 0 {
		call.Arguments = record.Args
	}
	if record.Type == "tool_result" {
		if record.Output != nil {
			call.Result = record.Output
		} else if record.OutputText != "" {
			call.Result = record.OutputText
		}
	}
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
	ev.GenAI.Tool = &schema.GenAIToolInfo{Name: name, Call: call}
	if path := argString(record.Args, "path", "file_path", "filePath", "target_file", "targetFile"); path != "" {
		ev.Tool.Path = path
	}
	if command := argString(record.Args, "command", "cmd"); command != "" {
		ev.Tool.Command = command
	}
}

func applyCommand(ev *schema.Event, record Record) {
	command := ""
	if ev.Tool != nil {
		command = ev.Tool.Command
	}
	info := &schema.CommandInfo{Command: command}
	if out := strings.TrimSpace(record.OutputText); out != "" {
		info.Output = out
		ev.Content = asymptoteobserve.RetainedContent(out, asymptoteobserve.DefaultStringLimit)
	}
	ev.Command = info
}

func applyFile(ev *schema.Event, name string, record Record, root string) {
	pathValue := ""
	if ev.Tool != nil {
		pathValue = ev.Tool.Path
	}
	if pathValue == "" {
		return
	}
	pathValue = clineWorkspacePath(pathValue, root)
	operation := clineFileOperation(name)
	if operation == "" {
		return
	}
	ev.File = &schema.FileInfo{Path: pathValue, Operation: operation, Language: fileLanguage(pathValue)}
	if ev.Tool != nil {
		ev.Tool.Path = pathValue
	}
}

func applyUsage(ev *schema.Event, usage *TokenUsage) {
	if usage == nil {
		return
	}
	info := &schema.GenAIUsageInfo{}
	if usage.Input > 0 {
		value := usage.Input
		info.InputTokens = &value
	}
	if usage.Output > 0 {
		value := usage.Output
		info.OutputTokens = &value
	}
	if usage.CacheRead > 0 {
		value := usage.CacheRead
		info.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &value}
	}
	if usage.CacheWrite > 0 {
		value := usage.CacheWrite
		info.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &value}
	}
	if usage.Reasoning > 0 {
		value := usage.Reasoning
		info.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &value}
	}
	if usage.CostUSD > 0 {
		value := usage.CostUSD
		info.CostUSD = &value
	}
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Usage = info
}

func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	ev.Event.ID = deterministicEventID(m.ref, record.Order, suffix)
	m.out = append(m.out, MappedEvent{SourceOrder: record.Order, Event: ev})
}

func (m *mapper) raw(record Record) map[string]interface{} {
	return map[string]interface{}{"cline": map[string]interface{}{
		"source_path": m.ref.SourcePath,
		"source_kind": m.ref.Kind,
		"record":      record.Raw,
	}}
}

func deterministicEventID(ref TraceRef, order int, suffix string) string {
	h := sha1.Sum([]byte(fmt.Sprintf("cline:%s:%s:%d:%s", ref.Kind, ref.ID, order, suffix)))
	return hex.EncodeToString(h[:])
}

func ensureGenAI(info *schema.GenAIInfo) *schema.GenAIInfo {
	if info != nil {
		return info
	}
	return &schema.GenAIInfo{}
}

func argString(args map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(args[key]); value != "" {
			return value
		}
	}
	return ""
}

func outputString(value interface{}) string {
	if text := stringValue(value); text != "" {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return ""
	}
	return string(data)
}

func toolNameHasToken(name, token string) bool {
	parts := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	for _, part := range parts {
		if part == token {
			return true
		}
	}
	return false
}

func clineFileOperation(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "list_files", "search_files", "list_code_definition_names":
		return "read"
	}
	switch {
	case toolNameHasToken(lower, "read") || toolNameHasToken(lower, "view") || toolNameHasToken(lower, "list"):
		return "read"
	case toolNameHasToken(lower, "write") || toolNameHasToken(lower, "create"):
		return "create"
	case toolNameHasToken(lower, "edit") || toolNameHasToken(lower, "replace") || toolNameHasToken(lower, "patch"):
		return "modify"
	default:
		return ""
	}
}

func clineWorkspacePath(value, root string) string {
	value = strings.TrimSpace(value)
	if value == "" || root == "" || isRootedPath(value) {
		return value
	}
	separator := "/"
	if strings.Contains(root, "\\") && !strings.Contains(root, "/") {
		separator = "\\"
	}
	volume, rest := splitPathVolume(root)
	joined := path.Join(strings.ReplaceAll(rest, "\\", "/"), strings.ReplaceAll(value, "\\", "/"))
	if separator != "/" {
		joined = strings.ReplaceAll(joined, "/", separator)
	}
	return volume + joined
}

func isRootedPath(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '/' || value[0] == '\\' {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		letter := value[0] | 0x20
		return letter >= 'a' && letter <= 'z'
	}
	return filepath.IsAbs(value)
}

func splitPathVolume(value string) (string, string) {
	if len(value) >= 2 && value[1] == ':' {
		return value[:2], value[2:]
	}
	return "", value
}
