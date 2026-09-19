package factorysession

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type MapOptions struct {
	MinLine            int
	SkipSessionStarted bool
	EmitSettingsUsage  bool
}

func MapSession(ref SessionRef, records []Record, opts MapOptions) []MappedEvent {
	m := &mapper{
		ref:         ref,
		opts:        opts,
		model:       asymptoteobserve.NormalizeModelName(ref.Model),
		pendingTool: map[string]string{},
	}
	for i := range records {
		m.consume(&records[i])
	}
	if opts.EmitSettingsUsage && ref.Settings != nil {
		m.emitSettingsUsage(len(records) + 1)
	}
	return m.out
}

type mapper struct {
	ref         SessionRef
	opts        MapOptions
	out         []MappedEvent
	model       string
	pendingTool map[string]string
	order       uint64
}

func (m *mapper) consume(record *Record) {
	emit := record.Line > m.opts.MinLine
	switch record.Type {
	case RecordSessionStart:
		if emit && !m.opts.SkipSessionStarted {
			ev := m.base(record, "session.started", "session", schema.SeverityInfo, "Factory Droid session started")
			m.append(record, "session.started", ev)
		}
	case RecordTodoState:
		return
	case RecordCompactionState:
		if emit {
			ev := m.base(record, "session.compacted", "session", schema.SeverityInfo, "Factory Droid context compacted")
			data := map[string]any{}
			if record.SummaryText != "" {
				data["summary"] = record.SummaryText
			}
			if record.SummaryTokens != nil {
				data["summary_tokens"] = *record.SummaryTokens
			}
			if record.AnchorMessage != nil && record.AnchorMessage.ID != "" {
				data["retained_from_id"] = record.AnchorMessage.ID
			}
			if len(data) > 0 {
				ev.Raw = map[string]any{"factory": data}
			}
			m.append(record, "session.compacted", ev)
		}
	case RecordMessage:
		if record.Message == nil {
			return
		}
		if record.Message.Model != "" {
			m.model = asymptoteobserve.NormalizeModelName(record.Message.Model)
		}
		if record.Message.Visibility == "user_only" {
			return
		}
		if record.Message.Visibility == "llm_only" {
			if emit {
				for _, text := range messageTexts(record.Message) {
					ev := m.base(record, "error.observed", "error", schema.SeverityLow, "Factory Droid hidden message observed")
					ev.Error = &schema.ErrorInfo{Type: "llm_only_message"}
					ev.Content = asymptoteobserve.RetainedContent(text, 0)
					ev.Raw = map[string]any{"factory_error_message": text}
					m.append(record, "llm_only.error", ev)
				}
			}
			return
		}
		if emit {
			m.emitMessage(record)
		} else {
			m.rememberToolCalls(record.Message)
		}
	}
}

func (m *mapper) emitMessage(record *Record) {
	msg := record.Message
	switch msg.Role {
	case RoleUser:
		for i, text := range messageTexts(msg) {
			ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, "Factory Droid prompt submitted")
			ev.Prompt = &schema.PromptInfo{Text: text}
			ev.Content = asymptoteobserve.RetainedContent(text, 0)
			ev.GenAI = &schema.GenAIInfo{Input: &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}}
			m.append(record, fmt.Sprintf("prompt.%d", i), ev)
		}
	case RoleAssistant:
		blocks := messageBlocks(msg)
		for i, block := range blocks {
			switch normalizedBlockType(block.Type) {
			case BlockThinking:
				text := firstNonEmpty(block.Thinking, block.Text, rawString(block.Content))
				if text == "" {
					continue
				}
				ev := m.base(record, "agent.reasoning", "agent", schema.SeverityInfo, "Factory Droid reasoning observed")
				ev.Content = asymptoteobserve.RetainedContent(text, 0)
				ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.ReasoningOutputMessages(text)}}
				m.append(record, fmt.Sprintf("reasoning.%d", i), ev)
			case BlockToolUse:
				callID := firstNonEmpty(block.ID, fmt.Sprintf("%s:tool:%d", record.ID, i))
				name := firstNonEmpty(block.Name, "unknown")
				args := parseObject(block.Input)
				m.pendingTool[callID] = name
				ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, "Factory Droid tool invoked")
				ev.Tool = &schema.ToolInfo{Name: name}
				ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{
					Name: name,
					Call: &schema.GenAIToolCallInfo{ID: callID, Arguments: args},
				}}
				ev.Raw = map[string]any{"factory_tool_input": args}
				m.append(record, fmt.Sprintf("tool.%s.invoked", callID), ev)
			case BlockToolResult:
				callID := firstNonEmpty(block.ToolUseID, block.ID, fmt.Sprintf("%s:tool:%d", record.ID, i))
				name := firstNonEmpty(m.pendingTool[callID], block.Name, "unknown")
				output := rawString(block.Content)
				ev := m.base(record, "tool.completed", "tool", schema.SeverityInfo, "Factory Droid tool completed")
				ev.Tool = &schema.ToolInfo{Name: name}
				ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{
					Name: name,
					Call: &schema.GenAIToolCallInfo{ID: callID, Result: output},
				}}
				if output != "" {
					ev.Content = asymptoteobserve.RetainedContent(output, 0)
				}
				if block.IsError {
					ev.Severity = schema.SeverityHigh
					ev.Event.Action = "tool.failed"
					ev.Message = "Factory Droid tool failed"
				}
				m.append(record, fmt.Sprintf("tool.%s.result", callID), ev)
			default:
				text := firstNonEmpty(block.Text, rawString(block.Content))
				if text == "" {
					continue
				}
				ev := m.base(record, "agent.message", "agent", schema.SeverityInfo, "Factory Droid assistant message")
				ev.Content = asymptoteobserve.RetainedContent(text, 0)
				ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}}
				m.append(record, fmt.Sprintf("assistant.%d", i), ev)
			}
		}
	case RoleTool:
		for i, text := range messageTexts(msg) {
			ev := m.base(record, "tool.completed", "tool", schema.SeverityInfo, "Factory Droid tool result")
			ev.Content = asymptoteobserve.RetainedContent(text, 0)
			m.append(record, fmt.Sprintf("tool-message.%d", i), ev)
		}
	}
}

func (m *mapper) rememberToolCalls(msg *Message) {
	for i, block := range messageBlocks(msg) {
		if normalizedBlockType(block.Type) != BlockToolUse {
			continue
		}
		callID := firstNonEmpty(block.ID, fmt.Sprintf("skipped:tool:%d", i))
		m.pendingTool[callID] = firstNonEmpty(block.Name, "unknown")
	}
}

func (m *mapper) emitSettingsUsage(line int) {
	usage := m.ref.Settings.TokenUsage
	if usage == nil {
		usage = m.ref.Settings.InclusiveTokenUsage
	}
	if usage == nil {
		return
	}
	record := &Record{Line: line, Type: "settings", Timestamp: unixMilliToRFC3339(m.ref.SettingsUnixMS)}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, "Factory Droid token usage")
	ev.GenAI = &schema.GenAIInfo{Usage: usageInfo(usage)}
	raw := map[string]any{"source": "factory_settings"}
	if m.ref.Settings.AssistantActiveTimeMs > 0 {
		raw["assistant_active_time_ms"] = m.ref.Settings.AssistantActiveTimeMs
	}
	if usage.FactoryCredits > 0 {
		raw["factory_credits"] = usage.FactoryCredits
	}
	ev.Raw = raw
	m.append(record, fmt.Sprintf("settings.usage.%d", m.ref.SettingsUnixMS), ev)
}

func (m *mapper) base(record *Record, action, category string, severity schema.Severity, message string) schema.Event {
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
	if ts := parseTime(record.Timestamp); !ts.IsZero() {
		ev.Timestamp = schema.FormatTimestamp(ts)
	}
	ev.Session = &schema.SessionInfo{ID: m.ref.ID, WorkingDirectory: m.ref.CWD}
	if m.model != "" {
		ev.Model = m.model
	}
	m.order++
	ev.Sequence = m.order
	return ev
}

func (m *mapper) append(record *Record, suffix string, ev schema.Event) {
	if ev.Raw == nil {
		ev.Raw = map[string]any{}
	}
	ev.Raw["source"] = "factory_session"
	ev.Raw["source_path"] = m.ref.Path
	ev.Raw["source_line"] = record.Line
	if record.ID != "" {
		ev.Raw["source_record_id"] = record.ID
	}
	dedupID := fmt.Sprintf("%s:%d:%s", m.ref.ID, record.Line, suffix)
	ev.Event.ID = factoryEventID(dedupID)
	m.out = append(m.out, MappedEvent{
		SourceLine: record.Line,
		DedupID:    dedupID,
		Event:      ev,
	})
}

// factoryIDNamespace is the fixed UUID namespace for Factory dedup-coordinate
// event IDs, distinct from both the content-based namespace in asymptoteobserve
// and the fx namespace so the derivations can never collide.
var factoryIDNamespace = [16]byte{
	0x5e, 0x2a, 0x3f, 0x8c, 0x9b, 0x61, 0x4b, 0x1f,
	0x6d, 0x7c, 0x0d, 0x5a, 0x14, 0x2c, 0x8e, 0x3a,
}

// factoryEventID derives a deterministic UUID v5 from a dedup coordinate so the
// same Factory record always produces the same event ID regardless of how many
// times it is read.
func factoryEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(factoryIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50 // version 5
	id[8] = (id[8] & 0x3f) | 0x80 // RFC 4122 variant
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}

func messageTexts(msg *Message) []string {
	blocks := messageBlocks(msg)
	var out []string
	for _, block := range blocks {
		if normalizedBlockType(block.Type) != BlockText {
			continue
		}
		text := firstNonEmpty(block.Text, rawString(block.Content))
		if strings.TrimSpace(text) != "" {
			out = append(out, stripUserQueryTags(text))
		}
	}
	if len(out) == 0 {
		if text := rawString(msg.Content); strings.TrimSpace(text) != "" {
			out = append(out, stripUserQueryTags(text))
		}
	}
	return out
}

func messageBlocks(msg *Message) []Block {
	if len(msg.Content) == 0 {
		return nil
	}
	if msg.Content[0] == '"' {
		var text string
		if json.Unmarshal(msg.Content, &text) == nil {
			return []Block{{Type: BlockText, Text: text}}
		}
	}
	var blocks []Block
	if json.Unmarshal(msg.Content, &blocks) == nil {
		return blocks
	}
	return nil
}

func normalizedBlockType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return BlockText
	}
	return value
}

func parseObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{"raw": string(raw)}
	}
	if v == nil {
		return map[string]any{}
	}
	return v
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []Block
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			if normalizedBlockType(part.Type) != BlockText {
				continue
			}
			if part.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(part.Text)
			}
		}
		return b.String()
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		data, _ := json.Marshal(v)
		return string(data)
	}
	return string(raw)
}

func usageInfo(usage *TokenUsage) *schema.GenAIUsageInfo {
	info := &schema.GenAIUsageInfo{}
	if usage.InputTokens > 0 {
		info.InputTokens = int64Ptr(usage.InputTokens)
	}
	if usage.OutputTokens > 0 {
		info.OutputTokens = int64Ptr(usage.OutputTokens)
	}
	if usage.CacheCreationTokens > 0 {
		info.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: int64Ptr(usage.CacheCreationTokens)}
	}
	if usage.CacheReadTokens > 0 {
		info.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(usage.CacheReadTokens)}
	}
	if usage.ThinkingTokens > 0 {
		info.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: int64Ptr(usage.ThinkingTokens)}
	}
	return info
}

func parseTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts.UTC()
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.UTC()
	}
	return time.Time{}
}

func unixMilliToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func stripUserQueryTags(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "<user_query>")
	end := strings.Index(text, "</user_query>")
	if start >= 0 && end > start {
		return strings.TrimSpace(text[start+len("<user_query>") : end])
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func int64Ptr(v int64) *int64 { return &v }
