package opencodesession

import (
	"crypto/sha1"
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

const Harness = "opencode"

type MappedEvent struct {
	DedupID     string
	SourceOrder int
	Event       schema.Event
}

// MapOptions is deliberately empty: a trace is mapped whole on every read and the collector decides
// what is new by event id. Filtering here by how far the last sweep read would reintroduce exactly
// what that dedup exists to avoid -- see collectTrace.
type MapOptions struct{}

func MapTrace(ref TraceRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{ref: ref, opts: opts}
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
	switch record.Type {
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
	case "compaction":
		m.emitCompaction(record)
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
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
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

func (m *mapper) emitPrompt(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, "opencode prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = &schema.GenAIInfo{Input: &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}}
	m.append(record, "prompt", ev)
}

func (m *mapper) emitAgentMessage(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "agent.message", "session", schema.SeverityInfo, "opencode agent message")
	ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	m.append(record, "assistant", ev)
}

func (m *mapper) emitReasoning(record Record) {
	text := strings.TrimSpace(record.Content)
	if text == "" {
		return
	}
	ev := m.base(record, "agent.reasoning", "session", schema.SeverityInfo, "opencode agent reasoning")
	ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: []interface{}{map[string]interface{}{
		"role": "assistant",
		"parts": []interface{}{map[string]interface{}{
			"type":    "reasoning",
			"content": text,
		}},
	}}}}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	m.append(record, "reasoning", ev)
}

func (m *mapper) emitToolCall(record Record) {
	name := firstNonEmpty(record.ToolName, "unknown")
	ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, "opencode tool invoked")
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
		ev.MCP = &schema.MCPInfo{Tool: name}
	}
	if record.Status == "error" {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
	}
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "tool.result", ev)
}

func (m *mapper) emitCompaction(record Record) {
	ev := m.base(record, "session.compacting", "session", schema.SeverityInfo, "opencode compacted session history")
	ev.Raw = mergeRaw(ev.Raw, record.Raw)
	m.append(record, "compaction", ev)
}

func (m *mapper) emitError(record Record) {
	ev := m.base(record, "session.error", "session", schema.SeverityHigh, "opencode session error")
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
		value := record.Tokens.Input
		usage.InputTokens = &value
	}
	if record.Tokens.Output > 0 {
		value := record.Tokens.Output
		usage.OutputTokens = &value
	}
	if record.Tokens.Reasoning > 0 {
		value := record.Tokens.Reasoning
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &value}
	}
	if record.Tokens.CacheRead > 0 {
		value := record.Tokens.CacheRead
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &value}
	}
	if record.Tokens.CacheWrite > 0 {
		value := record.Tokens.CacheWrite
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &value}
	}
	if record.Tokens.CostUSD > 0 {
		value := record.Tokens.CostUSD
		usage.CostUSD = &value
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.Reasoning == nil && usage.CacheRead == nil && usage.CacheCreation == nil && usage.CostUSD == nil {
		return
	}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, "opencode token usage")
	ev.GenAI = &schema.GenAIInfo{Usage: usage}
	m.append(record, "usage", ev)
}

func classifyTool(name string, record Record) (action, category string, severity schema.Severity, message string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case toolNameHasToken(lower, "mcp"):
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "opencode MCP tool invoked"
	case toolNameHasToken(lower, "bash") || toolNameHasToken(lower, "shell") || toolNameHasToken(lower, "terminal") || lower == "powershell":
		return "command.executed", "command", schema.SeverityInfo, "opencode command executed"
	case toolNameHasToken(lower, "read") || toolNameHasToken(lower, "view") || toolNameHasToken(lower, "list") || toolNameHasToken(lower, "grep") || toolNameHasToken(lower, "glob"):
		return "file.read", "file", schema.SeverityInfo, "opencode file read"
	case toolNameHasToken(lower, "write") || toolNameHasToken(lower, "create"):
		return "file.created", "file", schema.SeverityInfo, "opencode file created"
	case toolNameHasToken(lower, "edit") || toolNameHasToken(lower, "patch") || lower == "applypatch" || lower == "multiedit":
		return "file.modified", "file", schema.SeverityInfo, "opencode file modified"
	case record.Status == "error":
		return "tool.failed", "tool", schema.SeverityHigh, "opencode tool failed"
	default:
		return "tool.completed", "tool", schema.SeverityInfo, "opencode tool completed"
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
	if path := argString(record.Args, "file_path", "filePath", "path", "target", "destination"); path != "" {
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
	if metadata := mapFromAny(record.Output); len(metadata) > 0 {
		if inner := mapFromAny(metadata["metadata"]); len(inner) > 0 {
			if code, ok := exitCodeFromAny(firstMapValue(inner, "exit", "exit_code", "exitCode", "status")); ok {
				info.ExitCode = &code
			}
		}
	}
	ev.Command = info
}

func applyFile(ev *schema.Event, name string, record Record) {
	path := ""
	if ev.Tool != nil {
		path = ev.Tool.Path
	}
	if path == "" {
		path = argString(record.Args, "file_path", "filePath", "path", "target", "destination")
	}
	operation := "modify"
	action := ev.Event.Action
	switch action {
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
	if value := argString(record.Args, "diff", "patch"); value != "" {
		return value
	}
	if value := argString(record.Args, "content", "new_string", "newString"); value != "" && classifyFileOperation(name) != "read" {
		return value
	}
	if out := mapFromAny(record.Output); len(out) > 0 {
		if value := stringFromAny(out["diff"]); value != "" {
			return value
		}
	}
	return ""
}

func classifyFileOperation(name string) string {
	lower := strings.ToLower(name)
	switch {
	case toolNameHasToken(lower, "read"), toolNameHasToken(lower, "view"), toolNameHasToken(lower, "list"):
		return "read"
	case toolNameHasToken(lower, "write"), toolNameHasToken(lower, "create"):
		return "create"
	default:
		return "modify"
	}
}

// append names the event by the record's own identity rather than by where the record happened to
// sit in this read. Order is recomputed on every sweep and is only assigned to records that map, so
// a part that becomes mappable later -- an assistant text part still streaming in, say -- renumbers
// every record after it. With the position in the id, that renumbering renamed events the log had
// already seen, and the collector's dedup could neither recognise them nor place the completion of
// a tool call beside its invocation.
func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	dedupID := fmt.Sprintf("%s:%s:%s:%s", m.ref.Kind, m.ref.ID, record.NativeID, suffix)
	ev.Event.ID = opencodeEventID(dedupID)
	m.out = append(m.out, MappedEvent{DedupID: dedupID, SourceOrder: record.Order, Event: ev})
}

var opencodeIDNamespace = [16]byte{
	0x7d, 0xa2, 0x3e, 0x0c, 0x5b, 0x7a, 0x4f, 0x22,
	0xa6, 0x5a, 0x31, 0xc2, 0x85, 0xf9, 0x11, 0x6e,
}

func opencodeEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(opencodeIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
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

func mergeRaw(existing map[string]interface{}, fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return existing
	}
	if existing == nil {
		existing = map[string]interface{}{}
	}
	nested, ok := existing["opencode"].(map[string]interface{})
	if !ok {
		nested = map[string]interface{}{}
	}
	for key, value := range fields {
		nested[key] = value
	}
	existing["opencode"] = nested
	return existing
}

func argString(args map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringFromAny(args[key]); value != "" {
			return value
		}
	}
	return ""
}

func outputString(value interface{}) string {
	if s := stringFromAny(value); s != "" {
		return s
	}
	if m := mapFromAny(value); len(m) > 0 {
		if s := stringFromAny(m["output"]); s != "" {
			return s
		}
		data, err := json.Marshal(m)
		if err == nil {
			return string(data)
		}
	}
	return ""
}
