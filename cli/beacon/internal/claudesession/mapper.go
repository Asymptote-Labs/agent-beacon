package claudesession

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

type MappedEvent struct {
	DedupID    string
	SourceLine int
	Event      schema.Event
}

type MapOptions struct {
	MinLine            int
	SkipSessionStarted bool
}

type toolCall struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

type mapper struct {
	ref   SessionRef
	opts  MapOptions
	out   []MappedEvent
	tools map[string]toolCall
}

func MapSession(ref SessionRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{ref: ref, opts: opts, tools: map[string]toolCall{}}
	for i := range records {
		m.consume(records[i])
	}
	return m.out
}

func (m *mapper) consume(record Record) {
	entry := record.Entry
	emit := record.Line > m.opts.MinLine
	if emit && !m.opts.SkipSessionStarted && len(m.out) == 0 && entry.SessionID != "" {
		m.emitSessionStarted(record)
	}

	switch entry.Type {
	case "user":
		m.consumeUser(record, emit)
	case "assistant":
		m.consumeAssistant(record, emit)
	case "summary":
		if emit {
			m.emitSummary(record)
		}
	}
}

func (m *mapper) consumeUser(record Record, emit bool) {
	entry := record.Entry
	if entry.IsMeta {
		return
	}
	content := entry.Message["content"]
	if text, ok := content.(string); ok {
		text = cleanUserText(text)
		if emit && text != "" {
			m.emitPrompt(record, text)
		}
		return
	}
	blocks, ok := content.([]interface{})
	if !ok {
		return
	}
	var textParts []string
	for i, raw := range blocks {
		block, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			if text := cleanUserText(stringValue(block["text"])); text != "" {
				textParts = append(textParts, text)
			}
		case "tool_result":
			if emit {
				m.emitToolResult(record, i, block)
			}
		}
	}
	if emit {
		if text := strings.TrimSpace(strings.Join(textParts, "\n")); text != "" {
			m.emitPrompt(record, text)
		}
	}
}

func (m *mapper) consumeAssistant(record Record, emit bool) {
	entry := record.Entry
	blocks, _ := entry.Message["content"].([]interface{})
	var textParts []string
	var reasoningParts []string
	for i, raw := range blocks {
		block, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			if text := strings.TrimSpace(stringValue(block["text"])); text != "" {
				textParts = append(textParts, text)
			}
		case "thinking":
			if text := strings.TrimSpace(firstString(block, "thinking", "text")); text != "" {
				reasoningParts = append(reasoningParts, text)
			}
		case "tool_use":
			call := toolCall{
				ID:    stringValue(block["id"]),
				Name:  stringValue(block["name"]),
				Input: mapValue(block["input"]),
			}
			if call.ID != "" {
				m.tools[call.ID] = call
			}
			if emit {
				m.emitToolInvoked(record, i, call)
			}
		}
	}
	if emit {
		if text := strings.TrimSpace(strings.Join(textParts, "\n")); text != "" {
			m.emitAgentMessage(record, text)
		}
		if text := strings.TrimSpace(strings.Join(reasoningParts, "\n")); text != "" {
			m.emitAgentReasoning(record, text)
		}
		if usage := usageFromMessage(entry.Message); usage != nil {
			m.emitUsage(record, usage)
		}
	}
}

func (m *mapper) emitSessionStarted(record Record) {
	ev := m.base(record, "session.started", "session", schema.SeverityInfo, schema.FidelityObserved, "Claude Code session started")
	ev.Raw = map[string]interface{}{"claude_session": m.raw(record)}
	m.append(record, "session.started", ev)
}

func (m *mapper) emitPrompt(record Record, text string) {
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, schema.FidelityObserved, "Prompt submitted to Claude Code")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = contentMarker(text)
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Input: &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}})
	m.append(record, "prompt", ev)
	if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
		link := m.base(record, "session.handoff", "session", schema.SeverityInfo, schema.FidelityObserved, "Session continued from a "+info.SourceHarness+" session")
		link.Handoff = &info
		m.append(record, "handoff", link)
	}
}

func (m *mapper) emitAgentMessage(record Record, text string) {
	ev := m.base(record, "agent.message", "agent", schema.SeverityInfo, schema.FidelityObserved, "Claude Code assistant message")
	ev.Content = contentMarker(text)
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}})
	m.applyResponse(&ev, record.Entry)
	m.append(record, "agent.message", ev)
}

func (m *mapper) emitAgentReasoning(record Record, text string) {
	ev := m.base(record, "agent.reasoning", "agent", schema.SeverityInfo, schema.FidelityObserved, "Claude Code reasoning observed")
	ev.Content = contentMarker(text)
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.ReasoningOutputMessages(text)}})
	m.applyResponse(&ev, record.Entry)
	m.append(record, "agent.reasoning", ev)
}

func (m *mapper) emitToolInvoked(record Record, block int, call toolCall) {
	if call.Name == "" {
		return
	}
	action, category := "tool.invoked", "tool"
	ev := m.base(record, action, category, schema.SeverityInfo, schema.FidelityObserved, "Claude Code tool invocation observed")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Input}}})
	if command := commandFromTool(call); command != "" {
		ev.Tool.Command = command
	}
	if path := pathFromTool(call); path != "" {
		ev.Tool.Path = path
	}
	if server, tool := mcpFromToolName(call.Name); server != "" || tool != "" {
		ev.Event.Action = "mcp.tool_invoked"
		ev.Event.Category = "mcp"
		ev.MCP = &schema.MCPInfo{Server: server, Tool: tool}
	}
	m.append(record, "tool.invoked."+itoa(block), ev)
}

func (m *mapper) emitToolResult(record Record, block int, result map[string]interface{}) {
	callID := stringValue(result["tool_use_id"])
	call := m.tools[callID]
	if call.ID == "" {
		call.ID = callID
	}
	content := toolResultText(result["content"])
	isError, _ := result["is_error"].(bool)
	switch {
	case isCommandTool(call):
		ev := m.base(record, "command.executed", "command", severityForError(isError), schema.FidelityObserved, "Claude Code command completed")
		ev.Command = &schema.CommandInfo{Command: commandFromTool(call), Output: content}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Command: commandFromTool(call)}
		ev.GenAI = toolResultGenAI(call, content)
		m.append(record, "tool.result."+itoa(block), ev)
	case isFileTool(call):
		ev := m.base(record, fileAction(call), "file", severityForError(isError), schema.FidelityObserved, "Claude Code file activity observed")
		ev.File = &schema.FileInfo{Path: pathFromTool(call), Operation: fileOperation(call), Language: languageForPath(pathFromTool(call))}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Path: pathFromTool(call)}
		ev.GenAI = toolResultGenAI(call, content)
		m.append(record, "tool.result."+itoa(block), ev)
	default:
		action := "tool.completed"
		if isError {
			action = "tool.failed"
		}
		ev := m.base(record, action, "tool", severityForError(isError), schema.FidelityObserved, "Claude Code tool completed")
		ev.Tool = &schema.ToolInfo{Name: call.Name}
		ev.GenAI = toolResultGenAI(call, content)
		m.append(record, "tool.result."+itoa(block), ev)
	}
}

func (m *mapper) emitUsage(record Record, usage *schema.GenAIUsageInfo) {
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, schema.FidelityObserved, "Claude Code token usage observed")
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Usage: usage})
	m.applyResponse(&ev, record.Entry)
	m.append(record, "usage", ev)
}

func (m *mapper) emitSummary(record Record) {
	summary := firstString(record.Entry.Message, "summary", "content")
	if summary == "" {
		return
	}
	ev := m.base(record, "session.summary", "session", schema.SeverityInfo, schema.FidelityObserved, "Claude Code session summary observed")
	ev.Content = contentMarker(summary)
	ev.Raw = map[string]interface{}{"claude_session": map[string]interface{}{"summary": summary}}
	m.append(record, "summary", ev)
}

func (m *mapper) base(record Record, action, category string, severity schema.Severity, fidelity, message string) schema.Event {
	entry := record.Entry
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: fidelity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness:  schema.HarnessInfo{Name: Harness, CollectionMethod: schema.CollectionMethodPoll},
	})
	if ts := parseTimestamp(entry.Timestamp); ts != "" {
		ev.Timestamp = ts
	}
	sessionID := entry.SessionID
	if sessionID == "" {
		sessionID = m.ref.ID
	}
	workspace := entry.CWD
	if workspace == "" {
		workspace = m.ref.ProjectPath
	}
	ev.Session = &schema.SessionInfo{ID: sessionID, WorkingDirectory: workspace}
	if workspace != "" {
		ev.Repository = workspace
	}
	if branch := firstNonEmpty(entry.GitBranch, indexBranch(m.ref)); branch != "" {
		ev.Branch = branch
	}
	if model := stringValue(entry.Message["model"]); model != "" {
		ev.Model = asymptoteobserve.NormalizeModelName(model)
	}
	if entry.IsSidechain || m.ref.IsSidechain {
		ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Agent: m.agentInfo(entry)})
	}
	return ev
}

func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	coordinate := firstNonEmpty(record.Entry.UUID, m.ref.Path+":"+itoa(record.Line)) + ":" + suffix
	ev.Event.ID = claudeEventID(coordinate)
	m.out = append(m.out, MappedEvent{DedupID: ev.Event.ID, SourceLine: record.Line, Event: ev})
}

func (m *mapper) raw(record Record) map[string]interface{} {
	raw := map[string]interface{}{
		"source_path":  recordSourcePath(m.ref),
		"line":         record.Line,
		"version":      record.Entry.Version,
		"entrypoint":   record.Entry.Entrypoint,
		"user_type":    record.Entry.UserType,
		"is_sidechain": record.Entry.IsSidechain || m.ref.IsSidechain,
	}
	if m.ref.ParentSessionID != "" {
		raw["parent_session_id"] = m.ref.ParentSessionID
	}
	return raw
}

func (m *mapper) agentInfo(entry Entry) *schema.GenAIAgentInfo {
	agent := &schema.GenAIAgentInfo{ID: firstNonEmpty(entry.AgentID, m.ref.ID)}
	if m.ref.Meta != nil {
		agent.Name = m.ref.Meta.AgentType
		agent.Description = m.ref.Meta.Description
	}
	if entry.AttributionAgent != "" && agent.Name == "" {
		agent.Name = entry.AttributionAgent
	}
	return agent
}

func (m *mapper) applyResponse(ev *schema.Event, entry Entry) {
	if ev.GenAI == nil {
		ev.GenAI = &schema.GenAIInfo{}
	}
	if id := stringValue(entry.Message["id"]); id != "" {
		ev.GenAI.Response = &schema.GenAIResponseInfo{ID: id, Model: stringValue(entry.Message["model"])}
	}
}

func contentMarker(text string) *schema.ContentInfo {
	return asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
}

func usageFromMessage(message map[string]interface{}) *schema.GenAIUsageInfo {
	raw := mapValue(message["usage"])
	if len(raw) == 0 {
		return nil
	}
	usage := &schema.GenAIUsageInfo{}
	if v, ok := int64Value(raw["input_tokens"]); ok {
		usage.InputTokens = &v
	}
	if v, ok := int64Value(raw["output_tokens"]); ok {
		usage.OutputTokens = &v
	}
	if v, ok := int64Value(firstValue(raw, "cache_creation_input_tokens", "cache_creation_tokens")); ok {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &v}
	}
	if v, ok := int64Value(firstValue(raw, "cache_read_input_tokens", "cache_read_tokens")); ok {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &v}
	}
	if v, ok := int64Value(firstValue(raw, "reasoning_output_tokens")); ok {
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &v}
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CacheCreation == nil && usage.CacheRead == nil && usage.Reasoning == nil {
		return nil
	}
	return usage
}

func toolResultGenAI(call toolCall, result string) *schema.GenAIInfo {
	return &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Input, Result: result}}}
}

func mergeGenAI(base, next *schema.GenAIInfo) *schema.GenAIInfo {
	if base == nil {
		return next
	}
	if next == nil {
		return base
	}
	if next.Agent != nil {
		base.Agent = next.Agent
	}
	if next.Input != nil {
		base.Input = next.Input
	}
	if next.Output != nil {
		base.Output = next.Output
	}
	if next.Response != nil {
		base.Response = next.Response
	}
	if next.Tool != nil {
		base.Tool = next.Tool
	}
	if next.Usage != nil {
		base.Usage = next.Usage
	}
	return base
}

func parseTimestamp(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return schema.FormatTimestamp(ts.UTC())
	}
	return ""
}

func cleanUserText(text string) string {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "<local-command-caveat>") || strings.Contains(text, "<command-name>") {
		return ""
	}
	return text
}

func toolResultText(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, raw := range v {
			block, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if text := firstString(block, "text", "content"); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		if value == nil {
			return ""
		}
		data, _ := json.Marshal(value)
		return string(data)
	}
}

func isCommandTool(call toolCall) bool {
	switch strings.ToLower(call.Name) {
	case "bash", "shell", "terminal":
		return true
	default:
		return false
	}
}

func isFileTool(call toolCall) bool {
	switch strings.ToLower(call.Name) {
	case "read", "write", "edit", "multiedit", "notebookread", "notebookedit":
		return pathFromTool(call) != ""
	default:
		return false
	}
}

func commandFromTool(call toolCall) string {
	return firstString(call.Input, "command", "cmd", "shell_command")
}

func pathFromTool(call toolCall) string {
	return firstString(call.Input, "file_path", "filePath", "path", "notebook_path")
}

func fileAction(call toolCall) string {
	if strings.EqualFold(call.Name, "Read") || strings.EqualFold(call.Name, "NotebookRead") {
		return "file.read"
	}
	return "file.modified"
}

func fileOperation(call toolCall) string {
	switch strings.ToLower(call.Name) {
	case "read", "notebookread":
		return "read"
	case "write":
		return "create"
	default:
		return "modify"
	}
}

func languageForPath(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	return ext
}

func severityForError(isError bool) schema.Severity {
	if isError {
		return schema.SeverityMedium
	}
	return schema.SeverityInfo
}

func mcpFromToolName(name string) (string, string) {
	if !strings.HasPrefix(name, "mcp__") {
		return "", ""
	}
	rest := strings.TrimPrefix(name, "mcp__")
	if server, tool, ok := strings.Cut(rest, "__"); ok {
		return server, tool
	}
	server, tool, ok := strings.Cut(rest, "_")
	if !ok {
		return "", ""
	}
	return server, tool
}

func recordSourcePath(ref SessionRef) string {
	if ref.Path == "" {
		return ""
	}
	return ref.Path
}

func indexBranch(ref SessionRef) string {
	if ref.Index == nil {
		return ""
	}
	return ref.Index.GitBranch
}

func mapValue(value interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func stringValue(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(m[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstValue(m map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
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
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var claudeIDNamespace = [16]byte{
	0x61, 0x2f, 0x2d, 0x8a, 0x09, 0x9f, 0x44, 0x6a,
	0x93, 0x31, 0x86, 0x17, 0x55, 0x28, 0xcd, 0x42,
}

func claudeEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(claudeIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
