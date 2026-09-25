package groksession

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// MapSession converts one Grok session directory into endpoint events.
//
// It always walks the whole session, because a record only makes sense next to the ones around it:
// a tool_result line carries the result but names its call by id, and the arguments live on an
// assistant line that may already have been collected on an earlier sweep. What opts controls is
// what gets *emitted* -- everything at or below the cursor is rebuilt for context and dropped -- so
// a session that grew since the last sweep yields only the records that grew.
func MapSession(data SessionData, opts MapOptions) []MappedEvent {
	m := &mapper{
		data:       data,
		opts:       opts,
		toolByID:   map[string]ToolCall{},
		toolOutput: map[string]string{},
	}
	for _, msg := range data.Chat {
		if msg.Type == "assistant" {
			for _, call := range msg.ToolCalls {
				m.toolByID[call.ID] = call
			}
		}
	}
	m.emitLifecycleStart()
	for _, item := range m.orderedItems() {
		switch v := item.value.(type) {
		case ChatMessage:
			m.consumeChat(v)
		case LifecycleEvent:
			m.consumeLifecycle(v)
		}
	}
	return m.out
}

type mapper struct {
	data       SessionData
	opts       MapOptions
	out        []MappedEvent
	toolByID   map[string]ToolCall
	toolOutput map[string]string
}

type orderedItem struct {
	ts    time.Time
	seq   int
	value interface{}
}

func (m *mapper) orderedItems() []orderedItem {
	items := make([]orderedItem, 0, len(m.data.Chat)+len(m.data.Lifecycle))
	for _, msg := range m.data.Chat {
		items = append(items, orderedItem{seq: msg.Index * 100, value: msg})
	}
	for _, ev := range m.data.Lifecycle {
		if ev.Type == "turn_started" {
			continue
		}
		items = append(items, orderedItem{ts: parseTime(ev.TS), seq: ev.Index*100 + 50, value: ev})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].ts.IsZero() && !items[j].ts.IsZero() && !items[i].ts.Equal(items[j].ts) {
			return items[i].ts.Before(items[j].ts)
		}
		return items[i].seq < items[j].seq
	})
	return items
}

func (m *mapper) consumeChat(msg ChatMessage) {
	switch msg.Type {
	case "user":
		text := contentText(msg.Content)
		if text == "" || msg.SyntheticReason != "" {
			return
		}
		ev := m.base("", "prompt.submitted", "prompt", schema.SeverityInfo, schema.FidelityObserved, "Grok prompt submitted")
		ev.Prompt = &schema.PromptInfo{Text: text}
		ev.Content = contentMarker(text)
		m.append(ev, SourceChat, msg.Index)
		if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
			link := m.base("", "session.handoff", "session", schema.SeverityInfo, schema.FidelityObserved, "Session continued from a "+info.SourceHarness+" session")
			link.Handoff = &info
			// The writer derives an event's id from its bytes, and a chat line carries no time, so
			// this link is stamped with the time of the sync. It names its id from where it was
			// read instead, so reading the prompt again always yields the same link.
			link.Event.ID = grokEventID(fmt.Sprintf("%s:%s:%d:handoff", m.data.Ref.ID, m.data.Ref.SourcePath, msg.Index))
			m.append(link, SourceChat, msg.Index)
		}
	case "assistant":
		model := firstNonEmpty(msg.ModelID, m.summary().CurrentModelID)
		if msg.Reasoning != nil && msg.Reasoning.Text != "" {
			ev := m.base("", "agent.reasoning", "agent", schema.SeverityInfo, schema.FidelityObserved, "Grok assistant reasoning")
			ev.Model = model
			ev.GenAI = &schema.GenAIInfo{
				Output: &schema.GenAIOutputInfo{Messages: []map[string]interface{}{{
					"role": "assistant",
					"content": []map[string]string{{
						"type": "reasoning",
						"text": msg.Reasoning.Text,
					}},
				}}},
			}
			ev.Content = contentMarker(msg.Reasoning.Text)
			m.append(ev, SourceChat, msg.Index)
		}
		if text := contentText(msg.Content); text != "" {
			ev := m.base("", "agent.message", "agent", schema.SeverityInfo, schema.FidelityObserved, "Grok assistant message")
			ev.Model = model
			ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: []map[string]interface{}{{"role": "assistant", "content": text}}}}
			ev.Content = contentMarker(text)
			m.append(ev, SourceChat, msg.Index)
		}
		for _, call := range msg.ToolCalls {
			ev := m.toolCallEvent(call, msg.Index)
			ev.Model = model
			m.append(ev, SourceChat, msg.Index)
		}
	case "tool_result":
		call := m.toolByID[msg.ToolCallID]
		output := contentText(msg.Content)
		if log := m.data.TerminalLogs[msg.ToolCallID]; log != "" {
			output = log
		}
		m.toolOutput[msg.ToolCallID] = output
		ev := m.toolResultEvent(call, msg.ToolCallID, output)
		m.append(ev, SourceChat, msg.Index)
	}
}

func (m *mapper) consumeLifecycle(ev LifecycleEvent) {
	switch ev.Type {
	case "permission_requested":
		out := m.base(ev.TS, "approval.requested", "approval", schema.SeverityInfo, schema.FidelityObserved, "Grok requested tool approval")
		out.Tool = &schema.ToolInfo{Name: ev.ToolName}
		out.Approval = &schema.ApprovalInfo{Required: true}
		m.append(out, SourceLifecycle, ev.Index)
	case "permission_resolved":
		out := m.base(ev.TS, "approval."+normalizeDecision(ev.Decision), "approval", schema.SeverityInfo, schema.FidelityObserved, "Grok resolved tool approval")
		out.Tool = &schema.ToolInfo{Name: ev.ToolName}
		out.Approval = &schema.ApprovalInfo{Required: true, Decision: normalizeDecision(ev.Decision)}
		if ev.WaitMS > 0 {
			out.Raw = map[string]interface{}{"grok": map[string]interface{}{"approval_wait_ms": ev.WaitMS}}
		}
		m.append(out, SourceLifecycle, ev.Index)
	case "tool_completed":
		// chat_history carries the call arguments and result. The lifecycle row is kept only when
		// it adds failure/status detail that a tool_result row cannot express by itself.
		if strings.EqualFold(ev.Outcome, "success") {
			return
		}
		out := m.base(ev.TS, "tool.failed", "tool", schema.SeverityHigh, schema.FidelityObserved, "Grok tool failed")
		out.Tool = &schema.ToolInfo{Name: ev.ToolName}
		if ev.DurationMS > 0 {
			out.Raw = map[string]interface{}{"grok": map[string]interface{}{"duration_ms": ev.DurationMS, "outcome": ev.Outcome}}
		}
		m.append(out, SourceLifecycle, ev.Index)
	case "yolo_toggled":
		out := m.base(ev.TS, "session.context", "session", schema.SeverityInfo, schema.FidelityObserved, "Grok YOLO mode changed")
		out.Raw = map[string]interface{}{"grok": ev}
		m.append(out, SourceLifecycle, ev.Index)
	case "turn_ended":
		// A turn is not a session. Grok writes turn_ended once per assistant turn and keeps the
		// session open -- the next prompt appends to the same chat_history.jsonl -- so mapping it
		// to session.ended would close a session repeatedly while only one session.started was
		// ever written, and every duration and still-open count computed from the log would be
		// wrong. session.context is the action the Codex and fx integrations already use for
		// session metadata that is not a lifecycle boundary. Grok exposes no session-end record at
		// all, so Beacon writes no session.ended for this runtime rather than inventing one, the
		// same posture fx and Kiro take.
		out := m.base(ev.TS, "session.context", "session", schema.SeverityInfo, schema.FidelityObserved, "Grok turn ended")
		detail := map[string]interface{}{"lifecycle": "turn_ended"}
		if ev.Outcome != "" {
			detail["outcome"] = ev.Outcome
		}
		if ev.CancellationCategory != "" {
			detail["cancellation_category"] = ev.CancellationCategory
		}
		if ev.TurnNumber != nil {
			detail["turn_number"] = *ev.TurnNumber
		}
		out.Raw = map[string]interface{}{"grok": detail}
		m.append(out, SourceLifecycle, ev.Index)
	}
}

func (m *mapper) emitLifecycleStart() {
	ts := ""
	for _, ev := range m.data.Lifecycle {
		if ev.Type == "turn_started" {
			ts = ev.TS
			break
		}
	}
	if ts == "" {
		ts = m.summary().CreatedAt
	}
	ev := m.base(ts, "session.started", "session", schema.SeverityInfo, schema.FidelityObserved, "Grok session started")
	if title := firstNonEmpty(m.summary().GeneratedTitle, m.summary().SessionSummary); title != "" {
		ev.Raw = map[string]interface{}{"grok": map[string]interface{}{"title": title}}
	}
	m.append(ev, SourceSession, 0)
}

func (m *mapper) toolCallEvent(call ToolCall, seq int) schema.Event {
	args := decodeArgs(call.Arguments)
	action := actionForTool(call.Name, false)
	ev := m.base("", action, categoryForAction(action), schema.SeverityInfo, schema.FidelityObserved, "Grok tool invoked")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	if command := stringArg(args, "command"); command != "" {
		ev.Tool.Command = command
		ev.Command = &schema.CommandInfo{Command: command}
	}
	if path := pathArg(call.Name, args); path != "" {
		ev.Tool.Path = path
		ev.File = &schema.FileInfo{Path: path, Operation: fileOperation(call.Name)}
	}
	ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{
		Name: call.Name,
		Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: args},
	}}
	return ev
}

func (m *mapper) toolResultEvent(call ToolCall, callID, output string) schema.Event {
	args := decodeArgs(call.Arguments)
	action := actionForTool(call.Name, true)
	ev := m.base("", action, categoryForAction(action), schema.SeverityInfo, schema.FidelityObserved, "Grok tool completed")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	if command := stringArg(args, "command"); command != "" {
		ev.Tool.Command = command
		ev.Command = &schema.CommandInfo{Command: command, Output: output}
		ev.Content = contentMarker(output)
	}
	if path := pathArg(call.Name, args); path != "" {
		ev.Tool.Path = path
		ev.File = &schema.FileInfo{Path: path, Operation: fileOperation(call.Name)}
	}
	ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{
		Name: call.Name,
		Call: &schema.GenAIToolCallInfo{ID: callID, Arguments: args, Result: output},
	}}
	return ev
}

func (m *mapper) base(ts, action, category string, severity schema.Severity, fidelity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: fidelity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	if ts != "" {
		ev.Timestamp = normalizeTimestamp(ts)
	}
	ev.Session = &schema.SessionInfo{ID: m.data.Ref.ID, WorkingDirectory: m.data.Ref.Workspace}
	if model := m.summary().CurrentModelID; model != "" {
		ev.Model = asymptoteobserve.NormalizeModelName(model)
	}
	if m.summary().HeadBranch != "" {
		ev.Branch = m.summary().HeadBranch
	}
	return ev
}

// append records an event unless the collector has already written it.
//
// The cursor is per source file and holds the last line written, so a line is skipped at or below
// it. session.started has no line of its own and is gated by its own flag.
func (m *mapper) append(ev schema.Event, kind SourceKind, line int) {
	if m.collected(kind, line) {
		return
	}
	m.out = append(m.out, MappedEvent{Event: ev, SourceKind: kind, SourceLine: line})
}

func (m *mapper) collected(kind SourceKind, line int) bool {
	switch kind {
	case SourceChat:
		return line <= m.opts.MinChatLine
	case SourceLifecycle:
		return line <= m.opts.MinLifecycleLine
	case SourceSession:
		return m.opts.SkipStarted
	default:
		return false
	}
}

func (m *mapper) summary() Summary {
	if m.data.Ref.Summary == nil {
		return Summary{}
	}
	return *m.data.Ref.Summary
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if part.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return ""
}

func decodeArgs(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(encoded), &out); err == nil {
			return out
		}
		return map[string]interface{}{"raw": encoded}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err == nil {
		return out
	}
	return nil
}

func actionForTool(name string, completed bool) string {
	switch strings.ToLower(name) {
	case "run_terminal_command":
		if completed {
			return "command.executed"
		}
		return "tool.invoked"
	case "read_file", "list_dir", "grep":
		if completed {
			return "file.read"
		}
		return "tool.invoked"
	case "search_replace", "write_file":
		if completed {
			return "file.modified"
		}
		return "tool.invoked"
	default:
		if completed {
			return "tool.completed"
		}
		return "tool.invoked"
	}
}

func categoryForAction(action string) string {
	switch {
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	default:
		return "tool"
	}
}

func pathArg(tool string, args map[string]interface{}) string {
	switch strings.ToLower(tool) {
	case "read_file":
		return stringArg(args, "target_file", "path", "file")
	case "list_dir":
		return stringArg(args, "target_directory", "path", "directory")
	case "grep":
		return stringArg(args, "path", "glob")
	case "search_replace", "write_file":
		return stringArg(args, "target_file", "path", "file")
	default:
		return stringArg(args, "path", "target_file", "target_directory")
	}
}

func fileOperation(tool string) string {
	switch strings.ToLower(tool) {
	case "search_replace":
		return "modify"
	case "write_file":
		return "write"
	default:
		return "read"
	}
}

func stringArg(args map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := args[key].(string); ok {
			return v
		}
	}
	return ""
}

func normalizeDecision(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "allow", "allowed", "approve", "approved":
		return "allowed"
	case "deny", "denied", "block", "blocked":
		return "denied"
	default:
		if decision == "" {
			return "unknown"
		}
		return strings.ToLower(decision)
	}
}

func contentMarker(text string) *schema.ContentInfo {
	return asymptoteobserve.RetainedContent(text, 0)
}

func normalizeTimestamp(ts string) string {
	t := parseTime(ts)
	if t.IsZero() {
		return schema.FormatTimestamp(time.Now())
	}
	return schema.FormatTimestamp(t)
}

func parseTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

var grokIDNamespace = [16]byte{
	0x6b, 0x2e, 0x91, 0x0c, 0x4f, 0xd3, 0x47, 0x58,
	0xa2, 0x1b, 0x7e, 0x93, 0x05, 0xc8, 0x3d, 0x61,
}

// grokEventID is a version 5 UUID of coordinate under grokIDNamespace.
func grokEventID(coordinate string) string {
	digest := sha1.New()
	digest.Write(grokIDNamespace[:])
	io.WriteString(digest, coordinate)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
