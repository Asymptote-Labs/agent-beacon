package primesession

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const Harness = "prime_agent"

type MappedEvent struct {
	SourceLine int
	Event      schema.Event
}

type MapOptions struct {
	MinLine     int
	SkipStarted bool
	SubagentOf  string
}

func MapSession(ref SessionRef, entries []Entry, opts MapOptions) []MappedEvent {
	m := &mapper{
		ref:   ref,
		opts:  opts,
		model: stringValue(ref.Header["model"]),
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

	switch typ {
	case "session":
		if id := stringValue(data["id"]); id != "" {
			m.sessionID = id
		}
		if cwd := stringValue(data["cwd"]); cwd != "" {
			m.cwd = cwd
		}
	case "model_change":
		if model := stringValue(data["modelId"]); model != "" {
			m.model = model
			if emit {
				ev := m.base(timestamp, "session.context", "session", schema.SeverityInfo, "Prime Agent model changed")
				ev.Raw = m.raw(map[string]interface{}{"change": "model_change", "model_id": model})
				m.append(entry.Line, "model_change", ev)
			}
		}
	case "compaction":
		if emit {
			ev := m.base(timestamp, "session.compacting", "session", schema.SeverityInfo, "Prime Agent compacted session history")
			ev.Raw = m.raw(compactionRaw(data))
			m.append(entry.Line, "compaction", ev)
		}
	case "custom":
		if stringValue(data["customType"]) == "prime-agent.refinement" && emit {
			ev := m.base(timestamp, "session.context", "session", schema.SeverityInfo, "Prime Agent refinement state updated")
			ev.Raw = m.raw(map[string]interface{}{"custom_type": "prime-agent.refinement", "data": data["data"]})
			m.append(entry.Line, "refinement", ev)
		}
	case "custom_message":
		if emit {
			ev := m.base(timestamp, "agent.message", "session", schema.SeverityInfo, "Prime Agent custom message")
			ev.Raw = m.raw(map[string]interface{}{"custom_message": data})
			m.append(entry.Line, "custom_message", ev)
		}
	case "message":
		message, _ := data["message"].(map[string]interface{})
		if message == nil {
			return
		}
		m.consumeMessage(entry.Line, message, timestamp, emit)
	}
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
		for i, part := range content {
			m.consumeAssistantPart(line, i, timestamp, part, emit)
		}
	case "toolresult", "tool_result", "tool":
		callID := firstNonEmpty(stringValue(message["toolCallId"]), stringValue(message["callId"]), fmt.Sprintf("line-%d", line))
		if emit {
			m.emitToolResult(line, timestamp, callID, message)
		}
	case "error":
		if emit {
			ev := m.base(timestamp, "agent.error", "session", schema.SeverityHigh, "Prime Agent error")
			text := firstNonEmpty(stringValue(message["errorMessage"]), strings.Join(texts(message["content"]), "\n"), "Unknown error")
			ev.Error = &schema.ErrorInfo{Type: "runtime_error"}
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
			ev.Raw = m.raw(map[string]interface{}{"error_message": text})
			m.append(line, "error", ev)
		}
	}
}

func (m *mapper) consumeAssistantPart(line, index int, timestamp time.Time, part interface{}, emit bool) {
	item, _ := part.(map[string]interface{})
	if item == nil {
		return
	}
	kind := strings.ToLower(stringValue(item["type"]))
	switch kind {
	case "thinking":
		text := firstNonEmpty(stringValue(item["thinking"]), stringValue(item["text"]))
		if emit && text != "" {
			ev := m.base(timestamp, "agent.reasoning", "reasoning", schema.SeverityInfo, "Prime Agent reasoning")
			ev.GenAI = ensureGenAI(ev.GenAI)
			ev.GenAI.Output = &schema.GenAIOutputInfo{Messages: []interface{}{map[string]interface{}{
				"role":  "assistant",
				"parts": []interface{}{map[string]interface{}{"type": "reasoning", "content": text}},
			}}}
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
			m.append(line, fmt.Sprintf("assistant.%d.thinking", index), ev)
		}
	case "text":
		text := stringValue(item["text"])
		if emit && text != "" {
			ev := m.base(timestamp, "agent.message", "session", schema.SeverityInfo, "Prime Agent agent message")
			ev.GenAI = ensureGenAI(ev.GenAI)
			ev.GenAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
			m.append(line, fmt.Sprintf("assistant.%d.text", index), ev)
		}
	case "toolcall", "tool_call":
		callID := firstNonEmpty(stringValue(item["id"]), fmt.Sprintf("line-%d-call-%d", line, index))
		name := firstNonEmpty(stringValue(item["name"]), "unknown")
		args, _ := item["arguments"].(map[string]interface{})
		if args == nil {
			args = map[string]interface{}{}
		}
		m.calls[callID] = toolCallState{ID: callID, Name: name, Args: args, Timestamp: timestamp}
		if emit {
			ev := m.base(timestamp, "tool.invoked", "tool", schema.SeverityInfo, "Prime Agent tool invoked")
			m.applyToolCall(&ev, m.calls[callID])
			m.append(line, fmt.Sprintf("assistant.%d.tool_call", index), ev)
		}
	}
}

func (m *mapper) emitSessionStarted() {
	ts := timestampValue(firstNonNil(m.ref.Header["timestamp"], refTimestamp(m.ref)))
	ev := m.base(ts, "session.started", "session", schema.SeverityInfo, "Prime Agent session started")
	raw := map[string]interface{}{"source_path": m.ref.Path, "source_kind": m.ref.Kind}
	if parent := stringValue(m.ref.Header["parentSession"]); parent != "" {
		raw["parent_session"] = parent
	}
	ev.Raw = m.raw(raw)
	m.append(0, "session.started", ev)
}

func (m *mapper) emitPrompt(line, index int, timestamp time.Time, text string) {
	ev := m.base(timestamp, "prompt.submitted", "prompt", schema.SeverityInfo, "Prime Agent prompt submitted")
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Input = &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}
	m.append(line, fmt.Sprintf("user.%d", index), ev)
	if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
		link := m.base(timestamp, "session.handoff", "session", schema.SeverityInfo, "Session continued from a "+info.SourceHarness+" session")
		link.Handoff = &info
		m.append(line, fmt.Sprintf("user.%d.handoff", index), link)
	}
}

func (m *mapper) emitToolResult(line int, timestamp time.Time, callID string, message map[string]interface{}) {
	state := m.calls[callID]
	if state.ID == "" {
		state = toolCallState{ID: callID, Name: firstNonEmpty(stringValue(message["toolName"]), "unknown")}
	}
	output := strings.Join(texts(message["content"]), "\n")
	failed := boolValue(message["isError"]) || strings.EqualFold(stringValue(childMap(message["details"])["status"]), "error")
	action, category, severity := "tool.completed", "tool", schema.SeverityInfo
	if failed {
		action, severity = "tool.failed", schema.SeverityHigh
	}
	if isCommandTool(state.Name, state.Args) && !failed {
		action, category = "command.executed", "command"
	}
	ev := m.base(timestamp, action, category, severity, "Prime Agent tool result")
	if failed {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
	}
	m.applyToolCall(&ev, state)
	if category == "command" {
		ev.Command = &schema.CommandInfo{Command: commandFor(state.Name, state.Args), Output: output}
	}
	if output != "" {
		if ev.Command != nil {
			ev.Command.Output = output
		}
		if ev.Content == nil {
			ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
		}
	}
	raw := map[string]interface{}{"tool_result": message}
	if strings.EqualFold(state.Name, "ipython") {
		raw["python"] = true
	}
	ev.Raw = m.raw(raw)
	m.append(line, "tool_result", ev)

	for i, attachment := range attachments(message) {
		fileEvent := m.base(timestamp, "file.read", "file", schema.SeverityInfo, "Prime Agent context attachment")
		fileEvent.File = &schema.FileInfo{Path: attachment.path, Operation: "read", Language: strings.TrimPrefix(filepath.Ext(attachment.path), ".")}
		fileEvent.Raw = m.raw(map[string]interface{}{"attachment_mime_type": attachment.mimeType, "tool_call_id": callID})
		m.append(line, fmt.Sprintf("attachment.%d", i), fileEvent)
	}
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
		ev.File = &schema.FileInfo{Path: path, Language: strings.TrimPrefix(filepath.Ext(path), ".")}
	}
	ev.GenAI = ensureGenAI(ev.GenAI)
	ev.GenAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
	ev.GenAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Args}}
	if strings.EqualFold(call.Name, "ipython") {
		ev.Raw = m.raw(map[string]interface{}{"python": true})
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
	if m.opts.SubagentOf != "" {
		fields["parent_session"] = m.opts.SubagentOf
	}
	return map[string]interface{}{"prime": fields}
}

func (m *mapper) append(line int, suffix string, ev schema.Event) {
	ev.Event.ID = primeEventID(fmt.Sprintf("%s:%s:%d:%s", m.sessionID, m.ref.Path, line, suffix))
	m.out = append(m.out, MappedEvent{SourceLine: line, Event: ev})
}

func isCommandTool(name string, args map[string]interface{}) bool {
	return strings.EqualFold(name, "ipython") || strings.EqualFold(name, "bash") || commandFor(name, args) != ""
}

func commandFor(name string, args map[string]interface{}) string {
	if strings.EqualFold(name, "ipython") {
		return stringArg(args, "code")
	}
	if strings.EqualFold(name, "bash") {
		return stringArg(args, "command")
	}
	return stringArg(args, "command", "cmd")
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

type attachment struct {
	path     string
	mimeType string
}

func attachments(message map[string]interface{}) []attachment {
	details := childMap(message["details"])
	raw, _ := details["attachments"].([]interface{})
	var out []attachment
	for _, item := range raw {
		m, _ := item.(map[string]interface{})
		path := stringValue(m["path"])
		if path == "" {
			continue
		}
		out = append(out, attachment{path: path, mimeType: stringValue(m["mimeType"])})
	}
	return out
}

func childMap(value interface{}) map[string]interface{} {
	m, _ := value.(map[string]interface{})
	if m == nil {
		return map[string]interface{}{}
	}
	return m
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
			if text := stringValue(m["text"]); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
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
			return value
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
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
		if t, err := schema.ParseTimestamp(v); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func refTimestamp(ref SessionRef) time.Time {
	if ref.ModTimeMS > 0 {
		return time.UnixMilli(ref.ModTimeMS).UTC()
	}
	return time.Now().UTC()
}

var primeIDNamespace = [16]byte{
	0x8f, 0x89, 0x37, 0x12, 0xbb, 0x46, 0x46, 0xce,
	0xb5, 0xe2, 0x17, 0x96, 0xed, 0x3c, 0xab, 0x11,
}

func primeEventID(coordinate string) string {
	digest := sha1.New()
	digest.Write(primeIDNamespace[:])
	io.WriteString(digest, coordinate)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
