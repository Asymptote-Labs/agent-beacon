package asymptoteobserve

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// This file derives the chat view of a trace: TraceMessages turns a beacon.trace.v1 bundle's events
// into the messages a reader scrolls through -- what the user asked, what the assistant said and
// thought, each tool call paired with its result, approvals and lifecycle as system notes. It is
// derived from the bundle and never stored or uploaded, so the local dashboard, beacon.sh and
// anything else that holds a bundle show one trace as the same conversation.

// Message roles.
const (
	TraceRoleUser      = "user"
	TraceRoleAssistant = "assistant"
	TraceRoleSystem    = "system"
)

// Message kinds: what one message is, at a glance.
const (
	TraceMessageKindPrompt     = "prompt"
	TraceMessageKindAssistant  = "assistant_text"
	TraceMessageKindThinking   = "thinking"
	TraceMessageKindToolCall   = "tool_call"
	TraceMessageKindToolResult = "tool_result"
	TraceMessageKindApproval   = "approval"
	TraceMessageKindSystem     = "system"
	TraceMessageKindError      = "error"
)

// Part types.
const (
	TracePartText        = "text"
	TracePartThinking    = "thinking"
	TracePartToolCall    = "tool_call"
	TracePartToolResult  = "tool_result"
	TracePartError       = "error"
	TracePartSystemEvent = "system_event"
)

// Tool kinds, the toolKind of a tool_call part.
const (
	TraceToolKindTerminalCommand = "terminal_command"
	TraceToolKindRead            = "read"
	TraceToolKindEdit            = "edit"
	TraceToolKindWrite           = "write"
	TraceToolKindDelete          = "delete"
	TraceToolKindGrep            = "grep"
	TraceToolKindGlob            = "glob"
	TraceToolKindWebSearch       = "web_search"
	TraceToolKindWebFetch        = "web_fetch"
	TraceToolKindTask            = "task"
	TraceToolKindTodo            = "todo"
	TraceToolKindMCP             = "mcp"
	TraceToolKindOther           = "other"
)

// Tool result statuses.
const (
	TraceToolStatusSuccess = "success"
	TraceToolStatusError   = "error"
)

// TraceMessageV1 is one message of a trace's chat view. ID is the ID of the event it came from (a
// synthesized tool call has an ID derived from its result's), so it is stable across derivations.
type TraceMessageV1 struct {
	ID         string               `json:"id"`
	Role       string               `json:"role"`
	Kind       string               `json:"kind"`
	Order      int                  `json:"order"`
	Timestamp  string               `json:"timestamp"`
	Model      string               `json:"model,omitempty"`
	Text       string               `json:"text"`
	Parts      []TraceMessagePartV1 `json:"parts"`
	TokenUsage *TraceUsageV1        `json:"token_usage,omitempty"`
}

// TraceMessagePartV1 is one part of a message. Content is TraceTextContentV1 (text, thinking),
// *TraceToolCallContentV1, *TraceToolResultContentV1, TraceErrorContentV1, or a
// map[string]interface{} of small scalars for a system_event.
type TraceMessagePartV1 struct {
	Type    string      `json:"type"`
	Order   int         `json:"order"`
	Content interface{} `json:"content"`
}

// UnmarshalJSON decodes Content into the type its part type names, so a message survives a JSON
// round trip unchanged.
func (p *TraceMessagePartV1) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type    string          `json:"type"`
		Order   int             `json:"order"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	p.Type, p.Order, p.Content = wire.Type, wire.Order, nil
	if len(wire.Content) == 0 || string(wire.Content) == "null" {
		return nil
	}
	var err error
	switch wire.Type {
	case TracePartText, TracePartThinking:
		var c TraceTextContentV1
		err = json.Unmarshal(wire.Content, &c)
		p.Content = c
	case TracePartToolCall:
		c := &TraceToolCallContentV1{}
		err = json.Unmarshal(wire.Content, c)
		p.Content = c
	case TracePartToolResult:
		c := &TraceToolResultContentV1{}
		err = json.Unmarshal(wire.Content, c)
		p.Content = c
	case TracePartError:
		var c TraceErrorContentV1
		err = json.Unmarshal(wire.Content, &c)
		p.Content = c
	default:
		var c interface{}
		err = json.Unmarshal(wire.Content, &c)
		p.Content = c
	}
	return err
}

// TraceTextContentV1 is the content of a text or thinking part.
type TraceTextContentV1 struct {
	Text string `json:"text"`
}

// TraceToolCallContentV1 is the content of a tool_call part. Diff is the edit's retained diff when
// the runtime reported one; it lives on the call because a tool_result has no diff.
type TraceToolCallContentV1 struct {
	CallID   string                 `json:"callId"`
	ToolName string                 `json:"toolName"`
	ToolKind string                 `json:"toolKind"`
	Args     map[string]interface{} `json:"args"`
	Command  string                 `json:"command,omitempty"`
	FilePath string                 `json:"filePath,omitempty"`
	Diff     string                 `json:"diff,omitempty"`
}

// TraceToolResultContentV1 is the content of a tool_result part.
type TraceToolResultContentV1 struct {
	CallID     string `json:"callId"`
	ToolName   string `json:"toolName"`
	Output     string `json:"output"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	DurationMS *int64 `json:"durationMs,omitempty"`
}

// TraceErrorContentV1 is the content of an error part.
type TraceErrorContentV1 struct {
	Message string `json:"message"`
}

// TraceMessages derives the chat view of a trace from its events, in event order (Number, then the
// order given):
//
//   - a prompt is a user message with a text part;
//   - assistant text and reasoning are assistant messages with a text or thinking part;
//   - a tool invocation is an assistant message with a tool_call part, and its completion is a
//     separate message with a tool_result part carrying the same callId. Hook runtimes report one
//     call twice (before and after); the two are paired by tool_call_id, or, when the runtime names
//     no call id, by tool name with the most recent unanswered invocation. A call or result
//     reported twice (a hook and an OTLP copy of one action) is written once, with the second copy
//     only filling fields the first lacked. A completion with no invocation gets a tool_call
//     message of its own just before it, so every result has a call;
//   - one prompt captured by two paths (a hook and the OTLP export) is written once;
//   - approvals, policy decisions and session lifecycle are system messages with a system_event
//     part; failures that are not a tool's are system messages with an error part.
//
// Heartbeats, inventory, metric datapoints and Beacon's own bookkeeping are not messages. The
// bundle is read as it is: privacy (sanitizing, path scrubbing) is applied when the bundle is built,
// by BuildShareBundle for a shared trace.
func TraceMessages(bundle TraceBundleV1) []TraceMessageV1 {
	events := append([]TraceEventV1(nil), bundle.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Number < events[j].Number })
	b := &traceMessageBuilder{
		seen:        map[string]bool{},
		calls:       map[string]*TraceToolCallContentV1{},
		results:     map[string]*TraceToolResultContentV1{},
		callMessage: map[string]*TraceMessageV1{},
		prompts:     map[string]*TraceMessageV1{},
	}
	for _, event := range events {
		b.add(event)
	}
	out := make([]TraceMessageV1, 0, len(b.messages))
	for i, message := range b.messages {
		// Later captures can fill tool parts after the message was created. Derive its text
		// from the final parts so the flat and structured views show the same content.
		for _, part := range message.Parts {
			switch content := part.Content.(type) {
			case *TraceToolCallContentV1:
				message.Text = traceToolCallText(content)
			case *TraceToolResultContentV1:
				message.Text = traceToolResultText(content)
			}
		}
		message.Order = i + 1
		out = append(out, *message)
	}
	return out
}

type traceMessageBuilder struct {
	messages []*TraceMessageV1
	seen     map[string]bool
	// calls, results and callMessage are keyed by callId.
	calls       map[string]*TraceToolCallContentV1
	results     map[string]*TraceToolResultContentV1
	callMessage map[string]*TraceMessageV1
	// pending holds call ids of invocations that named no runtime call id, newest last, waiting for
	// a completion to pair with by tool name.
	pending []string
	// prompts holds user messages by prompt id, so one prompt captured by two paths (a hook and the
	// OTLP export) is written once. lastPrompt is the newest user message, for the copy that carries
	// no prompt id (Claude Code's UserPromptSubmit hook runs before the prompt is recorded, so it
	// has none).
	prompts    map[string]*TraceMessageV1
	lastPrompt *tracePromptRef
}

type tracePromptRef struct {
	message *TraceMessageV1
	keyed   bool
	ts      time.Time
	hasTS   bool
}

// tracePromptPairWindow is how far apart two captures of one prompt may be. The hook and the
// export record the same submission within moments of each other.
const tracePromptPairWindow = 10 * time.Second

const tracePromptNotRetained = "(prompt text not retained)"

type traceMessageClass int

const (
	traceMessageSkip traceMessageClass = iota
	traceMessagePrompt
	traceMessageAgentText
	traceMessageThinking
	traceMessageTool
	traceMessageApproval
	traceMessageSystem
	traceMessageError
)

// shareBookkeepingActions are the events Beacon writes about a session rather than the session's
// own activity: the record of a share, an auto-share request, and a commit link.
var shareBookkeepingActions = map[string]bool{
	"session.shared":          true,
	"session.share_requested": true,
	"session.commit_linked":   true,
}

// traceNoise reports Beacon's own telemetry and runtime status pings: heartbeats, inventory,
// destination health, idle/activity status, MCP tool listings and runtime detection. None of it is
// something the agent did.
func traceNoise(action, category string) bool {
	switch {
	case category == "inventory" || category == "health" || category == "telemetry" ||
		strings.HasPrefix(action, "endpoint.") || strings.HasPrefix(action, "telemetry.") ||
		strings.HasPrefix(action, "inventory.") || strings.HasPrefix(action, "destination.") ||
		strings.HasPrefix(action, "health."):
		return true
	case action == "session.activity" || action == "session.idle" || action == "session.status" ||
		action == "session.lock" || action == "mcp.tool_listed" || action == "agent.detected":
		return true
	}
	return false
}

func classifyTraceMessage(event TraceEventV1) traceMessageClass {
	action := strings.ToLower(strings.TrimSpace(event.Action))
	category := strings.ToLower(strings.TrimSpace(event.Category))
	switch {
	case action == "" || shareBookkeepingActions[action] || traceNoise(action, category):
		return traceMessageSkip
	case category == "metric" || strings.HasPrefix(action, "token.") || strings.HasPrefix(action, "metric."):
		return traceMessageSkip
	case category == "prompt" || strings.HasPrefix(action, "prompt."):
		return traceMessagePrompt
	case strings.Contains(action, "reasoning") || strings.Contains(action, "thinking"):
		return traceMessageThinking
	case action == "agent.message" || action == "assistant.message" || action == "agent.response" ||
		action == "agent.response.completed" || action == "agent.plan":
		return traceMessageAgentText
	case category == "approval" || strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return traceMessageApproval
	case action == "tool.completed" && tracePayloadlessTurnEnd(event):
		return traceMessageSkip
	case strings.HasPrefix(action, "tool.") || strings.HasPrefix(action, "command.") ||
		strings.HasPrefix(action, "file.") || action == "mcp.tool_invoked" || action == "mcp.tool_call" ||
		category == "tool" || category == "command" || category == "file":
		return traceMessageTool
	case action == "session.error" || action == "agent.error" || strings.HasPrefix(action, "error.") ||
		category == "error":
		return traceMessageError
	}
	return traceMessageSystem
}

// tracePayloadlessTurnEnd reports the Stop hook's "Agent response completed": a tool.completed that
// names no call and carries no tool, command, file or MCP payload. It marks the end of the agent's
// turn, not a tool, and as a tool it showed as an empty "Used tool" with no output.
func tracePayloadlessTurnEnd(event TraceEventV1) bool {
	return strings.TrimSpace(event.ToolCallID) == "" && event.Tool == nil && event.Command == nil &&
		event.File == nil && event.MCP == nil
}

// traceToolFamily groups tool kinds that one call can be reported as: an edit tool's call and the
// file write it produced are one call. "" is a kind that says nothing (other).
func traceToolFamily(kind string) string {
	switch kind {
	case TraceToolKindEdit, TraceToolKindWrite, TraceToolKindDelete:
		return "file"
	case TraceToolKindOther:
		return ""
	}
	return kind
}

// callKey is the key an event's call is held under. Usually the runtime's call id, but a runtime can
// report a second, different tool under the id of the call it serves: Cursor records the file it
// writes a WebSearch's output to as a Write carrying the WebSearch's tool_use_id. When the call
// already held under the id is plainly another kind of tool, this event's call is keyed apart, so
// the WebSearch keeps its own call and result instead of being folded into the Write.
func (b *traceMessageBuilder) callKey(callID string, event TraceEventV1, toolName string) string {
	call := b.calls[callID]
	if callID == "" || call == nil {
		return callID
	}
	held := traceToolFamily(call.ToolKind)
	this := traceToolFamily(TraceToolKind(toolName, event.Action, event.Category, event.MCP != nil))
	if held == "" || this == "" || held == this {
		return callID
	}
	return callID + "#" + normalizeToolName(toolName)
}

// traceToolInvocation reports an event that names a tool call before it runs.
func traceToolInvocation(event TraceEventV1) bool {
	switch strings.ToLower(strings.TrimSpace(event.Action)) {
	case "tool.invoked", "tool.started", "tool.call", "tool.execute.before", "tool.execution_start",
		"command.invoked", "command.execute.before", "mcp.tool_call":
		// The hook adapter also labels a post-tool event tool.invoked for tools it has no more
		// specific action for (WebFetch, Task, Grep, ...). That event carries the finished call's
		// result, and is a completion.
		return !traceCompletionEvidence(event)
	}
	return false
}

// traceToolCompletion reports an event that says a tool call finished: its action says so, or it
// carries what only a finished call has (a result, output, an exit code, a diff, an error).
func traceToolCompletion(event TraceEventV1) bool {
	switch strings.ToLower(strings.TrimSpace(event.Action)) {
	case "tool.completed", "tool.failed", "tool.execute.after", "tool.execution_complete", "tool.result":
		return true
	}
	if event.File != nil && event.File.Diff != nil && event.File.Diff.Text != "" {
		return true
	}
	return traceCompletionEvidence(event)
}

// traceCompletionEvidence reports an event that carries what only a finished call has: a result,
// command output or exit code, or an error. A diff is not evidence on its own, because an edit's
// invocation can carry the patch it is about to apply.
func traceCompletionEvidence(event TraceEventV1) bool {
	if event.Tool != nil && event.Tool.Result != nil {
		return true
	}
	if event.Command != nil && ((event.Command.Output != nil && event.Command.Output.Text != "") || event.Command.ExitCode != nil || event.Command.DurationMS > 0) {
		return true
	}
	return event.Error != nil && event.Error.Type != ""
}

func (b *traceMessageBuilder) add(event TraceEventV1) {
	if b.seen[event.ID] {
		return
	}
	class := classifyTraceMessage(event)
	if class == traceMessageSkip {
		return
	}
	b.seen[event.ID] = true
	switch class {
	case traceMessagePrompt:
		text := tracePromptText(event)
		key := strings.TrimSpace(event.PromptID)
		if existing := b.samePrompt(event, key, text); existing != nil {
			// The same prompt from a second capture path: fill in the text if the first copy had
			// none, and write nothing new.
			if existing.Text == tracePromptNotRetained && text != "" {
				existing.Text = text
				existing.Parts = []TraceMessagePartV1{{Type: TracePartText, Order: 1, Content: TraceTextContentV1{Text: text}}}
			}
			return
		}
		if text == "" {
			text = tracePromptNotRetained
		}
		message := b.appendText(event, TraceRoleUser, TraceMessageKindPrompt, TracePartText, text)
		if key != "" {
			b.prompts[key] = message
		}
		ts, hasTS := traceEventTime(event)
		b.lastPrompt = &tracePromptRef{message: message, keyed: key != "", ts: ts, hasTS: hasTS}
	case traceMessageAgentText:
		if text := traceAssistantText(event, "agent_message"); text != "" {
			b.appendText(event, TraceRoleAssistant, TraceMessageKindAssistant, TracePartText, text)
		}
	case traceMessageThinking:
		if text := traceAssistantText(event, "agent_reasoning"); text != "" {
			b.appendText(event, TraceRoleAssistant, TraceMessageKindThinking, TracePartThinking, text)
		}
	case traceMessageTool:
		b.addTool(event)
	case traceMessageApproval:
		b.addApproval(event)
	case traceMessageError:
		msg := traceFirstNonEmpty(event.Summary, traceErrorType(event), event.Action)
		b.append(event, TraceRoleSystem, TraceMessageKindError, msg, TraceMessagePartV1{Type: TracePartError, Content: TraceErrorContentV1{Message: msg}})
	default:
		b.addSystem(event)
	}
}

// samePrompt finds the user message an earlier capture of this prompt wrote. A prompt id match is
// exact. Without one, the newest prompt counts as the same submission only when exactly one of the
// two copies carries a prompt id (they came from different capture paths), they are within
// tracePromptPairWindow, and their texts agree or one of them retained none.
func (b *traceMessageBuilder) samePrompt(event TraceEventV1, key, text string) *TraceMessageV1 {
	if key != "" {
		if existing := b.prompts[key]; existing != nil {
			return existing
		}
	}
	last := b.lastPrompt
	ts, hasTS := traceEventTime(event)
	if last == nil || last.keyed == (key != "") || !last.hasTS || !hasTS {
		return nil
	}
	gap := ts.Sub(last.ts)
	if gap < 0 {
		gap = -gap
	}
	if gap > tracePromptPairWindow {
		return nil
	}
	if text != "" && last.message.Text != tracePromptNotRetained && text != last.message.Text {
		return nil
	}
	if key != "" {
		b.prompts[key] = last.message
	}
	last.keyed = true
	return last.message
}

func (b *traceMessageBuilder) newMessage(event TraceEventV1, id, role, kind, text string, parts ...TraceMessagePartV1) *TraceMessageV1 {
	for i := range parts {
		parts[i].Order = i + 1
	}
	message := &TraceMessageV1{
		ID:        id,
		Role:      role,
		Kind:      kind,
		Timestamp: traceMessageTimestamp(event),
		Model:     event.Model,
		Text:      text,
		Parts:     parts,
	}
	if event.Usage != nil && !traceUsageEmpty(*event.Usage) {
		usage := *event.Usage
		message.TokenUsage = &usage
	}
	return message
}

func (b *traceMessageBuilder) append(event TraceEventV1, role, kind, text string, parts ...TraceMessagePartV1) *TraceMessageV1 {
	message := b.newMessage(event, event.ID, role, kind, text, parts...)
	b.messages = append(b.messages, message)
	return message
}

func (b *traceMessageBuilder) appendText(event TraceEventV1, role, kind, partType, text string) *TraceMessageV1 {
	return b.append(event, role, kind, text, TraceMessagePartV1{Type: partType, Content: TraceTextContentV1{Text: text}})
}

func (b *traceMessageBuilder) addTool(event TraceEventV1) {
	toolName := traceMessageToolName(event)
	callID := b.callKey(strings.TrimSpace(event.ToolCallID), event, toolName)
	invocation := traceToolInvocation(event)
	explicit := invocation
	completion := !invocation && traceToolCompletion(event)

	if !invocation && !completion {
		// Neither phase is explicit (a file read a hook observed, an MCP call a transcript recorded).
		// It completes a call already on the trace, and is a call of its own otherwise.
		if callID != "" && b.calls[callID] != nil {
			completion = true
		} else if callID == "" {
			if paired := b.pairPending(event, toolName); paired != "" {
				callID = paired
				completion = true
			}
		}
		if !completion {
			invocation = true
		}
	}

	if invocation {
		if callID != "" && b.calls[callID] != nil {
			mergeTraceToolCall(b.calls[callID], traceToolCallContent(event, callID, toolName))
			return
		}
		named := callID != ""
		if !named {
			callID = event.ID
		}
		call := traceToolCallContent(event, callID, toolName)
		message := b.append(event, TraceRoleAssistant, TraceMessageKindToolCall, traceToolCallText(call), TraceMessagePartV1{Type: TracePartToolCall, Content: call})
		b.calls[callID] = call
		b.callMessage[callID] = message
		// Only an explicit invocation waits for a result. A phase-less event promoted to a call is
		// already whole; queuing it would turn the next read of the same file into its result.
		if !named && explicit {
			b.pending = append(b.pending, callID)
		}
		return
	}

	// A completion.
	if callID == "" {
		callID = b.pairPending(event, toolName)
	}
	if callID != "" && b.results[callID] != nil {
		mergeTraceToolResult(b.results[callID], traceToolResultContent(event, callID, toolName))
		if call := b.calls[callID]; call != nil {
			mergeTraceToolCall(call, traceToolCallContent(event, callID, toolName))
		}
		return
	}
	if callID == "" || b.calls[callID] == nil {
		// No invocation on the trace: the runtime reported only the finished call (a poll backfill,
		// a post-tool hook alone). Write the call just ahead of its result.
		if callID == "" {
			callID = event.ID
		}
		call := traceToolCallContent(event, callID, toolName)
		message := b.newMessage(event, eventUUID("trace-call", event.ID), TraceRoleAssistant, TraceMessageKindToolCall, traceToolCallText(call), TraceMessagePartV1{Type: TracePartToolCall, Content: call})
		message.TokenUsage = nil
		b.messages = append(b.messages, message)
		b.calls[callID] = call
		b.callMessage[callID] = message
	} else {
		// The completion often carries what the invocation did not: the diff, the command line.
		mergeTraceToolCall(b.calls[callID], traceToolCallContent(event, callID, toolName))
	}
	if toolName == "" {
		toolName = b.calls[callID].ToolName
	}
	result := traceToolResultContent(event, callID, toolName)
	b.append(event, TraceRoleAssistant, TraceMessageKindToolResult, traceToolResultText(result), TraceMessagePartV1{Type: TracePartToolResult, Content: result})
	b.results[callID] = result
}

// pairPending finds the most recent unanswered invocation without a runtime call id for the same
// tool, removes it from the queue and returns its call id.
func (b *traceMessageBuilder) pairPending(event TraceEventV1, toolName string) string {
	command, path := traceMessageCommand(event), traceMessageFilePath(event)
	for i := len(b.pending) - 1; i >= 0; i-- {
		id := b.pending[i]
		call := b.calls[id]
		if call == nil || b.results[id] != nil {
			continue
		}
		if normalizeToolName(call.ToolName) != normalizeToolName(toolName) {
			continue
		}
		if command != "" && call.Command != "" && command != call.Command {
			continue
		}
		if path != "" && call.FilePath != "" && path != call.FilePath {
			continue
		}
		b.pending = append(b.pending[:i], b.pending[i+1:]...)
		return id
	}
	return ""
}

func traceToolCallContent(event TraceEventV1, callID, toolName string) *TraceToolCallContentV1 {
	call := &TraceToolCallContentV1{
		CallID:   callID,
		ToolName: toolName,
		ToolKind: TraceToolKind(toolName, event.Action, event.Category, event.MCP != nil),
		Args:     traceToolArgs(event),
		Command:  traceMessageCommand(event),
		FilePath: traceMessageFilePath(event),
	}
	if event.File != nil && event.File.Diff != nil {
		call.Diff = event.File.Diff.Text
	}
	if call.Args == nil {
		call.Args = map[string]interface{}{}
	}
	return call
}

func traceToolResultContent(event TraceEventV1, callID, toolName string) *TraceToolResultContentV1 {
	result := &TraceToolResultContentV1{
		CallID:   callID,
		ToolName: toolName,
		Output:   traceToolOutput(event),
		Status:   TraceToolStatusSuccess,
	}
	if event.Command != nil {
		if event.Command.ExitCode != nil {
			code := *event.Command.ExitCode
			result.ExitCode = &code
		}
		if event.Command.DurationMS > 0 {
			ms := event.Command.DurationMS
			result.DurationMS = &ms
		}
	}
	if traceToolFailed(event) {
		result.Status = TraceToolStatusError
	}
	return result
}

func mergeTraceToolCall(dst, src *TraceToolCallContentV1) {
	if dst.ToolName == "" {
		dst.ToolName = src.ToolName
	}
	if dst.ToolKind == "" || dst.ToolKind == TraceToolKindOther {
		dst.ToolKind = src.ToolKind
	}
	if len(dst.Args) == 0 && len(src.Args) > 0 {
		dst.Args = src.Args
	}
	if dst.Command == "" {
		dst.Command = src.Command
	}
	if dst.FilePath == "" {
		dst.FilePath = src.FilePath
	}
	if dst.Diff == "" {
		dst.Diff = src.Diff
	}
}

func mergeTraceToolResult(dst, src *TraceToolResultContentV1) {
	if dst.ToolName == "" {
		dst.ToolName = src.ToolName
	}
	if dst.Output == "" {
		dst.Output = src.Output
	}
	if dst.ExitCode == nil {
		dst.ExitCode = src.ExitCode
	}
	if dst.DurationMS == nil {
		dst.DurationMS = src.DurationMS
	}
	if src.Status == TraceToolStatusError {
		dst.Status = TraceToolStatusError
	}
}

func (b *traceMessageBuilder) addApproval(event TraceEventV1) {
	content := map[string]interface{}{}
	action := strings.ToLower(event.Action)
	subtype := "approval"
	if strings.HasPrefix(action, "policy.") || (event.Policy != nil && event.Approval == nil) {
		subtype = "policy"
	}
	content["subtype"] = subtype
	content["action"] = event.Action
	if event.Approval != nil {
		putString(content, "decision", event.Approval.Decision)
		putString(content, "reason", event.Approval.Reason)
		if event.Approval.Required {
			content["required"] = true
		}
	}
	if event.Policy != nil {
		putString(content, "policyDecision", event.Policy.Decision)
		putString(content, "enforcement", event.Policy.Enforcement)
		putString(content, "policyName", event.Policy.Name)
		if _, ok := content["reason"]; !ok {
			putString(content, "reason", event.Policy.Reason)
		}
	}
	putString(content, "toolName", traceMessageToolName(event))
	putString(content, "callId", event.ToolCallID)
	putString(content, "command", traceMessageCommand(event))
	putString(content, "fidelity", event.Fidelity)
	text := strings.TrimSpace(strings.Join(nonEmpty(event.Action, stringField(content, "toolName"), stringField(content, "decision")), " "))
	b.append(event, TraceRoleSystem, TraceMessageKindApproval, text, TraceMessagePartV1{Type: TracePartSystemEvent, Content: content})
}

func (b *traceMessageBuilder) addSystem(event TraceEventV1) {
	content := map[string]interface{}{"subtype": event.Action}
	putString(content, "fidelity", event.Fidelity)
	summary := TruncateString(strings.TrimSpace(event.Summary), 500)
	putString(content, "summary", summary)
	b.append(event, TraceRoleSystem, TraceMessageKindSystem, traceFirstNonEmpty(summary, event.Action), TraceMessagePartV1{Type: TracePartSystemEvent, Content: content})
}

func tracePromptText(event TraceEventV1) string {
	if event.Content != nil {
		return strings.TrimSpace(event.Content.Text)
	}
	return ""
}

// traceAssistantText is the retained assistant text of an event the projection typed eventType.
// When nothing was retained the projection falls back to the event's message, which writers
// usually fill with a fixed label ("Codex assistant message"); a label is not what the assistant
// said, so it is not text.
func traceAssistantText(event TraceEventV1, eventType string) string {
	if event.Type != eventType || event.Content == nil {
		return ""
	}
	text := strings.TrimSpace(event.Content.Text)
	if text != "" && text == strings.TrimSpace(event.Summary) && looksLikeLabel(text) {
		return ""
	}
	return text
}

// looksLikeLabel recognizes the fixed descriptions writers put in event.message ("Codex assistant
// message", "Claude Code reasoning observed"), which are not what the assistant said.
func looksLikeLabel(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	for _, suffix := range []string{" observed", " message", " reasoning", " response", " completed", " captured"} {
		if strings.HasSuffix(lower, suffix) && utf8.RuneCountInString(lower) < 60 {
			return true
		}
	}
	return false
}

func traceMessageToolName(event TraceEventV1) string {
	if event.Tool != nil && strings.TrimSpace(event.Tool.Name) != "" {
		return strings.TrimSpace(event.Tool.Name)
	}
	if event.MCP != nil && strings.TrimSpace(event.MCP.Tool) != "" {
		if event.MCP.Server != "" {
			return "mcp__" + event.MCP.Server + "__" + event.MCP.Tool
		}
		return event.MCP.Tool
	}
	switch {
	case event.Command != nil && event.Command.Command != "":
		return "shell"
	case event.File != nil:
		switch strings.ToLower(event.Action) {
		case "file.read":
			return "read"
		case "file.created":
			return "write"
		case "file.deleted":
			return "delete"
		default:
			return "edit"
		}
	}
	return ""
}

func traceMessageCommand(event TraceEventV1) string {
	if event.Command != nil && strings.TrimSpace(event.Command.Command) != "" {
		return strings.TrimSpace(event.Command.Command)
	}
	if event.Tool != nil {
		return strings.TrimSpace(event.Tool.Command)
	}
	return ""
}

func traceMessageFilePath(event TraceEventV1) string {
	if event.File != nil && strings.TrimSpace(event.File.Path) != "" {
		return strings.TrimSpace(event.File.Path)
	}
	if event.Tool != nil {
		return strings.TrimSpace(event.Tool.Path)
	}
	return ""
}

// traceToolArgs returns the call's arguments when they are an object (or a JSON object in a
// string).
func traceToolArgs(event TraceEventV1) map[string]interface{} {
	if event.Tool == nil {
		return nil
	}
	return objectValue(event.Tool.Arguments)
}

func objectValue(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		if len(typed) == 0 {
			return nil
		}
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if !strings.HasPrefix(trimmed, "{") {
			return nil
		}
		var out map[string]interface{}
		if json.Unmarshal([]byte(trimmed), &out) == nil && len(out) > 0 {
			return SanitizeMap(out, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
		}
	case nil:
		return nil
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		var out map[string]interface{}
		if json.Unmarshal(data, &out) == nil && len(out) > 0 {
			return out
		}
	}
	return nil
}

func traceToolOutput(event TraceEventV1) string {
	if event.Command != nil && event.Command.Output != nil && strings.TrimSpace(event.Command.Output.Text) != "" {
		return event.Command.Output.Text
	}
	if event.Tool != nil && event.Tool.Result != nil {
		return RedactString(TruncateString(ToolResultPlainText(event.Tool.Result), DefaultStringLimit))
	}
	return ""
}

func traceToolFailed(event TraceEventV1) bool {
	action := strings.ToLower(event.Action)
	if strings.Contains(action, "fail") || strings.Contains(action, "error") || strings.Contains(action, "denied") {
		return true
	}
	if event.Error != nil && event.Error.Type != "" {
		return true
	}
	return event.Command != nil && event.Command.ExitCode != nil && *event.Command.ExitCode != 0
}

func traceErrorType(event TraceEventV1) string {
	if event.Error != nil {
		return event.Error.Type
	}
	return ""
}

func traceToolCallText(call *TraceToolCallContentV1) string {
	detail := traceFirstNonEmpty(call.Command, call.FilePath)
	if detail == "" {
		return call.ToolName
	}
	if call.ToolName == "" {
		return TruncateString(detail, 1024)
	}
	return TruncateString(call.ToolName+": "+detail, 1024)
}

func traceToolResultText(result *TraceToolResultContentV1) string {
	if result.Output != "" {
		return TruncateString(result.Output, 1024)
	}
	return strings.TrimSpace(result.ToolName + " " + result.Status)
}

// traceEventTime parses an event's timestamp.
func traceEventTime(event TraceEventV1) (time.Time, bool) {
	if ts, err := ParseTimestamp(event.Timestamp); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339Nano, event.Timestamp); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

func traceMessageTimestamp(event TraceEventV1) string {
	if ts, ok := traceEventTime(event); ok {
		return ts.UTC().Format(time.RFC3339Nano)
	}
	return event.Timestamp
}

// TraceToolKind classifies a tool for a tool_call part's toolKind, from the tool's own name first
// -- every runtime names its tools differently, so the table covers each supported runtime's
// spellings -- and then from what Beacon recorded about the call (an MCP server, a command, a file
// operation). Anything else is "other".
func TraceToolKind(toolName, action, category string, isMCP bool) string {
	name := normalizeToolName(toolName)
	if kind, ok := traceToolKindsByName[name]; ok {
		return kind
	}
	lowerAction := strings.ToLower(strings.TrimSpace(action))
	lowerCategory := strings.ToLower(strings.TrimSpace(category))
	rawLower := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case isMCP || lowerCategory == "mcp" || strings.HasPrefix(lowerAction, "mcp.") ||
		strings.HasPrefix(rawLower, "mcp__") || strings.HasPrefix(rawLower, "mcp_") || strings.HasPrefix(rawLower, "mcp."):
		return TraceToolKindMCP
	case strings.HasPrefix(lowerAction, "command.") || lowerCategory == "command":
		return TraceToolKindTerminalCommand
	case lowerAction == "file.read":
		return TraceToolKindRead
	case lowerAction == "file.created":
		return TraceToolKindWrite
	case lowerAction == "file.deleted":
		return TraceToolKindDelete
	case lowerAction == "file.modified" || lowerAction == "file.edited":
		return TraceToolKindEdit
	}
	switch {
	case name == "":
		return TraceToolKindOther
	case strings.Contains(name, "bash") || strings.Contains(name, "shell") || strings.Contains(name, "terminal"):
		return TraceToolKindTerminalCommand
	case strings.Contains(name, "websearch"):
		return TraceToolKindWebSearch
	case strings.Contains(name, "webfetch") || strings.Contains(name, "fetchurl"):
		return TraceToolKindWebFetch
	case strings.Contains(name, "todo"):
		return TraceToolKindTodo
	case strings.Contains(name, "grep"):
		return TraceToolKindGrep
	case strings.Contains(name, "glob"):
		return TraceToolKindGlob
	case strings.Contains(name, "read"):
		return TraceToolKindRead
	case strings.Contains(name, "edit") || strings.Contains(name, "patch") || strings.Contains(name, "replace"):
		return TraceToolKindEdit
	case strings.Contains(name, "write") || strings.Contains(name, "create"):
		return TraceToolKindWrite
	case strings.Contains(name, "delete"):
		return TraceToolKindDelete
	}
	return TraceToolKindOther
}

// normalizeToolName lowercases a tool name and drops separators, so exec_command, execCommand and
// exec-command compare equal. A namespace prefix some runtimes add ("functions.", "default_api:")
// is dropped first.
func normalizeToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"functions.", "default_api:", "default_api."} {
		name = strings.TrimPrefix(name, prefix)
	}
	var b strings.Builder
	for _, r := range name {
		if r == '_' || r == '-' || r == ' ' || r == '.' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// traceToolKindsByName maps normalized tool names to kinds: Claude Code, Codex CLI, Cursor, Gemini
// CLI, Qwen Code, OpenCode, Cline, the Pi family, Copilot, Kiro, OpenHands, Antigravity, DeepSeek
// Harness, Grok Build and fx spellings.
var traceToolKindsByName = func() map[string]string {
	table := map[string][]string{
		TraceToolKindTerminalCommand: {
			"bash", "shell", "execcommand", "exec", "localshell", "runterminalcmd", "runterminalcommand",
			"runcommand", "runshellcommand", "executebash", "executecommand", "terminal", "command",
			"bashoutput", "killshell", "killbash", "terminalsend", "ipython", "powershell", "cmd",
		},
		TraceToolKindRead: {
			"read", "view", "readfile", "viewfile", "cat", "notebookread", "readmanyfiles", "openfile",
			"fsread", "readimage",
		},
		TraceToolKindEdit: {
			"edit", "multiedit", "applypatch", "strreplace", "strreplaceeditor", "strreplacebasededittool",
			"searchreplace", "editfile", "replace", "notebookedit", "patch", "replaceinfile", "editnotebook",
			"fswrite",
		},
		TraceToolKindWrite: {
			"write", "create", "writefile", "createfile", "writetofile", "newfile",
		},
		TraceToolKindDelete: {
			"delete", "deletefile", "remove", "removefile", "rm",
		},
		TraceToolKindGrep: {
			"grep", "grepsearch", "search", "rg", "ripgrep", "searchfilecontent", "codebasesearch",
			"searchfiles",
		},
		TraceToolKindGlob: {
			"glob", "find", "filesearch", "findbyname", "ls", "listdir", "listdirectory", "listfiles",
		},
		TraceToolKindWebSearch: {
			"websearch", "googlesearch", "googlewebsearch", "searchweb",
		},
		TraceToolKindWebFetch: {
			"webfetch", "fetch", "fetchurl", "readurl", "urlfetch", "browse",
		},
		TraceToolKindTask: {
			"task", "agent", "subagent", "spawnagent", "dispatchagent", "delegate", "newtask", "runsubagent",
		},
		TraceToolKindTodo: {
			"todowrite", "todoread", "todo", "updateplan", "writetodos", "updatetodos", "todolist",
		},
	}
	out := map[string]string{}
	for kind, names := range table {
		for _, name := range names {
			out[name] = kind
		}
	}
	return out
}()

func putString(m map[string]interface{}, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		m[key] = value
	}
}

func stringField(m map[string]interface{}, key string) string {
	value, _ := m[key].(string)
	return value
}

func nonEmpty(values ...string) []string {
	out := values[:0:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
