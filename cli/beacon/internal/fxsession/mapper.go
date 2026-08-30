package fxsession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Harness is the canonical harness name on every event this package produces. It matches what
// NormalizeHarnessName resolves every fx spelling to, so a query grouping by harness.name sees one
// runtime rather than two.
const Harness = "vercel_fx"

// fx's own command tools. Both run operating-system processes; `run_command` is the foreground form
// with a deadline and `terminal` is the durable session form fx uses for servers and watchers.
const (
	ToolRunCommand = "run_command"
	ToolTerminal   = "terminal"
)

// fx's built-in MCP control tools. They start with the same `mcp_` prefix fx gives to tools it
// imports from an MCP server, and they are not calls to a server: they search the catalog, select a
// tool, and report server features. Excluding them by name is what keeps the prefix a reliable
// signal for "this call went to an external MCP server".
var fxMCPControlTools = map[string]bool{
	"mcp_search_tools": true,
	"mcp_select_tool":  true,
	"mcp_features":     true,
}

// MappedEvent pairs a Beacon event with the id that makes re-reading fx's log idempotent.
//
// The id is derived from the record's own coordinates -- session, log generation, sequence, and the
// position within the turn -- rather than from the event's content, so the same fx record maps to
// the same id on every sweep no matter how many times the log is read.
type MappedEvent struct {
	DedupID string
	Event   schema.Event
}

// MapOptions controls one mapping pass over a session's decoded log.
type MapOptions struct {
	// MinSeq is the last sequence already collected. Events at or below it still contribute to
	// the mapper's running state -- the model in force, the workspace, the last usage snapshot --
	// but produce no output. Re-deriving that state from the log each sweep, rather than storing
	// it in the cursor, is what keeps a resumed sweep and a first sweep produce identical events.
	MinSeq uint64
	// SkipSessionStarted suppresses the session.started event. fx writes a fresh session_started
	// at sequence 1 of every log generation, including the one it creates when it compacts a log,
	// so a session that has already been reported as started must not be reported again.
	SkipSessionStarted bool
}

// MapSession converts one session's decoded events into Beacon endpoint events, in log order.
//
// Everything here is written as an after-the-fact reading: harness.collection_method is `poll` on
// every event, because Beacon read a committed record rather than observing the agent as it worked.
// What that costs is worth stating plainly -- no approval can be held, no tool call can be blocked,
// and nothing appears in the log until fx commits the turn.
//
// What it does not cost is fidelity about what happened. fx's record names the tool, the call id,
// the file, the action, the exit code, and the diff, so almost every event here is
// event.fidelity=observed. The exceptions are marked individually.
func MapSession(ref SessionRef, events []Event, opts MapOptions) []MappedEvent {
	m := &mapper{ref: ref, opts: opts}
	if ref.Manifest != nil {
		m.workspace = ref.Manifest.WorkspaceRoot
		if ref.Manifest.Preferences != nil {
			m.applyPreferences(ref.Manifest.Preferences)
		}
	}
	for i := range events {
		m.consume(&events[i])
	}
	return m.out
}

type mapper struct {
	ref  SessionRef
	opts MapOptions
	out  []MappedEvent

	workspace string
	model     string
	provider  string

	// lastUsage is the previous cumulative usage snapshot, kept so a checkpoint can be emitted as
	// the difference from the one before it. See usageEvent.
	lastUsage *UsageSnapshot
	// lastTotals is the session's cumulative token count as of the previous committed turn, used
	// to derive a turn's own usage on fx builds that write no turn summary. See emitTurnUsage.
	lastTotals struct {
		input  int64
		output int64
		seen   bool
	}
	// turnIndex counts committed turns, so a dedup id can name which turn an event came from even
	// though fx numbers turns only within its own history.
	turnIndex int
}

func (m *mapper) consume(event *Event) {
	emit := event.Seq > m.opts.MinSeq
	switch event.Kind {
	case KindSessionStarted:
		if event.SessionStarted == nil {
			return
		}
		if root := event.SessionStarted.WorkspaceRoot.String(); root != "" {
			m.workspace = root
		}
		m.applyPreferences(event.SessionStarted.Preferences)
		// A session_started can carry the usage the session resumed with. Seeding the delta
		// baseline from it stops the first checkpoint after a resume from being reported as if
		// the whole prior session had happened just then.
		if event.SessionStarted.Usage != nil {
			m.lastUsage = event.SessionStarted.Usage
		}
		if emit && !m.opts.SkipSessionStarted {
			m.emitSessionStarted(event)
		}
	case KindPreferencesChanged:
		m.applyPreferences(event.PreferencesChanged)
		if emit {
			m.emitSessionContext(event, "preferences_changed", "fx session preferences changed")
		}
	case KindWorkspaceRebound:
		if event.WorkspaceRebound != nil {
			if root := event.WorkspaceRebound.WorkspaceRoot.String(); root != "" {
				m.workspace = root
			}
		}
		if emit {
			m.emitSessionContext(event, "workspace_rebound", "fx session rebound to a new workspace")
		}
	case KindHistoryTurnCommitted:
		if event.TurnCommitted == nil {
			return
		}
		m.turnIndex++
		if emit {
			m.emitTurn(event)
		}
		// After emitting, and for every turn including the ones this sweep skips. The token
		// fallback in emitTurnUsage measures against this baseline, so a resumed sweep has to
		// carry it forward over the turns it is not re-emitting -- otherwise the first turn it
		// does emit reports the session's entire history as its own usage.
		m.recordTotals(event.TurnCommitted)
	case KindUsageCheckpointed:
		if event.UsageCheckpointed == nil {
			return
		}
		if emit {
			m.emitUsage(event)
		}
		m.lastUsage = event.UsageCheckpointed
	}
}

func (m *mapper) applyPreferences(prefs *Preferences) {
	if prefs == nil {
		return
	}
	if prefs.Model != "" {
		m.model = prefs.Model
	}
	if prefs.Provider != "" {
		m.provider = prefs.Provider
	}
}

// base builds the event every fx record shares: the harness, the poll provenance, the local origin,
// the session and its workspace, and the model in force at that point in the log.
func (m *mapper) base(event *Event, action, category string, severity schema.Severity, fidelity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: fidelity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name: Harness,
			// Poll, not hook: fx exposes no hook a third party can install and no OTLP export,
			// so Beacon reads the session record fx committed. Recording that here is what keeps
			// a consumer from reading these events as hook-grade coverage.
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	if event.TimestampMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(millis(event.TimestampMS))
	}
	ev.Session = &schema.SessionInfo{ID: m.sessionID(), WorkingDirectory: m.workspace}
	if m.model != "" {
		ev.Model = m.model
	}
	if m.provider != "" {
		ev.GenAI = &schema.GenAIInfo{Provider: &schema.GenAIProviderInfo{Name: m.provider}}
	}
	return ev
}

func (m *mapper) sessionID() string {
	if m.ref.ID != "" {
		return m.ref.ID
	}
	if m.ref.Manifest != nil {
		return m.ref.Manifest.ID
	}
	return ""
}

func (m *mapper) emitSessionStarted(event *Event) {
	started := event.SessionStarted
	ev := m.base(event, "session.started", "session", schema.SeverityInfo, schema.FidelityObserved, "fx session started")
	if started.CreatedAtMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(millis(started.CreatedAtMS))
	}
	raw := map[string]interface{}{
		"session_id":            started.ID,
		"workspace_root":        started.WorkspaceRoot.String(),
		"origin_workspace_root": started.OriginWorkspaceRoot.String(),
		"conversation_language": started.ConversationLang,
	}
	if started.Preferences != nil {
		raw["effort"] = started.Preferences.Effort
		if started.Preferences.FastMode != nil {
			raw["fast_mode"] = *started.Preferences.FastMode
		}
	}
	ev.Raw = map[string]interface{}{"fx": raw}
	m.append(event, "session.started", ev)
}

// emitSessionContext records a change to the session's configuration -- the model or provider in
// force, or the workspace it is bound to.
//
// session.context is the action the Codex integration already uses for "session metadata
// observed", so an fx model switch lands in the same place a reader already looks rather than
// under a new verb only fx produces.
func (m *mapper) emitSessionContext(event *Event, change, message string) {
	ev := m.base(event, "session.context", "session", schema.SeverityInfo, schema.FidelityObserved, message)
	raw := map[string]interface{}{"change": change}
	if event.PreferencesChanged != nil {
		raw["provider"] = event.PreferencesChanged.Provider
		raw["model"] = event.PreferencesChanged.Model
		raw["effort"] = event.PreferencesChanged.Effort
		if event.PreferencesChanged.FastMode != nil {
			raw["fast_mode"] = *event.PreferencesChanged.FastMode
		}
	}
	if event.WorkspaceRebound != nil {
		raw["previous_workspace_root"] = event.WorkspaceRebound.PreviousWorkspaceRoot.String()
		raw["workspace_root"] = event.WorkspaceRebound.WorkspaceRoot.String()
	}
	ev.Raw = map[string]interface{}{"fx": raw}
	m.append(event, change, ev)
}

// emitTurn expands one committed turn into the events it justifies: the prompt, one event per tool
// call, the assistant's reply, and the turn's token usage.
func (m *mapper) emitTurn(event *Event) {
	turn := event.TurnCommitted.Turn
	if turn.User != nil {
		if text := turn.User.Text.String(); text != "" {
			m.emitPrompt(event, text, turn.User.Images)
		}
	}
	if turn.Execution != nil {
		for stepIndex, step := range turn.Execution.ToolSteps {
			m.emitStep(event, turn, stepIndex, step)
		}
	}
	// The tool call that was in flight when the user interrupted the turn has no result and sits
	// outside the steps. Without this it would be the one action fx recorded that Beacon dropped --
	// and an interrupted call is more interesting than a completed one, not less.
	if turn.Kind == TurnKindInterrupted && turn.ToolCall != nil {
		m.emitToolCallWithoutResult(event, *turn.ToolCall, "interrupted", -1, 0)
	}
	if turn.Assistant != nil && strings.TrimSpace(*turn.Assistant) != "" {
		m.emitAssistantMessage(event, *turn.Assistant)
	}
	if turn.Kind == TurnKindCompactedSummary {
		m.emitCompaction(event, turn)
	}
	m.emitTurnUsage(event, turn)
}

func (m *mapper) emitPrompt(event *Event, text string, images []UserImage) {
	ev := m.base(event, "prompt.submitted", "prompt", schema.SeverityInfo, schema.FidelityObserved, "fx prompt submitted")
	if summary := event.TurnCommitted.Turn.Execution; summary != nil && summary.TurnSummary != nil && summary.TurnSummary.StartedAtMS > 0 {
		// The envelope is stamped when fx commits the turn, which is after the model has finished.
		// The prompt happened when the turn started, and using the commit time for it would put
		// every prompt after the tool calls it caused.
		ev.Timestamp = schema.FormatTimestamp(millis(summary.TurnSummary.StartedAtMS))
	}
	ev.Prompt = &schema.PromptInfo{Text: text}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Input = &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}
	})
	if len(images) > 0 {
		paths := make([]interface{}, 0, len(images))
		for _, image := range images {
			paths = append(paths, map[string]interface{}{
				"path":       image.Path.String(),
				"media_type": image.MediaType.String(),
			})
		}
		// Paths and media types, never the bytes: the image is a file on this machine, and copying
		// it into a log line would put unbounded binary content into a stream meant to be read.
		ev.Raw = mergeRaw(ev.Raw, map[string]interface{}{"images": paths})
	}
	m.append(event, fmt.Sprintf("turn.%d.prompt", m.turnIndex), ev)
}

func (m *mapper) emitAssistantMessage(event *Event, text string) {
	ev := m.base(event, "agent.message", "session", schema.SeverityInfo, schema.FidelityObserved, "fx agent message")
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}
	})
	// gen_ai is sanitized at the raw string limit rather than the prompt limit, so the content
	// marker has to describe that limit or it claims something about text nobody stored.
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	m.append(event, fmt.Sprintf("turn.%d.assistant", m.turnIndex), ev)
}

// emitCompaction records that fx dropped turns from its own history to reclaim context.
//
// Worth an event rather than a silent state change: the summary is what the model can still see of
// the work it did, so a reader reconstructing a session needs to know where the record thins out.
func (m *mapper) emitCompaction(event *Event, turn Turn) {
	ev := m.base(event, "session.compacting", "session", schema.SeverityInfo, schema.FidelityObserved, "fx compacted session history")
	ev.Raw = mergeRaw(ev.Raw, map[string]interface{}{
		"removed_turn_count": turn.RemovedTurnCount,
		"compaction_count":   turn.CompactionCount,
		"summary":            turn.Summary.String(),
	})
	m.append(event, fmt.Sprintf("turn.%d.compaction", m.turnIndex), ev)
}

func (m *mapper) emitStep(event *Event, turn Turn, stepIndex int, step ToolStep) {
	results := make(map[string]*ToolResult, len(step.ToolResults))
	for i := range step.ToolResults {
		results[step.ToolResults[i].ToolCallID.String()] = &step.ToolResults[i]
	}
	for callIndex, call := range step.ToolCalls {
		result := results[call.ID.String()]
		if result == nil {
			m.emitToolCallWithoutResult(event, call, "no_result", stepIndex, callIndex)
			continue
		}
		m.emitToolOutcome(event, turn, stepIndex, callIndex, call, *result)
	}
}

// emitToolCallWithoutResult records a call fx asked for and never recorded an outcome for: the turn
// was interrupted, or the process died between the call and its result.
//
// tool.invoked rather than a completion verb, because that is all that is known. The alternative --
// dropping it -- would make an interrupted dangerous call invisible, which is the opposite of what
// an endpoint log is for.
func (m *mapper) emitToolCallWithoutResult(event *Event, call ToolCall, reason string, stepIndex, callIndex int) {
	name := call.Name.String()
	ev := m.base(event, "tool.invoked", "tool", schema.SeverityInfo, schema.FidelityObserved, "fx tool invoked without a recorded result")
	ev.Tool = &schema.ToolInfo{Name: name}
	m.applyToolArguments(&ev, name, call)
	ev.Raw = mergeRaw(ev.Raw, map[string]interface{}{"result_missing": reason})
	m.append(event, fmt.Sprintf("turn.%d.step.%d.call.%d.invoked", m.turnIndex, stepIndex, callIndex), ev)
}

// emitToolOutcome maps one completed call onto the single event that best describes what it did.
//
// The classification order is deliberate and one part of it is worth defending: a tool's failure
// does not change what kind of action it was. fx marks a command that exits non-zero as
// status=failure, so classifying failure first -- which is what the Grok and Qwen hook mappers do,
// because their payloads carry a failure event type and no structured outcome -- would file every
// failing command as tool.failed and hide it from every rule that matches command.executed. fx
// gives an exit code, so a command that ran and failed is still command.executed, with the exit
// code and a failure marker on it.
func (m *mapper) emitToolOutcome(event *Event, turn Turn, stepIndex, callIndex int, call ToolCall, result ToolResult) {
	name := firstNonEmpty(result.ToolName.String(), call.Name.String())
	evidence := fileEvidenceFor(turn, result.ToolCallID.String(), name)

	action, category, severity, message := classifyOutcome(name, result, evidence)
	ev := m.base(event, action, category, severity, schema.FidelityObserved, message)
	if result.CreatedAtMS > 0 {
		// fx timestamps each result, so the events inside one turn keep the order they happened in
		// rather than all landing on the turn's commit time.
		ev.Timestamp = schema.FormatTimestamp(millis(result.CreatedAtMS))
	}
	ev.Tool = &schema.ToolInfo{Name: name}
	m.applyToolArguments(&ev, name, call)

	switch action {
	case "command.executed":
		m.applyCommand(&ev, result)
	case "file.read", "file.modified", "file.created":
		m.applyFile(&ev, result, evidence)
	case "mcp.tool_invoked":
		ev.MCP = &schema.MCPInfo{Tool: name}
	}

	if result.Status == ToolStatusFailure {
		// Carried as an error marker on whatever the action was, rather than by rerouting the
		// action, so a failed command is still findable as a command.
		ev.Error = &schema.ErrorInfo{Type: "tool_execution_failed"}
	}
	ev.Raw = mergeRaw(ev.Raw, toolResultRaw(result))
	m.append(event, fmt.Sprintf("turn.%d.step.%d.call.%d.result", m.turnIndex, stepIndex, callIndex), ev)
}

// classifyOutcome decides which endpoint action one completed tool call is.
func classifyOutcome(name string, result ToolResult, evidence *FileRecord) (action, category string, severity schema.Severity, message string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case isMCPServerTool(lower):
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "fx MCP tool invoked"
	case lower == ToolRunCommand || lower == ToolTerminal:
		return "command.executed", "command", schema.SeverityInfo, "fx command executed"
	case result.CommittedFile != nil:
		if result.CommittedFile.Kind == "added" {
			return "file.created", "file", schema.SeverityInfo, "fx file created"
		}
		return "file.modified", "file", schema.SeverityInfo, "fx file modified"
	case evidence != nil:
		switch evidence.Action {
		case FileActionRead, FileActionSearch, FileActionList:
			return "file.read", "file", schema.SeverityInfo, "fx file read"
		case FileActionWrite:
			return "file.created", "file", schema.SeverityInfo, "fx file created"
		case FileActionEdit, FileActionDelete, FileActionRename, FileActionCopy:
			// A delete, a rename and a copy are all mutations of the tree, and file.modified is
			// the action every mutation rule matches on. file.operation carries which one it was,
			// so nothing is lost by not inventing a verb per action -- while a new verb would be
			// invisible to the rules that exist.
			return "file.modified", "file", schema.SeverityInfo, "fx file modified"
		}
	}
	if result.Status == ToolStatusFailure {
		return "tool.failed", "tool", schema.SeverityHigh, "fx tool failed"
	}
	return "tool.completed", "tool", schema.SeverityInfo, "fx tool completed"
}

// isMCPServerTool reports whether a tool name is one fx imported from an MCP server.
//
// fx allocates those names itself as `mcp_<server>_<tool>`, sanitizing anything outside
// [A-Za-z0-9_-] to an underscore, truncating to 64 bytes, and appending `_2` on a collision. The
// prefix is therefore reliable; the server name is not recoverable from it, which is why the events
// carry mcp.tool and leave mcp.server unset rather than guessing at a split that fx's own
// sanitization makes ambiguous.
func isMCPServerTool(lower string) bool {
	return strings.HasPrefix(lower, "mcp_") && !fxMCPControlTools[lower]
}

// applyToolArguments promotes fx's own identifiers and the call's arguments.
//
// gen_ai.tool.call.id is the field that links a call to its result, an approval to what it
// approved, and one runtime's record to another's, so fx's call id goes there on every tool event.
func (m *mapper) applyToolArguments(ev *schema.Event, name string, call ToolCall) {
	arguments := decodeArguments(call.ArgumentsJSON.String())
	callID := call.ID.String()
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
		tool := &schema.GenAIToolInfo{Name: name}
		callInfo := &schema.GenAIToolCallInfo{ID: callID}
		if arguments != nil {
			callInfo.Arguments = arguments
		}
		tool.Call = callInfo
		genAI.Tool = tool
	})
	if path := stringArg(arguments, "path", "file_path", "target_path"); path != "" {
		if ev.Tool == nil {
			ev.Tool = &schema.ToolInfo{Name: name}
		}
		ev.Tool.Path = path
	}
	if command := stringArg(arguments, "command", "cmd"); command != "" {
		if ev.Tool == nil {
			ev.Tool = &schema.ToolInfo{Name: name}
		}
		ev.Tool.Command = command
	}
}

// applyCommand fills the command block from fx's own process outcome.
//
// The exit code is fx's, not a guess: fx records the process presentation as an exit code, a
// signal, a timeout, or a failure to capture output, and each of those means something different
// to a reader. Only the first is an exit code, so only the first is written as one; the rest are
// named in raw rather than flattened into a number that would read as a real status.
func (m *mapper) applyCommand(ev *schema.Event, result ToolResult) {
	command := ""
	if ev.Tool != nil {
		command = ev.Tool.Command
	}
	info := &schema.CommandInfo{Command: command}
	if output := result.Output.String(); output != "" {
		info.Output = output
		ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
	}
	if result.CommandProcess != nil && result.CommandProcess.Kind == ProcessExitCode && result.CommandProcess.Value != nil {
		code := *result.CommandProcess.Value
		info.ExitCode = &code
	}
	ev.Command = info
}

// applyFile fills the file block: the path fx attributed to the call, the operation it performed,
// and the diff it committed.
func (m *mapper) applyFile(ev *schema.Event, result ToolResult, evidence *FileRecord) {
	info := &schema.FileInfo{}
	if evidence != nil {
		info.Path = evidence.Path.String()
		info.Operation = fileOperation(evidence.Action)
	}
	if result.CommittedFile != nil {
		if path := result.CommittedFile.Path.String(); path != "" {
			info.Path = path
		}
		if info.Operation == "" {
			if result.CommittedFile.Kind == "added" {
				info.Operation = "create"
			} else {
				info.Operation = "modify"
			}
		}
		if diff := unifiedDiff(result.CommittedFile); diff != "" {
			info.Diff = diff
			info.DiffBytes = len(diff)
			info.DiffHash = sha256Hex(diff)
			ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
		}
	}
	if info.Path == "" && ev.Tool != nil {
		info.Path = ev.Tool.Path
	}
	if info.Path != "" {
		info.Language = strings.TrimPrefix(filepath.Ext(info.Path), ".")
	}
	ev.File = info
}

// fileOperation maps fx's file action onto the operation vocabulary the hook path already writes:
// read, create, modify. The actions with no counterpart there keep fx's own word rather than being
// forced into one of the three, because "delete" is not a modification a reader should have to
// infer from an action name.
func fileOperation(action string) string {
	switch action {
	case FileActionRead, FileActionSearch, FileActionList:
		return "read"
	case FileActionWrite:
		return "create"
	case FileActionEdit:
		return "modify"
	case FileActionDelete, FileActionRename, FileActionCopy:
		return action
	default:
		return ""
	}
}

// unifiedDiff renders fx's committed diff lines back into unified-diff text.
//
// fx stores the diff as structured lines rather than as text, so this is a rendering rather than a
// pass-through. The markers are the standard ones so an existing reader -- a rule matching on
// file.diff, a person looking at the dashboard -- sees the shape it expects. fx's own elision and
// notice lines become `...` and a comment, since they are fx's markers for omitted regions rather
// than file content.
func unifiedDiff(file *CommittedFile) string {
	if file == nil || len(file.Lines) == 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range file.Lines {
		switch line.Kind {
		case "addition":
			b.WriteString("+")
		case "deletion":
			b.WriteString("-")
		case "context":
			b.WriteString(" ")
		case "elision":
			b.WriteString("...")
			b.WriteString("\n")
			continue
		default:
			b.WriteString("# ")
		}
		b.WriteString(line.Text.String())
		b.WriteString("\n")
	}
	return b.String()
}

// emitTurnUsage reports the turn's own token counts.
//
// Per-turn counts, from fx's turn summary, rather than the session totals sitting beside them in
// the same record: Beacon's rollups sum gen_ai.usage across events, so emitting the cumulative
// totals once per turn would count a session's whole history once per turn.
//
// Only input and output tokens come from here. Cost and cache tokens exist only in fx's cumulative
// usage snapshots and are emitted from those as deltas, in fields this event leaves unset -- so the
// two sources add up rather than overlapping.
//
// Two sources for the per-turn counts, because fx has shipped both shapes. A build that writes a
// turn summary is read from it, flags and durations included. A build that writes none -- fx 0.0.7
// omits the summary entirely, which is how this was found: a real session through the released
// binary produced no token usage at all -- falls back to the difference between this turn's
// cumulative session totals and the previous turn's. Same discipline as the checkpoint deltas, and
// the same reason: the alternative is either no usage or a session's whole history counted once per
// turn. raw.fx.token_source says which was used, because an exact per-turn count and a difference
// of two running totals are not equally trustworthy.
func (m *mapper) emitTurnUsage(event *Event, turn Turn) {
	usage := &schema.GenAIUsageInfo{}
	raw := map[string]interface{}{}

	summary := (*TurnSummary)(nil)
	if turn.Execution != nil {
		summary = turn.Execution.TurnSummary
	}
	switch {
	case summary != nil && (summary.TokenProgress.InputTokens > 0 || summary.TokenProgress.OutputTokens > 0):
		progress := summary.TokenProgress
		if progress.InputTokens > 0 {
			input := progress.InputTokens
			usage.InputTokens = &input
		}
		if progress.OutputTokens > 0 {
			output := progress.OutputTokens
			usage.OutputTokens = &output
		}
		// fx says when a count is its own estimate rather than the provider's number. Carrying the
		// flags is what keeps a cost report from presenting an estimate as a measurement.
		raw["token_source"] = "turn_summary"
		raw["input_exact"] = progress.InputExact
		raw["output_exact"] = progress.OutputExact
		raw["turn_duration_ms"] = summary.TurnDurationMS
		raw["thinking_duration_ms"] = summary.ThinkingDurationMS
	default:
		input := delta(int64(event.TurnCommitted.TotalInputTokens), m.lastTotals.input)
		output := delta(int64(event.TurnCommitted.TotalOutputTokens), m.lastTotals.output)
		if input > 0 {
			usage.InputTokens = &input
		}
		if output > 0 {
			usage.OutputTokens = &output
		}
		raw["token_source"] = "session_totals_delta"
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil {
		return
	}
	ev := m.base(event, "token.usage", "metric", schema.SeverityInfo, schema.FidelityObserved, "fx token usage")
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) { genAI.Usage = usage })
	ev.Raw = mergeRaw(ev.Raw, raw)
	m.append(event, fmt.Sprintf("turn.%d.usage", m.turnIndex), ev)
}

// recordTotals advances the cumulative baseline the fallback above measures against. Its caller
// runs it for every committed turn, including ones this sweep is not emitting, so a resumed sweep
// measures the first turn it emits against the turn before it rather than against zero.
func (m *mapper) recordTotals(committed *TurnCommitted) {
	if committed == nil {
		return
	}
	m.lastTotals.input = int64(committed.TotalInputTokens)
	m.lastTotals.output = int64(committed.TotalOutputTokens)
	m.lastTotals.seen = true
}

// emitUsage reports a usage checkpoint as the difference from the previous one.
//
// fx's snapshots are cumulative replacements: each one restates the session's whole usage. Emitting
// a snapshot's numbers directly would count everything before it again in any rollup that sums
// events, which is every rollup Beacon has. The difference is what actually happened since the last
// checkpoint, so that is what the event carries.
//
// Token counts here are the cache buckets only. The input and output tokens are already reported
// per turn from fx's turn summaries, and reporting them again from the cumulative side would double
// every session's totals.
func (m *mapper) emitUsage(event *Event) {
	current := event.UsageCheckpointed
	usage := &schema.GenAIUsageInfo{}
	cacheRead := delta(current.CacheReadTokens, usageField(m.lastUsage, func(s *UsageSnapshot) int64 { return s.CacheReadTokens }))
	cacheWrite := delta(current.CacheWriteTokens, usageField(m.lastUsage, func(s *UsageSnapshot) int64 { return s.CacheWriteTokens }))
	cost := current.TotalCost
	if m.lastUsage != nil {
		cost -= m.lastUsage.TotalCost
	}
	if cacheRead > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &cacheRead}
	}
	if cacheWrite > 0 {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &cacheWrite}
	}
	// Runtime-reported cost only: fx computes it from the provider's own billing, and Beacon never
	// derives cost from a local pricing table. A negative difference means the snapshot went
	// backwards (a restored session, a reset window), which is not a cost and is dropped.
	if cost > 0 {
		usage.CostUSD = &cost
	}
	if usage.CacheRead == nil && usage.CacheCreation == nil && usage.CostUSD == nil {
		return
	}
	ev := m.base(event, "token.usage", "metric", schema.SeverityInfo, schema.FidelityObserved, "fx usage checkpoint")
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) { genAI.Usage = usage })
	raw := map[string]interface{}{
		"billing":                      current.Billing,
		"cumulative_input_tokens":      current.InputTokens,
		"cumulative_output_tokens":     current.OutputTokens,
		"cumulative_cost_usd":          current.TotalCost,
		"billable_web_search_calls":    current.BillableWebSearchCalls,
		"cumulative_lines_added":       current.LinesAdded,
		"cumulative_lines_removed":     current.LinesRemoved,
		"reported_as_delta_from_prior": m.lastUsage != nil,
	}
	if len(current.Models) > 0 {
		models := make([]interface{}, 0, len(current.Models))
		for _, model := range current.Models {
			models = append(models, map[string]interface{}{
				"model":         model.Model,
				"input_tokens":  model.InputTokens,
				"output_tokens": model.OutputTokens,
				"cost_usd":      model.TotalCost,
			})
		}
		raw["cumulative_models"] = models
	}
	ev.Raw = mergeRaw(ev.Raw, raw)
	m.append(event, fmt.Sprintf("usage.%d", event.Seq), ev)
}

func (m *mapper) append(event *Event, suffix string, ev schema.Event) {
	m.out = append(m.out, MappedEvent{
		DedupID: fmt.Sprintf("%s:%s:%d:%s", m.sessionID(), event.LogGeneration, event.Seq, suffix),
		Event:   ev,
	})
}

// withGenAI edits the event's gen_ai block in place, creating it when the event has none. The
// provider set by base() has to survive every later addition, so nothing here replaces the block.
func (m *mapper) withGenAI(genAI *schema.GenAIInfo, edit func(*schema.GenAIInfo)) *schema.GenAIInfo {
	if genAI == nil {
		genAI = &schema.GenAIInfo{}
	}
	edit(genAI)
	return genAI
}

// fileEvidenceFor finds fx's own record of what a call did to a file.
//
// Matched on the call id rather than on the tool name, because one turn can read and edit the same
// file through different calls, and the tool name would not tell them apart.
func fileEvidenceFor(turn Turn, callID, toolName string) *FileRecord {
	if turn.Execution == nil || callID == "" {
		return nil
	}
	for i := range turn.Execution.Files {
		record := &turn.Execution.Files[i]
		if record.ToolCallID.String() == callID {
			return record
		}
	}
	// A call id fx did not record file evidence for is not a file action, even when the tool's
	// name sounds like one.
	_ = toolName
	return nil
}

// toolResultRaw carries fx's own accounting of the result: its status, how much output there was
// against how much survived fx's limit, and the permission feedback a person typed at a prompt.
func toolResultRaw(result ToolResult) map[string]interface{} {
	raw := map[string]interface{}{
		"status":              result.Status,
		"output_bytes":        result.OutputBytes,
		"stored_output_bytes": result.StoredOutputBytes,
		"truncated":           result.Truncated,
		"provider_native":     result.ProviderNative,
	}
	if result.CommandOutputReplay != nil && result.CommandOutputReplay.Handle != "" {
		// The full captured output of a command is not in the record: fx keeps it in a framed
		// replay file beside the session and puts the handle here. Beacon cannot read that framing,
		// so it carries the handle rather than pretending the stored text is the whole output.
		raw["command_output_handle"] = result.CommandOutputReplay.Handle.String()
		raw["command_output_framed_bytes"] = result.CommandOutputReplay.FramedBytes
	}
	if result.CommandProcess != nil {
		raw["process_outcome"] = result.CommandProcess.Kind
		if result.CommandProcess.Value != nil {
			raw["process_value"] = *result.CommandProcess.Value
		}
	}
	if result.TerminalAction != nil {
		raw["terminal_outcome"] = result.TerminalAction.Kind
		if result.TerminalAction.Outcome != nil {
			raw["terminal_result"] = result.TerminalAction.Outcome.Kind
		}
	}
	if len(result.PermissionFeedback) > 0 {
		feedback := make([]interface{}, 0, len(result.PermissionFeedback))
		for _, item := range result.PermissionFeedback {
			feedback = append(feedback, item.String())
		}
		// What a person told the agent at a permission prompt. It is evidence about a decision,
		// not the decision itself: fx does not persist an approve/deny record per call, so Beacon
		// records the feedback where it belongs and does not synthesize an approval event from it.
		// Same posture as the Cline and Pi integrations, for the same reason.
		raw["permission_feedback"] = feedback
	}
	if result.CommittedFile != nil {
		raw["diff_additions"] = result.CommittedFile.Additions
		raw["diff_deletions"] = result.CommittedFile.Deletions
		raw["diff_truncated"] = result.CommittedFile.Truncated
	}
	return map[string]interface{}{"fx": raw}
}

func decodeArguments(argumentsJSON string) map[string]interface{} {
	if strings.TrimSpace(argumentsJSON) == "" {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(argumentsJSON), &out); err != nil {
		// A tool's arguments are whatever the model sent; fx stores them verbatim without
		// requiring them to be a JSON object. Keeping the raw text beats dropping the field.
		return map[string]interface{}{"raw": argumentsJSON}
	}
	return out
}

func stringArg(arguments map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := arguments[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func mergeRaw(existing map[string]interface{}, fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return existing
	}
	if existing == nil {
		existing = map[string]interface{}{}
	}
	nested, ok := existing["fx"].(map[string]interface{})
	if !ok {
		nested = map[string]interface{}{}
	}
	// A caller passing a pre-nested {"fx": ...} block merges into the same place as one passing
	// bare fields, so raw.fx stays a single object rather than nesting fx inside fx.
	if inner, ok := fields["fx"].(map[string]interface{}); ok && len(fields) == 1 {
		fields = inner
	}
	for key, value := range fields {
		nested[key] = value
	}
	existing["fx"] = nested
	return existing
}

func usageField(snapshot *UsageSnapshot, get func(*UsageSnapshot) int64) int64 {
	if snapshot == nil {
		return 0
	}
	return get(snapshot)
}

// delta is the increase between two cumulative counters. A decrease means the snapshot went
// backwards, which is not negative usage: it is a session restored from an earlier state, and
// reporting it as a negative would subtract from a total that describes something else.
func delta(current, previous int64) int64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// millis converts fx's millisecond timestamps into the instant the event writers stamp events with.
func millis(value int64) time.Time { return time.UnixMilli(value) }
