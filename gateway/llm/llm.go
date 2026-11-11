// Package llm is the canonical form of a model call: every gateway adapter and every protocol
// codec produces it, and every analyzer reads it. It has no dependencies.
//
// A part is classified by its type before the role of the message that carries it, because
// protocols disagree on roles: an Anthropic tool result travels in a user message, an OpenAI one
// in a tool message, and both are results.
package llm

import "strings"

// Protocol names a model API wire format.
type Protocol string

// Protocols the codecs understand.
const (
	AnthropicMessages Protocol = "anthropic.messages"
	OpenAIChat        Protocol = "openai.chat"
	OpenAIResponses   Protocol = "openai.responses"
)

// Role is who a message is from. System covers system and developer instructions wherever the
// protocol carries them (a top-level field, an instructions field, or a message).
type Role string

// Roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Request is a model request.
//
// A decoded request is read-only: a codec may share its tools (the slice, each Tool and its
// Parameters) and the parts of its system messages with other requests that resend the same
// content, so no consumer may modify them, or append to them in place.
type Request struct {
	Model  string
	Stream bool
	// Messages is the conversation in order, system instructions included as RoleSystem messages.
	// A stateful request (Stateful) carries only what is new.
	Messages []Message
	Tools    []Tool
	// Format is the text of response-shaping fields: tool_choice, response_format, a JSON schema.
	Format []string
	// Unknown names top-level fields the codec did not recognize, so new request fields surface
	// instead of disappearing.
	Unknown []string
	// Elided is the volume of the content of the Elided messages, by channel, as counting their
	// parts would have measured it; nil when no message was elided.
	Elided Inventory
	// Conversation and PreviousResponse name the provider-side state a stateful request continues
	// (OpenAI Responses' conversation and previous_response_id).
	Conversation     string
	PreviousResponse string
}

// Stateful reports that the provider holds the conversation's earlier turns, so the request
// carries only its new turn: its first message is not the conversation's, and a repeated input
// ("yes") is a new turn, not a retry.
func (r *Request) Stateful() bool { return r.Conversation != "" || r.PreviousResponse != "" }

// Response is a model reply: text, reasoning and tool calls.
type Response struct {
	ID         string
	Model      string
	Parts      []Part
	StopReason string
	Usage      Usage
}

// Usage is token accounting as the provider reported it.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
}

// Message is one turn.
type Message struct {
	Role  Role
	Parts []Part
	// Elided reports that the codec did not decode the message's content, only its role: a
	// skimmed request decodes what is new and measures the history it resends (Request.Elided).
	// Parts is nil.
	Elided bool
}

// PartKind is what a part holds.
type PartKind uint8

// Part kinds.
const (
	PartText PartKind = iota + 1
	PartReasoning
	PartMedia
	PartToolCall
	PartToolResult
)

// Part is one piece of a message. Exactly one of Text, Media, Call and Result is meaningful,
// chosen by Kind.
type Part struct {
	Kind   PartKind
	Text   string
	Media  *Media
	Call   *ToolCall
	Result *ToolResult
}

// Media is an image, document, audio clip or file. Data is the base64 payload as sent, when the
// media is inline; URL when it is referenced.
type Media struct {
	Type string // image, document, audio, file
	MIME string
	URL  string
	Data string
}

// ToolCall is a call the model made.
type ToolCall struct {
	ID   string
	Name string
	// Arguments is the call's input as sent: JSON text for function tools, free text for custom
	// tools (Codex's apply_patch).
	Arguments string
}

// ToolResult is a tool's output returned to the model.
type ToolResult struct {
	CallID  string
	Parts   []Part // text and media
	IsError bool
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Name        string
	Description string
	// Parameters is the input schema, verbatim.
	Parameters []byte
	// Kind is "function", "custom", or the provider's type for a hosted tool (web_search, ...).
	Kind string
}

// Channel is the request path content arrived on.
type Channel string

// Channels.
const (
	ChannelSystem    Channel = "System"
	ChannelTools     Channel = "Tools"
	ChannelFormat    Channel = "Format"
	ChannelUser      Channel = "User"
	ChannelAssistant Channel = "Assistant"
	ChannelResult    Channel = "Result"
	ChannelMedia     Channel = "Media"
)

// Channel classifies a part carried in a message from role: by the part's kind first, then the
// role.
func (p Part) Channel(role Role) Channel {
	switch p.Kind {
	case PartToolResult:
		return ChannelResult
	case PartMedia:
		return ChannelMedia
	case PartToolCall, PartReasoning:
		return ChannelAssistant
	}
	switch role {
	case RoleSystem:
		return ChannelSystem
	case RoleUser:
		return ChannelUser
	case RoleTool:
		return ChannelResult
	}
	return ChannelAssistant
}

// PlainText is the text a part puts in front of the model: text and reasoning as is, a tool
// call's name and arguments, a tool result's text parts. Media contributes nothing.
func (p Part) PlainText() string {
	switch p.Kind {
	case PartText, PartReasoning:
		return p.Text
	case PartToolCall:
		return p.Call.Name + " " + p.Call.Arguments
	case PartToolResult:
		return JoinText(p.Result.Parts)
	}
	return ""
}

// PlainTextLen is len(p.PlainText()), measured without building the text.
func (p Part) PlainTextLen() int {
	switch p.Kind {
	case PartText, PartReasoning:
		return len(p.Text)
	case PartToolCall:
		return len(p.Call.Name) + 1 + len(p.Call.Arguments)
	case PartToolResult:
		return joinTextLen(p.Result.Parts)
	}
	return 0
}

// JoinText joins the text parts, newline-separated.
func JoinText(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind != PartText || p.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// joinTextLen is len(JoinText(parts)).
func joinTextLen(parts []Part) int {
	n, texts := 0, 0
	for _, p := range parts {
		if p.Kind != PartText || p.Text == "" {
			continue
		}
		if texts > 0 {
			n++
		}
		n += len(p.Text)
		texts++
	}
	return n
}

// Text is the message's text parts, newline-separated.
func (m Message) Text() string { return JoinText(m.Parts) }

// TailStart is the index of the first message after the last assistant message: the part of the
// conversation that is new since the model last spoke. Clients resend the whole history on every
// call, so only the tail is new; on a conversation's first call it is every message.
func TailStart(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant {
			return i + 1
		}
	}
	return 0
}

// ToolCalls returns the tool calls among parts.
func ToolCalls(parts []Part) []ToolCall {
	var out []ToolCall
	for _, p := range parts {
		if p.Kind == PartToolCall {
			out = append(out, *p.Call)
		}
	}
	return out
}

// Volume is how much content one channel carried.
type Volume struct {
	Items int `json:"items"`
	Bytes int `json:"bytes"`
}

// Inventory is content volume by channel: what a call put in front of the model, counted by
// part. A channel is present only when it carried an item.
type Inventory map[Channel]Volume

// Add counts one item of n bytes on ch.
func (inv Inventory) Add(ch Channel, n int) {
	v := inv[ch]
	v.Items++
	v.Bytes += n
	inv[ch] = v
}

// AddInventory adds every channel of o.
func (inv Inventory) AddInventory(o Inventory) {
	for ch, ov := range o {
		v := inv[ch]
		v.Items += ov.Items
		v.Bytes += ov.Bytes
		inv[ch] = v
	}
}

// AddMessages counts the parts of msgs. An elided message has no parts; its volume is in the
// request's Elided inventory.
func (inv Inventory) AddMessages(msgs []Message) {
	for _, m := range msgs {
		for _, p := range m.Parts {
			inv.AddPart(p.Channel(m.Role), p)
		}
	}
}

// AddPart counts a part on ch: its plain text, and each media part of a tool result as one
// Media item.
func (inv Inventory) AddPart(ch Channel, p Part) {
	inv.Add(ch, p.PlainTextLen())
	if p.Kind == PartToolResult {
		for _, rp := range p.Result.Parts {
			if rp.Kind == PartMedia {
				inv.Add(ChannelMedia, 0)
			}
		}
	}
}

// AddTools counts tool definitions: name, description and parameters.
func (inv Inventory) AddTools(tools []Tool) {
	for _, t := range tools {
		inv.Add(ChannelTools, len(t.Name)+len(t.Description)+len(t.Parameters))
	}
}
