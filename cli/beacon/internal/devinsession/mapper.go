package devinsession

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

// internalModelLabels are values metadata.generation_model takes on rows the compactor wrote,
// not models a turn ran on. Events derived from them carry no model, the label stays in raw,
// and they never become the session's LastModel.
var internalModelLabels = map[string]bool{"compactor": true, "summarizer": true}

// mapper converts one session's rows into events. It holds the session's cursor and mutates it
// as it works (LastModel, the Recent ring, PendingCalls); the collector journals each row's
// changes and rolls them back when that row's events fail to write, so the cursor never
// describes rows the log does not hold.
type mapper struct {
	session Session
	cursor  *Cursor
	out     []MappedEvent
	// recent is the membership view of cursor.Recent, rebuilt per mapper.
	recent map[string]bool
	// journal is the current row's undo record; see rowJournal.
	journal *rowJournal
}

// beginRow starts a journal for the row about to be mapped.
func (m *mapper) beginRow() *rowJournal {
	m.journal = &rowJournal{lastModel: m.cursor.LastModel, recentLen: len(m.cursor.Recent)}
	return m.journal
}

// rollbackRow undoes everything mapping the current row changed in the cursor, so a row whose
// events did not all reach the log is retried from exactly the state it began with.
func (m *mapper) rollbackRow(j *rowJournal) {
	m.journal = nil
	m.cursor.LastModel = j.lastModel
	// The ring only ever appends and evicts from the front, so the row's changes undo as:
	// drop the appended tail, put the evicted head back.
	base := m.cursor.Recent[:len(m.cursor.Recent)-len(j.recentAdded)]
	restored := make([]string, 0, j.recentLen)
	restored = append(restored, j.recentEvicted...)
	restored = append(restored, base...)
	m.cursor.Recent = restored
	for _, digest := range j.recentAdded {
		delete(m.recent, digest)
	}
	for _, digest := range j.recentEvicted {
		m.recent[digest] = true
	}
	for _, id := range j.pendingAdded {
		delete(m.cursor.PendingCalls, id)
	}
	for id, call := range j.pendingRemoved {
		m.cursor.PendingCalls[id] = call
	}
}

func newMapper(session Session, cursor *Cursor) *mapper {
	m := &mapper{session: session, cursor: cursor, recent: map[string]bool{}}
	for _, id := range cursor.Recent {
		m.recent[id] = true
	}
	if m.cursor.PendingCalls == nil {
		m.cursor.PendingCalls = map[string]PendingCall{}
	}
	return m
}

// mapSessionStarted is the one event that precedes the rows: it is not tied to a row, so its
// SourceRowID is 0 and collecting it does not move LastRowID.
func (m *mapper) mapSessionStarted(timestampMS int64) MappedEvent {
	ev := m.base(timestampMS, "session.started", "session", schema.SeverityInfo, "Devin CLI session started", m.fallbackModel())
	raw := map[string]interface{}{
		"title":        m.session.Title,
		"agent_mode":   m.session.AgentMode,
		"backend_type": m.session.BackendType,
		"hidden":       m.session.Hidden,
	}
	for key, field := range map[string]string{"total_acu_cost": "total_acu_cost", "total_credit_cost": "total_credit_cost"} {
		if value, ok := mapValue(m.sessionMetadata())[key]; ok && value != nil {
			raw[field] = value
		}
	}
	ev.Raw = map[string]interface{}{"devin": raw}
	return m.append(0, timestampMS, "", "session.started", ev)
}

func (m *mapper) sessionMetadata() map[string]interface{} {
	return decodeObject(m.session.Metadata)
}

// mapRow maps one row into its events, which the collector emits before advancing the cursor
// past the row. priorMessageIDs resolves the node ids the page's compaction copies point back
// at (extensions."compact/prior_node_ids"), which is what separates a resume copy re-inserting
// an old message -- same message_id as a node it names -- from a compaction summary carrying a
// new one.
func (m *mapper) mapRow(row Row, priorMessageIDs map[int64]string) []MappedEvent {
	m.out = nil
	msg := decodeObject(row.ChatMessage)
	if len(msg) == 0 {
		return nil
	}
	// Devin re-inserts history in two shapes, and both dedupe on message identity rather than
	// row_id. Rows from before message_id existed collapse onto a content-derived id (see
	// messageIDOf), because old builds re-inserted those rows wholesale too.
	messageID := messageIDOf(msg)
	// A compaction copy names the nodes it was made from: it is a duplicate only when a node it
	// names carried the same message_id (a summary of new content names priors but keeps its own
	// id). Any other re-insert is caught by the ring of recently seen ids, which the cursor
	// persists so the check survives across sweeps.
	if priors := priorNodeIDs(row); len(priors) > 0 {
		duplicate := false
		for _, nodeID := range priors {
			if priorMessageIDs[nodeID] == messageID {
				duplicate = true
				break
			}
		}
		m.recordMessageID(messageID)
		if duplicate {
			return nil
		}
	} else if m.seenMessageID(messageID) {
		return nil
	}

	role := strings.ToLower(strings.TrimSpace(stringValue(msg["role"])))
	// A third re-insert shape: an assistant row rewritten with a NEW message_id but the same
	// request_id (real data carries groups of them, usage included, so missing this double-counts
	// token.usage). The request id joins the same ring under a "req:" marker, and the message id
	// is still recorded so a later copy of exactly this row is caught either way.
	if role == "assistant" {
		if requestID := stringValue(mapValue(msg["metadata"])["request_id"]); requestID != "" {
			if m.seenMessageID("req:" + requestID) {
				m.recordMessageID(messageID)
				return nil
			}
			m.recordMessageID("req:" + requestID)
		}
	}
	switch role {
	case "user":
		m.mapUser(row, msg)
	case "assistant":
		m.mapAssistant(row, msg)
	case "tool":
		m.mapToolResult(row, msg)
	case "system":
		m.mapSystem(row, msg)
	}
	// Only conversation rows join the ring: system bookkeeping rows (rules, keepalives, the
	// compaction summary marker itself) are not what re-inserts replay.
	switch role {
	case "user", "assistant", "tool":
		m.recordMessageID(messageID)
	}
	return m.out
}

// mapUser emits a prompt only for an operator's own input. Devin also writes user rows the
// compactor feeds itself and keepalive rows, none of which carry metadata.is_user_input.
func (m *mapper) mapUser(row Row, msg map[string]interface{}) {
	meta := mapValue(msg["metadata"])
	if isUserInput, _ := meta["is_user_input"].(bool); !isUserInput {
		return
	}
	text := strings.TrimSpace(stringValue(msg["content"]))
	if text == "" {
		return
	}
	ts := rowTimestampMS(row, meta)
	ev := m.base(ts, "prompt.submitted", "prompt", schema.SeverityInfo, "Devin CLI prompt submitted", m.fallbackModel())
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	m.append(row.RowID, ts, messageIDOf(msg), "prompt.submitted", ev)
	if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
		link := m.base(ts, "session.handoff", "session", schema.SeverityInfo, "Session continued from a "+info.SourceHarness+" session", m.fallbackModel())
		link.Handoff = &info
		m.append(row.RowID, ts, messageIDOf(msg), "prompt.submitted.handoff", link)
	}
}

func (m *mapper) mapAssistant(row Row, msg map[string]interface{}) {
	meta := mapValue(msg["metadata"])
	ext := mapValue(meta["extensions"])
	ts := rowTimestampMS(row, meta)
	messageID := messageIDOf(msg)
	genModel := strings.TrimSpace(stringValue(meta["generation_model"]))

	// Rows the compactor itself produced are bookkeeping, not a reply: their content is the
	// summary text the following devin-rs/summary system row then carries. Reporting it as
	// agent.message would show a summary as something the agent said, so these rows contribute
	// only their token metrics -- with no model, since the label is not one.
	if internalModelLabels[strings.ToLower(genModel)] {
		m.mapUsage(row, msg, ts, "")
		return
	}

	subRaw := subagentRaw(ext)

	// The per-turn model is the point of reading this store: sessions.model only names the
	// latest one. A change between real generation_models is recorded as session.context before
	// the row's other events, so the context event precedes the turn it describes.
	rowModel := m.fallbackModel()
	if realModel := realGenerationModel(genModel); realModel != "" {
		rowModel = realModel
		if m.cursor.LastModel != "" && m.cursor.LastModel != realModel {
			ev := m.base(ts, "session.context", "session", schema.SeverityInfo, "Devin CLI session model changed", realModel)
			ev.Raw = map[string]interface{}{"devin": map[string]interface{}{
				"change": "model_change",
				"from":   m.cursor.LastModel,
				"to":     realModel,
			}}
			m.append(row.RowID, ts, messageID, "model_change", ev)
		}
		m.cursor.LastModel = realModel
	} else if subModel := asymptoteobserve.NormalizeModelName(stringValue(ext["subagent/model"])); subModel != "" {
		// A subagent row without its own generation_model runs on the subagent's profile model.
		rowModel = subModel
	}

	if thinking := strings.TrimSpace(stringValue(mapValue(msg["thinking"])["thinking"])); thinking != "" {
		ev := m.base(ts, "agent.reasoning", "agent", schema.SeverityInfo, "Devin CLI agent reasoning", rowModel)
		ev.Content = asymptoteobserve.RetainedContent(thinking, asymptoteobserve.DefaultStringLimit)
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.ReasoningOutputMessages(thinking)}
		})
		ev.Raw = mergeDevin(ev.Raw, subRaw)
		m.append(row.RowID, ts, messageID, "reasoning", ev)
	}

	if text := strings.TrimSpace(stringValue(msg["content"])); text != "" {
		ev := m.base(ts, "agent.message", "agent", schema.SeverityInfo, "Devin CLI assistant message", rowModel)
		ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}
		})
		ev.Raw = mergeDevin(ev.Raw, subRaw)
		if finish := stringValue(meta["finish_reason"]); finish != "" {
			ev.Raw = mergeDevin(ev.Raw, map[string]interface{}{"finish_reason": finish})
		}
		m.append(row.RowID, ts, messageID, "agent.message", ev)
	}

	for idx, call := range toolCallsOf(msg) {
		m.mapToolCall(row, msg, ts, call, idx, rowModel, subRaw)
	}

	m.mapUsage(row, msg, ts, rowModel)
}

func (m *mapper) mapToolCall(row Row, msg map[string]interface{}, ts int64, call toolCall, idx int, rowModel string, subRaw map[string]interface{}) {
	messageID := messageIDOf(msg)
	server, tool := mcpForCall(call)
	action, category, message := "tool.invoked", "tool", "Devin CLI tool invoked"
	if server != "" || tool != "" {
		action, category, message = "mcp.tool_invoked", "mcp", "Devin CLI MCP tool invoked"
	}
	ev := m.base(ts, action, category, schema.SeverityInfo, message, rowModel)
	ev.Tool = &schema.ToolInfo{Name: call.Name, Command: call.Command, Path: call.Path}
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
		genAI.Tool = &schema.GenAIToolInfo{
			Name: call.Name,
			Call: &schema.GenAIToolCallInfo{ID: call.ID, Arguments: call.Arguments},
		}
	})
	if server != "" || tool != "" {
		ev.MCP = &schema.MCPInfo{Server: server, Tool: tool}
	}
	if call.ID != "" {
		// Only the fields a later result needs are persisted -- arguments can be hundreds of
		// kilobytes for an edit or write.
		if old, ok := m.cursor.PendingCalls[call.ID]; ok {
			m.journalPendingRemoved(call.ID, old)
		} else {
			m.journal.pendingAdded = append(m.journal.pendingAdded, call.ID)
		}
		m.cursor.PendingCalls[call.ID] = PendingCall{
			ID:        call.ID,
			Name:      call.Name,
			Model:     rowModel,
			Command:   call.Command,
			Path:      call.Path,
			MCPServer: server,
			MCPTool:   tool,
		}
	}
	suffix := "tool_call." + call.ID
	if call.ID == "" {
		suffix = fmt.Sprintf("tool_call.%d", idx)
	}
	ev.Raw = mergeDevin(ev.Raw, subRaw)
	m.append(row.RowID, ts, messageID, suffix, ev)
}

func (m *mapper) mapUsage(row Row, msg map[string]interface{}, ts int64, model string) {
	meta := mapValue(msg["metadata"])
	metrics := mapValue(meta["metrics"])
	usage := &schema.GenAIUsageInfo{}
	present := false
	if v, ok := numberField(metrics, "input_tokens"); ok {
		// Devin's input count excludes cache reads and writes, the same additive shape
		// gen_ai.usage uses.
		usage.InputTokens = &v
		present = true
	}
	if v, ok := numberField(metrics, "output_tokens"); ok {
		usage.OutputTokens = &v
		present = true
	}
	if v, ok := numberField(metrics, "cache_read_tokens"); ok {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &v}
		present = true
	}
	if v, ok := numberField(metrics, "cache_creation_tokens"); ok {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &v}
		present = true
	}
	if !present {
		return
	}
	ev := m.base(ts, "token.usage", "metric", schema.SeverityInfo, "Devin CLI token usage", model)
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) { genAI.Usage = usage })
	raw := subagentRaw(mapValue(meta["extensions"]))
	raw["generation_model"] = stringValue(meta["generation_model"])
	if requestID := stringValue(meta["request_id"]); requestID != "" {
		raw["request_id"] = requestID
	}
	ev.Raw = mergeDevin(ev.Raw, raw)
	m.append(row.RowID, ts, messageIDOf(msg), "usage", ev)
}

func (m *mapper) mapToolResult(row Row, msg map[string]interface{}) {
	meta := mapValue(msg["metadata"])
	ext := mapValue(meta["extensions"])
	ts := rowTimestampMS(row, meta)
	messageID := messageIDOf(msg)
	callID := stringValue(msg["tool_call_id"])

	// The call id is how a result finds its invocation, but it is not trusted as unique: some
	// models mint ids like "read_12" or "functions.read:3" and reuse them inside a session for
	// genuinely different calls. Sequential use still resolves correctly -- a result deletes the
	// pending entry it answers -- and only genuinely unknown ids fall through to the row's own
	// telemetry for a name.
	call, known := m.cursor.PendingCalls[callID]
	if !known {
		call = PendingCall{ID: callID}
	} else {
		m.journalPendingRemoved(callID, call)
	}
	delete(m.cursor.PendingCalls, callID)
	if call.Name == "" {
		// Calls the pending map never saw (invoked before Beacon's cursor, or an id collision)
		// still carry the tool's own name in telemetry.operation.
		call.Name = stringValue(mapValue(meta["telemetry"])["operation"])
	}
	model := call.Model
	if model == "" {
		model = m.fallbackModel()
	}

	resultMeta := mapValue(ext["chisel/tool_result_meta"])
	terminal := mapValue(ext["chisel/terminal_output"])
	kind := strings.ToLower(strings.TrimSpace(stringValue(resultMeta["kind"])))
	failure := failureReason(resultMeta["failure_reason"])

	success := true
	if value, ok := resultMeta["success"]; ok {
		success = truthy(value)
	} else if failure.present {
		success = false
	}
	exitCode, hasExit := intField(mapValue(terminal["exit"]), "exit_code")
	failed := !success || (hasExit && exitCode != 0)

	severity := schema.SeverityInfo
	if failed {
		severity = schema.SeverityMedium
	}

	content := stringValue(msg["content"])
	// Only a call whose recorded arguments carry the command is a command event: Devin's
	// execute-kind tools (get_output and friends) have no command argument, so classifying by
	// kind or name alone produced command.executed events with an empty command on real data.
	switch {
	case call.Command != "":
		ev := m.base(ts, "command.executed", "command", severity, "Devin CLI command executed", model)
		ev.Command = &schema.CommandInfo{Command: call.Command, Output: content}
		if hasExit {
			ev.Command.ExitCode = &exitCode
		}
		if failed {
			ev.Error = &schema.ErrorInfo{Type: errorType(call, failure, hasExit, exitCode)}
		}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Command: call.Command}
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: callID, Result: content}}
		})
		ev.Content = asymptoteobserve.RetainedContent(content, asymptoteobserve.DefaultStringLimit)
		ev.Raw = mergeDevin(ev.Raw, m.resultRaw(kind, resultMeta, terminal, ext, hasExit, exitCode))
		m.append(row.RowID, ts, messageID, "tool_result", ev)
	case kind == "read" || strings.EqualFold(call.Name, "read"):
		ev := m.base(ts, "file.read", "file", severity, "Devin CLI file read", model)
		ev.File = &schema.FileInfo{Path: call.Path, Operation: "read", Language: languageForPath(call.Path)}
		if failed {
			ev.Error = &schema.ErrorInfo{Type: errorType(call, failure, hasExit, exitCode)}
		}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Path: call.Path}
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: callID, Result: content}}
		})
		ev.Content = asymptoteobserve.RetainedContent(content, asymptoteobserve.DefaultStringLimit)
		ev.Raw = mergeDevin(ev.Raw, m.resultRaw(kind, resultMeta, terminal, ext, hasExit, exitCode))
		m.append(row.RowID, ts, messageID, "tool_result", ev)
	case kind == "edit" || strings.EqualFold(call.Name, "edit") || strings.EqualFold(call.Name, "write"):
		ev := m.base(ts, "file.modified", "file", severity, "Devin CLI file modified", model)
		ev.File = &schema.FileInfo{Path: call.Path, Operation: "modify", Language: languageForPath(call.Path)}
		if failed {
			ev.Error = &schema.ErrorInfo{Type: errorType(call, failure, hasExit, exitCode)}
		}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Path: call.Path}
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: callID, Result: content}}
		})
		ev.Content = asymptoteobserve.RetainedContent(content, asymptoteobserve.DefaultStringLimit)
		ev.Raw = mergeDevin(ev.Raw, m.resultRaw(kind, resultMeta, terminal, ext, hasExit, exitCode))
		m.append(row.RowID, ts, messageID, "tool_result", ev)
	default:
		action := "tool.completed"
		message := "Devin CLI tool completed"
		if failed {
			action = "tool.failed"
			message = "Devin CLI tool failed"
		}
		ev := m.base(ts, action, "tool", severity, message, model)
		if failed {
			ev.Error = &schema.ErrorInfo{Type: errorType(call, failure, hasExit, exitCode)}
		}
		if call.MCPServer != "" || call.MCPTool != "" {
			ev.MCP = &schema.MCPInfo{Server: call.MCPServer, Tool: call.MCPTool}
		}
		ev.Tool = &schema.ToolInfo{Name: call.Name, Command: call.Command, Path: call.Path}
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: callID, Result: content}}
		})
		ev.Content = asymptoteobserve.RetainedContent(content, asymptoteobserve.DefaultStringLimit)
		ev.Raw = mergeDevin(ev.Raw, m.resultRaw(kind, resultMeta, terminal, ext, hasExit, exitCode))
		m.append(row.RowID, ts, messageID, "tool_result", ev)
	}

	// An operator's rejection of the call is a second fact the tool result alone does not
	// carry, so it gets its own event the way a denied approval does on the hook path.
	if failure.rejected {
		ev := m.base(ts, "approval.denied", "approval", schema.SeverityMedium, "Devin CLI tool call rejected", model)
		reason := "rejected without prompting"
		if failure.interactive {
			reason = "operator rejected the tool call"
		}
		ev.Approval = &schema.ApprovalInfo{Required: true, Decision: "deny", Reason: reason}
		ev.Tool = &schema.ToolInfo{Name: call.Name}
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Tool = &schema.GenAIToolInfo{Name: call.Name, Call: &schema.GenAIToolCallInfo{ID: callID, Result: content}}
		})
		ev.Raw = map[string]interface{}{"devin": map[string]interface{}{"rejected_interactive": failure.interactive}}
		m.append(row.RowID, ts, messageID, "approval", ev)
	}
}

// mapSystem emits only for the compaction summary row: the system row whose extensions carry
// devin-rs/summary. Every other system row (prompts, rules, keepalives) is bookkeeping the
// endpoint does not model.
func (m *mapper) mapSystem(row Row, msg map[string]interface{}) {
	meta := mapValue(msg["metadata"])
	ext := mapValue(meta["extensions"])
	summary := mapValue(ext["devin-rs/summary"])
	if len(summary) == 0 {
		return
	}
	ts := rowTimestampMS(row, meta)
	text := strings.TrimSpace(stringValue(msg["content"]))
	ev := m.base(ts, "session.compacted", "session", schema.SeverityInfo, "Devin CLI session compacted", m.fallbackModel())
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	raw := map[string]interface{}{"summary_source": stringValue(summary["source"])}
	if text != "" {
		// The content marker is only a marker; the summary text itself goes in raw, as the
		// other collectors do for summaries.
		raw["summary"] = text
	}
	if summarized, ok := intField(nodeMetadata(row), "summarized_from"); ok {
		raw["summarized_from"] = summarized
	}
	ev.Raw = map[string]interface{}{"devin": raw}
	m.append(row.RowID, ts, messageIDOf(msg), "compacted", ev)
}

func (m *mapper) base(timestampMS int64, action, category string, severity schema.Severity, message, model string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Fidelity: schema.FidelityObserved,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	if timestampMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(timestampMS))
	}
	ev.Session = &schema.SessionInfo{ID: m.session.ID, WorkingDirectory: m.session.WorkingDirectory}
	if model != "" {
		ev.Model = model
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Request = &schema.GenAIRequestInfo{Model: model}
		})
	}
	return ev
}

func (m *mapper) append(sourceRowID int64, timestampMS int64, messageID, suffix string, ev schema.Event) MappedEvent {
	// The id is keyed on message_id, not row_id: Devin's resume and compaction copies reuse the
	// message's own id, so a retried sweep and a re-inserted row produce the same event id.
	// Determinism is what the consumers rely on -- the Beacon ingest side and `beacon share`
	// re-uploads treat equal ids as one event; the local writer's own heuristic (matching tool
	// call ids, or near-identical timing across capture paths) is a separate check.
	ev.Event.ID = devinEventID(fmt.Sprintf("%s:%s:%s", m.session.ID, messageID, suffix))
	item := MappedEvent{SourceRowID: sourceRowID, TimestampMS: timestampMS, Event: ev}
	m.out = append(m.out, item)
	return item
}

// fallbackModel is the model for events that carry none of their own: the last real
// generation_model the session produced, else the model the session row advertises.
func (m *mapper) fallbackModel() string {
	if m.cursor.LastModel != "" {
		return m.cursor.LastModel
	}
	return asymptoteobserve.NormalizeModelName(m.session.Model)
}

// subagentRaw is the provenance a subagent's own row carries: which subagent produced it and
// under which profile.
func subagentRaw(ext map[string]interface{}) map[string]interface{} {
	raw := map[string]interface{}{}
	if id := stringValue(ext["subagent/agent_id"]); id != "" {
		raw["subagent_id"] = id
	}
	if profile := stringValue(ext["subagent/profile_name"]); profile != "" {
		raw["subagent_profile"] = profile
	}
	return raw
}

func (m *mapper) resultRaw(kind string, resultMeta, terminal, ext map[string]interface{}, hasExit bool, exitCode int) map[string]interface{} {
	raw := map[string]interface{}{}
	if kind != "" {
		raw["kind"] = kind
	}
	if failure, present := resultMeta["failure_reason"]; present && failure != nil {
		raw["failure_reason"] = failure
	}
	if hasExit {
		raw["exit_code"] = exitCode
	}
	if approved := stringValue(ext["chisel/approved_by"]); approved != "" {
		raw["approved_by"] = approved
	}
	if cwd := stringValue(terminal["cwd"]); cwd != "" {
		raw["cwd"] = cwd
	}
	return raw
}

// messageIDDigest is what the ring stores: 16 hex chars of sha256, which is what the cursor's
// recent_message_ids entries are.
func messageIDDigest(messageID string) string {
	sum := sha256.Sum256([]byte(messageID))
	return hex.EncodeToString(sum[:])[:16]
}

func (m *mapper) seenMessageID(messageID string) bool {
	return m.recent[messageIDDigest(messageID)]
}

func (m *mapper) recordMessageID(messageID string) {
	if messageID == "" {
		return
	}
	digest := messageIDDigest(messageID)
	if m.recent[digest] {
		return
	}
	m.recent[digest] = true
	if m.journal != nil {
		m.journal.recentAdded = append(m.journal.recentAdded, digest)
	}
	m.cursor.Recent = append(m.cursor.Recent, digest)
	if len(m.cursor.Recent) > recentMessageIDLimit {
		for _, dropped := range m.cursor.Recent[:len(m.cursor.Recent)-recentMessageIDLimit] {
			delete(m.recent, dropped)
			if m.journal != nil {
				m.journal.recentEvicted = append(m.journal.recentEvicted, dropped)
			}
		}
		m.cursor.Recent = append([]string(nil), m.cursor.Recent[len(m.cursor.Recent)-recentMessageIDLimit:]...)
	}
}

// journalPendingRemoved remembers a pending call the current row took out of the map -- either
// a result resolving its call or an invocation overwriting one -- so a rollback can restore it.
func (m *mapper) journalPendingRemoved(id string, call PendingCall) {
	if m.journal == nil {
		return
	}
	if m.journal.pendingRemoved == nil {
		m.journal.pendingRemoved = map[string]PendingCall{}
	}
	if _, recorded := m.journal.pendingRemoved[id]; !recorded {
		m.journal.pendingRemoved[id] = call
	}
}

// realGenerationModel is the normalized model a row's generation_model names, or empty when it
// is absent or an internal label rather than a model.
func realGenerationModel(genModel string) string {
	if genModel == "" || internalModelLabels[strings.ToLower(genModel)] {
		return ""
	}
	return asymptoteobserve.NormalizeModelName(genModel)
}

// toolCall is one entry of a chat_message's tool_calls array.
type toolCall struct {
	ID        string
	Name      string
	Arguments map[string]interface{}
	Command   string
	Path      string
}

func toolCallsOf(msg map[string]interface{}) []toolCall {
	raw, ok := msg["tool_calls"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]toolCall, 0, len(raw))
	for _, item := range raw {
		entry := mapValue(item)
		if len(entry) == 0 {
			continue
		}
		args := decodeArguments(entry["arguments"])
		call := toolCall{
			ID:        firstNonEmpty(stringValue(entry["id"]), stringValue(entry["tool_call_id"])),
			Name:      firstNonEmpty(stringValue(entry["name"]), stringValue(mapValue(entry["function"])["name"])),
			Arguments: args,
			Command:   stringValue(args["command"]),
			Path:      firstNonEmpty(stringValue(args["file_path"]), stringValue(args["path"])),
		}
		out = append(out, call)
	}
	return out
}

// mcpForCall names the MCP server and tool a call reaches: either a native mcp__server__tool
// name or the mcp_call_tool adapter carrying them in its arguments.
func mcpForCall(call toolCall) (server, tool string) {
	if call.Name == "mcp_call_tool" {
		return stringValue(call.Arguments["server_name"]), stringValue(call.Arguments["tool_name"])
	}
	if rest, ok := strings.CutPrefix(call.Name, "mcp__"); ok {
		parts := strings.SplitN(rest, "__", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
	}
	return "", ""
}

// failureReason is a decoded chisel/tool_result_meta failure_reason: either a plain label
// ("ToolError", "ValidationError", "Canceled") or the rejection object {"Rejected":{"interactive":…}}.
type failureInfo struct {
	present     bool
	label       string
	rejected    bool
	interactive bool
}

func failureReason(value interface{}) failureInfo {
	switch typed := value.(type) {
	case nil:
		return failureInfo{}
	case string:
		return failureInfo{present: true, label: typed}
	default:
		rejected := mapValue(mapValue(typed)["Rejected"])
		if rejected == nil {
			return failureInfo{present: true}
		}
		interactive, _ := rejected["interactive"].(bool)
		return failureInfo{present: true, rejected: true, interactive: interactive}
	}
}

// errorType is the error.type a failed tool result carries: a command's nonzero exit is the
// command failing, an operator rejection is a rejection, the runtime's own label snakes down,
// and anything else is a generic tool error.
func errorType(call PendingCall, failure failureInfo, hasExit bool, exitCode int) string {
	if hasExit && exitCode != 0 && call.Command != "" {
		return "command_failed"
	}
	if failure.rejected {
		return "rejected"
	}
	if failure.label != "" {
		return snakeCase(failure.label)
	}
	return "tool_error"
}

func snakeCase(label string) string {
	var out strings.Builder
	for i, r := range label {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(r - 'A' + 'a')
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func languageForPath(path string) string {
	return strings.TrimPrefix(filepath.Ext(path), ".")
}

// rowTimestampMS prefers the message's own metadata.created_at (ISO-8601 with microseconds, when
// the runtime recorded the turn) and falls back to the node's created_at seconds.
func rowTimestampMS(row Row, meta map[string]interface{}) int64 {
	if value := strings.TrimSpace(stringValue(meta["created_at"])); value != "" {
		if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return ts.UnixMilli()
		}
	}
	return row.CreatedAtMS
}

// messageIDOf is the row's dedupe identity. Rows written before message_id existed get a
// content-derived id instead: old builds re-inserted those rows wholesale, so content is the
// right identity -- the same row re-read anywhere is the same row, and two different rows are
// different.
func messageIDOf(msg map[string]interface{}) string {
	if id := stringValue(msg["message_id"]); id != "" {
		return id
	}
	digest := sha256.New()
	for _, part := range []interface{}{
		msg["role"],
		msg["content"],
		msg["tool_call_id"],
		msg["tool_calls"],
		mapValue(msg["thinking"])["thinking"],
	} {
		switch typed := part.(type) {
		case string:
			io.WriteString(digest, typed)
		default:
			data, _ := json.Marshal(part)
			digest.Write(data)
		}
		digest.Write([]byte{0})
	}
	return "legacy-" + hex.EncodeToString(digest.Sum(nil))[:16]
}

// nodeMetadata decodes the row's node metadata column, which may be SQL NULL or the literal
// string "null".
func nodeMetadata(row Row) map[string]interface{} {
	raw := strings.TrimSpace(row.NodeMetadata)
	if raw == "" || raw == "null" {
		return nil
	}
	return decodeObject(raw)
}

// priorNodeIDs reads the compaction linkage out of a row's node metadata.
func priorNodeIDs(row Row) []int64 {
	ext := mapValue(nodeMetadata(row)["extensions"])
	list, _ := ext["compact/prior_node_ids"].([]interface{})
	if len(list) == 0 {
		return nil
	}
	out := make([]int64, 0, len(list))
	for _, item := range list {
		if id, ok := numberToInt64(item); ok {
			out = append(out, id)
		}
	}
	return out
}

// collectPriorNodeIDs gathers the node ids a page's rows point back at, deduped, for one batched
// priorMessageIDs lookup instead of a query per row.
func collectPriorNodeIDs(rows []Row) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, row := range rows {
		for _, id := range priorNodeIDs(row) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func decodeObject(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func decodeArguments(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case string:
		return decodeObject(typed)
	case map[string]interface{}:
		return typed
	default:
		return nil
	}
}

func mapValue(value interface{}) map[string]interface{} {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truthy(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed == "true" || typed == "1"
	}
	return false
}

// numberField returns a JSON number from an object while telling "absent" and "null" from a
// present zero: Devin's metrics may carry null fields, and a present zero is a real count.
func numberField(m map[string]interface{}, key string) (int64, bool) {
	value, ok := m[key]
	if !ok || value == nil {
		return 0, false
	}
	return numberToInt64(value)
}

func numberToInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case json.Number:
		n, err := typed.Int64()
		return n, err == nil
	}
	return 0, false
}

func intField(m map[string]interface{}, key string) (int, bool) {
	v, ok := numberField(m, key)
	return int(v), ok
}

func withGenAI(genAI *schema.GenAIInfo, edit func(*schema.GenAIInfo)) *schema.GenAIInfo {
	if genAI == nil {
		genAI = &schema.GenAIInfo{}
	}
	edit(genAI)
	return genAI
}

// mergeDevin merges fields into raw's "devin" sub-map: several contributors can add raw fields
// to one event (subagent provenance, finish_reason, usage ids), and a shallow merge would have
// each replace the ones before it.
func mergeDevin(raw map[string]interface{}, fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return raw
	}
	if raw == nil {
		raw = map[string]interface{}{}
	}
	sub, _ := raw["devin"].(map[string]interface{})
	if sub == nil {
		sub = map[string]interface{}{}
	}
	for k, v := range fields {
		sub[k] = v
	}
	raw["devin"] = sub
	return raw
}

var devinIDNamespace = [16]byte{
	0xfd, 0xee, 0x9b, 0x63, 0xfb, 0x48, 0xa9, 0x7b,
	0xbe, 0x30, 0x30, 0x22, 0xe2, 0xb2, 0xa5, 0xd1,
}

// devinEventID derives the deterministic, UUIDv5-shaped event id every poll event carries: a
// namespaced SHA-1 over the dedup coordinate, like hermesEventID but with its own namespace so a
// Devin id can never collide with another collector's.
func devinEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(devinIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
