package openclawsession

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

type MappedEvent struct {
	DedupID     string
	SourceOrder int
	Event       schema.Event
}

type MapOptions struct {
	MinOrder int
}

func MapTrace(ref TraceRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{ref: ref, opts: opts}
	for _, record := range records {
		m.consume(record)
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
	case "session_started":
		m.emitSessionStarted(record)
	case "user_message":
		m.emitPrompt(record)
	case "assistant_text":
		m.emitAgentMessage(record)
	case "agent_reasoning":
		m.emitReasoning(record)
	case "tool_call":
		m.emitToolCall(record)
	case "tool_result":
		m.emitToolResult(record)
	case "context_injection":
		m.emitContext(record)
	case "error":
		m.emitError(record)
	case "token_usage":
		m.emitUsage(record)
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
	ev.Raw = mergeRaw(ev.Raw, map[string]any{"profile": m.ref.Profile, "source_path": m.ref.SourcePath})
	return ev
}

func (m *mapper) emitSessionStarted(record Record) {
	ev := m.base(record, "session.started", "session", schema.SeverityInfo, "OpenClaw session started")
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "session.started", ev)
}

func (m *mapper) emitPrompt(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, "OpenClaw prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = &schema.GenAIInfo{Input: &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}}
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "prompt", ev)
	if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
		link := m.base(record, "session.handoff", "session", schema.SeverityInfo, "Session continued from a "+info.SourceHarness+" session")
		link.Handoff = &info
		m.append(record, "prompt.handoff", link)
	}
}

func (m *mapper) emitAgentMessage(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "agent.message", "session", schema.SeverityInfo, "OpenClaw agent message")
	ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "assistant", ev)
}

func (m *mapper) emitReasoning(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "agent.reasoning", "session", schema.SeverityInfo, "OpenClaw agent reasoning")
	ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: []any{map[string]any{
		"role":  "assistant",
		"parts": []any{map[string]any{"type": "reasoning", "content": text}},
	}}}}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "reasoning", ev)
}

func (m *mapper) emitToolCall(record Record) {
	name := firstNonEmpty(record.ToolName, "unknown")
	ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, "OpenClaw tool invoked")
	ev.Tool = &schema.ToolInfo{Name: name}
	applyToolCall(&ev, name, record)
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
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
		applyFile(&ev, name, record)
	case "mcp":
		server, tool := openClawMCPServerTool(name)
		ev.MCP = &schema.MCPInfo{Server: server, Tool: firstNonEmpty(tool, name)}
	}
	if record.Status == "error" || record.Status == "failed" || record.Status == "failure" {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
	}
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "tool.result", ev)
}

func (m *mapper) emitContext(record Record) {
	ev := m.base(record, "session.context", "session", schema.SeverityInfo, "OpenClaw runtime context injected")
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "context", ev)
}

func (m *mapper) emitError(record Record) {
	ev := m.base(record, "session.error", "session", schema.SeverityHigh, "OpenClaw session error")
	ev.Error = &schema.ErrorInfo{Type: firstNonEmpty(record.Content, "session_error")}
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "error", ev)
}

func (m *mapper) emitUsage(record Record) {
	if record.Tokens == nil {
		return
	}
	usage := &schema.GenAIUsageInfo{}
	if record.Tokens.Input > 0 {
		v := record.Tokens.Input
		usage.InputTokens = &v
	}
	if record.Tokens.Output > 0 {
		v := record.Tokens.Output
		usage.OutputTokens = &v
	}
	if record.Tokens.CacheRead > 0 {
		v := record.Tokens.CacheRead
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &v}
	}
	if record.Tokens.CacheWrite > 0 {
		v := record.Tokens.CacheWrite
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &v}
	}
	reasoning := firstNonZero(record.Tokens.Reasoning, record.Tokens.ReasoningTokens)
	if reasoning > 0 {
		v := reasoning
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &v}
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CacheRead == nil && usage.CacheCreation == nil && usage.Reasoning == nil {
		return
	}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, "OpenClaw token usage")
	ev.GenAI = &schema.GenAIInfo{Usage: usage}
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "usage", ev)
}

func classifyTool(name string, record Record) (action, category string, severity schema.Severity, message string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case isOpenClawMCPTool(lower):
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "OpenClaw MCP tool invoked"
	case lower == "exec" || toolNameHasToken(lower, "bash") || toolNameHasToken(lower, "shell"):
		return "command.executed", "command", schema.SeverityInfo, "OpenClaw command executed"
	case lower == "read" || lower == "grep" || lower == "find" || lower == "ls":
		return "file.read", "file", schema.SeverityInfo, "OpenClaw file read"
	case lower == "write":
		return "file.created", "file", schema.SeverityInfo, "OpenClaw file created"
	case lower == "edit" || lower == "apply_patch":
		return "file.modified", "file", schema.SeverityInfo, "OpenClaw file modified"
	case record.Status == "error" || record.Status == "failed" || record.Status == "failure":
		return "tool.failed", "tool", schema.SeverityHigh, "OpenClaw tool failed"
	default:
		return "tool.completed", "tool", schema.SeverityInfo, "OpenClaw tool completed"
	}
}

func applyToolCall(ev *schema.Event, name string, record Record) {
	call := &schema.GenAIToolCallInfo{ID: record.CallID}
	if len(record.Args) > 0 {
		call.Arguments = record.Args
	}
	if record.Output != nil && record.Type == "tool_result" {
		call.Result = record.Output
	}
	ev.GenAI = &schema.GenAIInfo{
		Operation: &schema.GenAIOperationInfo{Name: "execute_tool"},
		Tool:      &schema.GenAIToolInfo{Name: name, Call: call},
	}
	if ev.Tool == nil {
		ev.Tool = &schema.ToolInfo{Name: name}
	}
	if path := argString(record.Args, "path", "file_path", "filePath", "target", "destination"); path != "" {
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
	if out := outputString(record.Output); out != "" {
		info.Output = out
		ev.Content = asymptoteobserve.RetainedContent(out, asymptoteobserve.DefaultStringLimit)
	}
	if code, ok := exitCode(record.Output); ok {
		info.ExitCode = &code
	}
	ev.Command = info
}

func applyFile(ev *schema.Event, name string, record Record) {
	path := ""
	if ev.Tool != nil {
		path = ev.Tool.Path
	}
	operation := "modify"
	switch ev.Event.Action {
	case "file.read":
		operation = "read"
	case "file.created":
		operation = "create"
	}
	file := &schema.FileInfo{Path: path, Operation: operation}
	if path != "" {
		file.Language = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	if diff := diffText(name, record); diff != "" {
		file.Diff = diff
		file.DiffBytes = len(diff)
		file.DiffHash = sha256Hex(diff)
		ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
	}
	ev.File = file
}

func diffText(name string, record Record) string {
	if value := argString(record.Args, "diff", "patch", "input"); value != "" && strings.EqualFold(name, "apply_patch") {
		return value
	}
	if strings.EqualFold(name, "write") {
		return argString(record.Args, "content")
	}
	if strings.EqualFold(name, "edit") {
		old := argString(record.Args, "oldText", "old_string", "oldString")
		newText := argString(record.Args, "newText", "new_string", "newString")
		if old != "" || newText != "" {
			return "- " + old + "\n+ " + newText + "\n"
		}
	}
	return ""
}

func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	dedupID := fmt.Sprintf("%s:%s:%d:%s:%s", m.ref.Profile, m.ref.ID, record.Order, record.NativeID, suffix)
	ev.Event.ID = openClawEventID(dedupID)
	m.out = append(m.out, MappedEvent{DedupID: dedupID, SourceOrder: record.Order, Event: ev})
}

var openClawIDNamespace = [16]byte{
	0xb2, 0x10, 0x5c, 0xde, 0x15, 0x31, 0x42, 0x8f,
	0x94, 0x4c, 0x70, 0xa4, 0xe1, 0x0f, 0xc8, 0x42,
}

func openClawEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(openClawIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}

func isOpenClawMCPTool(name string) bool {
	server, tool := openClawMCPServerTool(name)
	return server != "" || tool != ""
}

func openClawMCPServerTool(toolName string) (string, string) {
	trimmed := strings.TrimSpace(toolName)
	if strings.HasPrefix(trimmed, "mcp__") {
		parts := strings.Split(trimmed, "__")
		if len(parts) >= 3 {
			return parts[1], strings.Join(parts[2:], "__")
		}
	}
	server, tool, ok := strings.Cut(trimmed, "__")
	if !ok || server == "" || tool == "" {
		return "", ""
	}
	return server, tool
}

func toolNameHasToken(name, token string) bool {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if part == token {
			return true
		}
	}
	return false
}

func mergeRaw(existing map[string]any, fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return existing
	}
	if existing == nil {
		existing = map[string]any{}
	}
	nested, ok := existing["openclaw"].(map[string]any)
	if !ok {
		nested = map[string]any{}
	}
	for key, value := range fields {
		nested[key] = value
	}
	existing["openclaw"] = nested
	return existing
}

func argString(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := textFromAny(args[key]); value != "" {
			return value
		}
	}
	return ""
}

func outputString(value any) string {
	if s := textFromAny(value); s != "" {
		return s
	}
	if data, err := json.Marshal(value); err == nil {
		return string(data)
	}
	return ""
}

func exitCode(value any) (int, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	for _, key := range []string{"exitCode", "exit_code", "code"} {
		switch v := m[key].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		}
	}
	if details, ok := m["details"].(map[string]any); ok {
		return exitCode(details)
	}
	return 0, false
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
