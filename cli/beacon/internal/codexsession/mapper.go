package codexsession

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

type codexToolCall struct {
	ID    string
	Name  string
	Input interface{}
}

type mapper struct {
	ref       SessionRef
	opts      MapOptions
	out       []MappedEvent
	tools     map[string]codexToolCall
	sessionID string
	workspace string
	model     string
	turnID    string
	started   bool

	tokenRecordTurns map[string]bool
	// lastSessionTotal remembers the last cumulative total_token_usage seen
	// across the whole session so repeated snapshots are skipped while each
	// new model completion is still counted once.  The total is session-scoped
	// because Codex's total_token_usage grows across turns.
	lastSessionTotal *TokenUsage
}

func MapSession(ref SessionRef, records []Record, opts MapOptions) []MappedEvent {
	tokenRecordTurns := map[string]bool{}
	for _, r := range records {
		if r.Entry.TokenUsageRecord != nil && r.Entry.TokenUsageRecord.TurnID != "" {
			tokenRecordTurns[r.Entry.TokenUsageRecord.TurnID] = true
		}
	}
	m := &mapper{
		ref:              ref,
		opts:             opts,
		tools:            map[string]codexToolCall{},
		sessionID:        ref.ID,
		workspace:        ref.Workspace,
		tokenRecordTurns: tokenRecordTurns,
	}
	for i := range records {
		m.consume(records[i])
	}
	return m.out
}

func (m *mapper) consume(record Record) {
	entry := record.Entry
	emit := record.Line > m.opts.MinLine

	switch {
	case entry.SessionMeta != nil:
		m.consumeSessionMeta(record, emit)
	case entry.TurnContext != nil:
		m.consumeTurnContext(record)
	case entry.ResponseItem != nil:
		m.consumeResponseItem(record, emit)
	case entry.EventMessage != nil:
		m.consumeEventMessage(record, emit)
	case entry.TokenUsageRecord != nil:
		m.consumeTokenUsageRecord(record, emit)
	}
}

func (m *mapper) consumeSessionMeta(record Record, emit bool) {
	meta := record.Entry.SessionMeta
	if id := firstNonEmpty(meta.ID, meta.SessionID); id != "" {
		m.sessionID = id
	}
	if meta.CWD != "" {
		m.workspace = meta.CWD
	}
	if emit && !m.opts.SkipSessionStarted && !m.started {
		ev := m.base(record, "session.started", "session", schema.SeverityInfo, schema.FidelityObserved, "Codex session started")
		ev.Raw = map[string]interface{}{"codex_session": m.raw(record)}
		m.append(record, "session.started", ev)
		m.started = true
	}
}

func (m *mapper) consumeTurnContext(record Record) {
	ctx := record.Entry.TurnContext
	if ctx.TurnID != "" {
		m.turnID = ctx.TurnID
	}
	if ctx.CWD != "" {
		m.workspace = ctx.CWD
	}
	if ctx.Model != "" {
		m.model = asymptoteobserve.NormalizeModelName(ctx.Model)
	}
}

func (m *mapper) consumeResponseItem(record Record, emit bool) {
	item := record.Entry.ResponseItem
	switch item.Type {
	case "message":
		m.consumeMessage(record, item, emit)
	case "custom_tool_call":
		call := codexToolCall{ID: item.CallID, Name: item.Name, Input: item.Input}
		if call.ID != "" {
			m.tools[call.ID] = call
		}
		if emit {
			m.emitToolInvoked(record, call)
		}
	case "custom_tool_call_output":
		if emit {
			m.emitToolResult(record, item)
		}
	case "function_call":
		input := parseFunctionArguments(item.Arguments, item.Input)
		call := codexToolCall{ID: item.CallID, Name: item.Name, Input: input}
		if call.ID != "" {
			m.tools[call.ID] = call
		}
		if emit {
			m.emitToolInvoked(record, call)
		}
	case "function_call_output":
		if emit {
			m.emitToolResult(record, item)
		}
	}
}

func (m *mapper) consumeMessage(record Record, item *ResponseItem, emit bool) {
	if !emit {
		return
	}
	text := strings.TrimSpace(textFromContent(item.Content))
	if text == "" {
		return
	}
	switch strings.ToLower(item.Role) {
	case "user":
		m.emitPrompt(record, text)
	case "assistant":
		m.emitAgentMessage(record, text, item.ID)
	}
}

func (m *mapper) consumeEventMessage(record Record, emit bool) {
	msg := record.Entry.EventMessage
	if msg.TurnID != "" {
		m.turnID = msg.TurnID
	}
	if msg.Type == "token_count" {
		// Handled before the emit guard so the cumulative totals of records
		// already collected still inform the next sweep's deduplication.
		m.consumeTokenCount(record, msg, emit)
		return
	}
	if !emit {
		return
	}
	switch msg.Type {
	case "task_complete":
		ev := m.base(record, "session.status", "session", schema.SeverityInfo, schema.FidelityObserved, "Codex task completed")
		ev.Raw = map[string]interface{}{
			"codex_session": map[string]interface{}{
				"source_path":            recordSourcePath(m.ref),
				"line":                   record.Line,
				"payload_type":           msg.Type,
				"turn_id":                msg.TurnID,
				"duration_ms":            msg.DurationMS,
				"time_to_first_token_ms": msg.TimeToFirstTokenMS,
			},
		}
		m.append(record, "task.complete", ev)
	}
}

func (m *mapper) consumeTokenCount(record Record, msg *EventMessage, emit bool) {
	if msg.Info == nil {
		return
	}
	turnID := firstNonEmpty(msg.TurnID, m.turnID)
	total := msg.Info.TotalTokenUsage
	hadPrevious := m.lastSessionTotal != nil
	var previous TokenUsage
	if hadPrevious {
		previous = *m.lastSessionTotal
	}
	if total != nil {
		if hadPrevious && previous == *total {
			// Codex re-emits token_count without a new model completion (a
			// rate-limit refresh, for example); nothing was spent since the
			// last snapshot.
			return
		}
		t := *total
		m.lastSessionTotal = &t
	}
	if turnID != "" && m.tokenRecordTurns[turnID] {
		// token_usage_record carries the authoritative per-turn total.
		return
	}
	if !emit {
		return
	}
	snapshot := msg.Info.LastTokenUsage
	if snapshot == nil {
		// Without the per-completion snapshot, report what the cumulative
		// total grew by rather than the running total itself.
		snapshot = deltaTokenUsage(total, previous, hadPrevious)
	}
	usage := usageFromCodex(snapshot)
	if usage == nil {
		return
	}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, schema.FidelityObserved, "Codex token usage observed")
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Usage: usage})
	if ev.GenAI == nil {
		ev.GenAI = &schema.GenAIInfo{}
	}
	ev.GenAI.Conversation = &schema.GenAIConversationInfo{ID: m.sessionID}
	ev.Raw = map[string]interface{}{
		"codex_session": map[string]interface{}{
			"source_path": recordSourcePath(m.ref),
			"line":        record.Line,
			"turn_id":     msg.TurnID,
			"source":      "codex_session_token_count",
		},
	}
	m.append(record, "usage.token_count."+firstNonEmpty(msg.TurnID, itoa(record.Line)), ev)
}

func (m *mapper) consumeTokenUsageRecord(record Record, emit bool) {
	usageRecord := record.Entry.TokenUsageRecord
	if usageRecord.TurnID != "" {
		m.turnID = usageRecord.TurnID
	}
	if usageRecord.SessionID != "" {
		m.sessionID = usageRecord.SessionID
	}
	if !emit {
		return
	}
	usage := usageFromCodex(firstUsage(usageRecord.TurnTokenUsage, usageRecord.Usage))
	if usage == nil {
		return
	}
	ev := m.base(record, "token.usage", "metric", schema.SeverityInfo, schema.FidelityObserved, "Codex token usage observed")
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Usage: usage})
	if ev.GenAI == nil {
		ev.GenAI = &schema.GenAIInfo{}
	}
	ev.GenAI.Conversation = &schema.GenAIConversationInfo{ID: m.sessionID}
	if usageRecord.ResponseID != "" {
		ev.GenAI.Response = &schema.GenAIResponseInfo{ID: usageRecord.ResponseID, Model: m.model}
	}
	ev.Raw = map[string]interface{}{
		"codex_session": map[string]interface{}{
			"source_path": recordSourcePath(m.ref),
			"line":        record.Line,
			"turn_id":     usageRecord.TurnID,
			"source":      "codex_session_token_usage_record",
		},
	}
	m.append(record, "usage."+firstNonEmpty(usageRecord.TurnID, itoa(record.Line)), ev)
}

func (m *mapper) emitPrompt(record Record, text string) {
	ev := m.base(record, "prompt.submitted", "prompt", schema.SeverityInfo, schema.FidelityObserved, "Prompt submitted to Codex")
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

func (m *mapper) emitAgentMessage(record Record, text, responseID string) {
	ev := m.base(record, "agent.message", "agent", schema.SeverityInfo, schema.FidelityObserved, "Codex assistant message")
	ev.Content = contentMarker(text)
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}})
	if responseID != "" {
		ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Response: &schema.GenAIResponseInfo{ID: responseID, Model: m.model}})
	}
	m.append(record, "agent.message", ev)
}

func (m *mapper) emitToolInvoked(record Record, call codexToolCall) {
	if call.Name == "" {
		return
	}
	ev := m.base(record, "tool.invoked", "tool", schema.SeverityInfo, schema.FidelityObserved, "Codex tool invocation observed")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	ev.GenAI = mergeGenAI(ev.GenAI, &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Input}}})
	if command := commandFromTool(call); command != "" {
		ev.Tool.Command = command
	}
	m.append(record, "tool.invoked."+firstNonEmpty(call.ID, itoa(record.Line)), ev)
}

func (m *mapper) emitToolResult(record Record, item *ResponseItem) {
	call := m.tools[item.CallID]
	if call.ID == "" {
		call.ID = item.CallID
	}
	output := toolOutputText(item.Output)
	if isCommandTool(call) {
		ev := m.base(record, "command.executed", "command", severityForStatus(item.Status), schema.FidelityObserved, "Codex command completed")
		ev.Command = &schema.CommandInfo{Command: commandFromTool(call), Output: output}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Command: commandFromTool(call)}
		ev.GenAI = toolResultGenAI(call, output)
		m.append(record, "tool.result."+firstNonEmpty(call.ID, itoa(record.Line)), ev)
		return
	}
	action := "tool.completed"
	if isErrorStatus(item.Status) {
		action = "tool.failed"
	}
	ev := m.base(record, action, "tool", severityForStatus(item.Status), schema.FidelityObserved, "Codex tool completed")
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	ev.GenAI = toolResultGenAI(call, output)
	m.append(record, "tool.result."+firstNonEmpty(call.ID, itoa(record.Line)), ev)
}

func (m *mapper) base(record Record, action, category string, severity schema.Severity, fidelity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: fidelity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness:  schema.HarnessInfo{Name: Harness, CollectionMethod: schema.CollectionMethodPoll},
	})
	if ts := eventTimestamp(record.Entry); ts != "" {
		ev.Timestamp = ts
	}
	ev.Session = &schema.SessionInfo{ID: firstNonEmpty(m.sessionID, m.ref.ID), WorkingDirectory: m.workspace}
	if m.workspace != "" {
		ev.Repository = m.workspace
	}
	if m.model != "" {
		ev.Model = m.model
	}
	return ev
}

func (m *mapper) append(record Record, suffix string, ev schema.Event) {
	coordinate := firstNonEmpty(m.sessionID, m.ref.ID, m.ref.Path) + ":" + itoa(record.Line) + ":" + suffix
	ev.Event.ID = codexEventID(coordinate)
	m.out = append(m.out, MappedEvent{DedupID: ev.Event.ID, SourceLine: record.Line, Event: ev})
}

func (m *mapper) raw(record Record) map[string]interface{} {
	raw := map[string]interface{}{
		"source_path": recordSourcePath(m.ref),
		"line":        record.Line,
		"entry_type":  record.Entry.Type,
	}
	if record.Entry.SessionMeta != nil {
		raw["cli_version"] = record.Entry.SessionMeta.CLIVersion
		raw["originator"] = record.Entry.SessionMeta.Originator
		raw["source"] = record.Entry.SessionMeta.Source
	}
	return raw
}

func eventTimestamp(entry Entry) string {
	for _, value := range []string{entry.Timestamp, sessionMetaTimestamp(entry)} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return schema.FormatTimestamp(ts.UTC())
		}
	}
	return ""
}

func sessionMetaTimestamp(entry Entry) string {
	if entry.SessionMeta == nil {
		return ""
	}
	return entry.SessionMeta.Timestamp
}

func contentMarker(text string) *schema.ContentInfo {
	return asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
}

func textFromContent(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		var parts []string
		for _, raw := range v {
			block, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			switch stringValue(block["type"]) {
			case "input_text", "output_text", "text", "summary_text":
				if text := strings.TrimSpace(firstString(block, "text", "content")); text != "" {
					parts = append(parts, text)
				}
			case "reasoning", "reasoning_text":
				if text := strings.TrimSpace(firstString(block, "text", "summary")); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func toolOutputText(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, raw := range v {
			if block, ok := raw.(map[string]interface{}); ok {
				if text := strings.TrimSpace(firstString(block, "text", "content", "output")); text != "" {
					parts = append(parts, text)
				}
				continue
			}
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, s)
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

func parseFunctionArguments(args string, fallback interface{}) interface{} {
	args = strings.TrimSpace(args)
	if args == "" {
		return fallback
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return args
	}
	return parsed
}

func commandFromTool(call codexToolCall) string {
	switch v := call.Input.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]interface{}:
		return firstStringOrJoinedArray(v, "command", "cmd", "shell_command")
	default:
		if call.Input == nil {
			return ""
		}
		data, _ := json.Marshal(call.Input)
		return string(data)
	}
}

func firstStringOrJoinedArray(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		val, ok := m[key]
		if !ok {
			continue
		}
		switch v := val.(type) {
		case string:
			if v != "" {
				return v
			}
		case []interface{}:
			if s := joinStringSlice(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func joinStringSlice(arr []interface{}) string {
	parts := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func isCommandTool(call codexToolCall) bool {
	switch strings.ToLower(call.Name) {
	case "exec", "exec_command", "shell", "bash":
		return true
	default:
		return false
	}
}

func toolResultGenAI(call codexToolCall, result string) *schema.GenAIInfo {
	return &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Input, Result: result}}}
}

func usageFromCodex(raw *TokenUsage) *schema.GenAIUsageInfo {
	if raw == nil || *raw == (TokenUsage{}) {
		return nil
	}
	usage := &schema.GenAIUsageInfo{}
	uncached := raw.InputTokens - raw.CachedInputTokens - raw.CacheWriteInputTokens
	if uncached < 0 {
		uncached = 0
	}
	usage.InputTokens = &uncached
	cached := max64(raw.CachedInputTokens, 0)
	cacheWrite := max64(raw.CacheWriteInputTokens, 0)
	output := max64(raw.OutputTokens, 0)
	reasoning := max64(raw.ReasoningOutputTokens, 0)
	usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &cached}
	usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &cacheWrite}
	usage.OutputTokens = &output
	usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &reasoning}
	return usage
}

// deltaTokenUsage reports how much the cumulative total grew since the previous
// snapshot, so a session without per-completion usage is not counted as the
// running total every time it is observed.
func deltaTokenUsage(total *TokenUsage, previous TokenUsage, hadPrevious bool) *TokenUsage {
	if total == nil {
		return nil
	}
	if !hadPrevious {
		return total
	}
	delta := TokenUsage{
		InputTokens:           max64(total.InputTokens-previous.InputTokens, 0),
		CachedInputTokens:     max64(total.CachedInputTokens-previous.CachedInputTokens, 0),
		CacheWriteInputTokens: max64(total.CacheWriteInputTokens-previous.CacheWriteInputTokens, 0),
		OutputTokens:          max64(total.OutputTokens-previous.OutputTokens, 0),
		ReasoningOutputTokens: max64(total.ReasoningOutputTokens-previous.ReasoningOutputTokens, 0),
		TotalTokens:           max64(total.TotalTokens-previous.TotalTokens, 0),
	}
	return &delta
}

func firstUsage(values ...*TokenUsage) *TokenUsage {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func mergeGenAI(base, next *schema.GenAIInfo) *schema.GenAIInfo {
	if base == nil {
		return next
	}
	if next == nil {
		return base
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
	if next.Conversation != nil {
		base.Conversation = next.Conversation
	}
	return base
}

func severityForStatus(status string) schema.Severity {
	if isErrorStatus(status) {
		return schema.SeverityMedium
	}
	return schema.SeverityInfo
}

func isErrorStatus(status string) bool {
	switch strings.ToLower(status) {
	case "failed", "failure", "error":
		return true
	default:
		return false
	}
}

func recordSourcePath(ref SessionRef) string {
	return ref.Path
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func max64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
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

var codexIDNamespace = [16]byte{
	0xb6, 0xa8, 0x4d, 0x69, 0x26, 0xa6, 0x45, 0x9d,
	0x8a, 0x89, 0x1b, 0xe6, 0x65, 0x2d, 0x14, 0x5c,
}

func codexEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(codexIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}

func languageForPath(path string) string {
	return strings.TrimPrefix(filepath.Ext(path), ".")
}
