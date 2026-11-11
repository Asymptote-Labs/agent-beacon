package antigravitysession

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Step types Antigravity writes beyond the user's own input (asymptoteobserve.IsAntigravityUserInput).
// Anything else is skipped by name, so a type a later agy adds is not mistaken for one Beacon maps.
const (
	stepPlannerResponse = "PLANNER_RESPONSE"
	stepGeneric         = "GENERIC"
)

// MapSession converts one conversation into endpoint events.
//
// It always walks the whole transcript, because a GENERIC step is only meaningful next to the
// planner response before it: Antigravity gives a tool call no id, and its result is the next
// GENERIC step in order. What opts controls is what gets *emitted* -- everything at or below the
// cursor is rebuilt for context and dropped -- so a conversation that grew since the last sweep
// yields only the steps that grew.
func MapSession(data SessionData, opts MapOptions) []MappedEvent {
	m := &mapper{data: data, opts: opts}
	if len(data.Steps) == 0 {
		return nil
	}
	m.emitStart()
	for _, step := range data.Steps {
		m.consume(step)
	}
	return m.out
}

type mapper struct {
	data SessionData
	opts MapOptions
	out  []MappedEvent
	// pending are the calls of the latest planner response whose GENERIC results have not been
	// read yet, in the order Antigravity runs them.
	pending []ToolCall
	// step and ordinal number the events mapped from one step, so each is named by where it was
	// read (see eventID).
	step    string
	ordinal int
}

func (m *mapper) consume(step Step) {
	switch {
	case asymptoteobserve.IsAntigravityUserInput(step.Source, step.Type):
		// A new turn: a call the last turn never reported a result for has none coming.
		m.pending = nil
		text := asymptoteobserve.AntigravityPrompt(step.Content)
		if text == "" {
			return
		}
		ev := m.base(step, "prompt.submitted", "prompt", "Antigravity CLI prompt submitted")
		ev.Prompt = &schema.PromptInfo{Text: text}
		ev.GenAI = &schema.GenAIInfo{Input: &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(text)}}
		ev.Content = contentMarker(text)
		m.append(ev, step)
		if info, ok := asymptoteobserve.ParseHandoffMarker(text); ok {
			link := m.base(step, "session.handoff", "session", "Session continued from a "+info.SourceHarness+" session")
			link.Handoff = &info
			m.append(link, step)
		}
	case step.Type == stepPlannerResponse:
		m.pending = nil
		if thinking := strings.TrimSpace(step.Thinking); thinking != "" {
			ev := m.base(step, "agent.reasoning", "agent", "Antigravity CLI agent reasoning")
			ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.ReasoningOutputMessages(step.Thinking)}}
			ev.Content = contentMarker(step.Thinking)
			m.append(ev, step)
		}
		if strings.TrimSpace(step.Content) != "" {
			ev := m.base(step, "agent.message", "agent", "Antigravity CLI agent message")
			ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(step.Content)}}
			ev.Content = contentMarker(step.Content)
			m.append(ev, step)
		}
		for _, call := range step.ToolCalls {
			m.append(m.toolCallEvent(step, call), step)
			m.pending = append(m.pending, call)
		}
		if usage := usageFor(step); usage != nil {
			ev := m.base(step, "token.usage", "metric", "Antigravity CLI token usage observed")
			ev.GenAI = &schema.GenAIInfo{Usage: usage}
			m.append(ev, step)
		}
	case step.Type == stepGeneric:
		if len(m.pending) == 0 {
			return
		}
		call := m.pending[0]
		m.pending = m.pending[1:]
		m.append(m.toolResultEvent(step, call), step)
	}
}

// emitStart writes session.started at the conversation's first step. Antigravity writes no
// session-end record -- a conversation is resumed with `agy --conversation <id>` -- so none is
// invented.
func (m *mapper) emitStart() {
	first := m.data.Steps[0]
	ev := m.base(first, "session.started", "session", "Antigravity CLI session started")
	ev.Event.ID = antigravityEventID(m.data.Ref.ID + ":session.started")
	if summary := m.data.Ref.Summary; summary != nil {
		detail := map[string]interface{}{}
		if summary.Title != "" {
			detail["title"] = summary.Title
		}
		if summary.ProjectID != "" {
			detail["project_id"] = summary.ProjectID
		}
		if summary.ParentConversationID != "" {
			detail["parent_conversation_id"] = summary.ParentConversationID
		}
		if summary.NestingDepth > 0 {
			detail["nesting_depth"] = summary.NestingDepth
		}
		if len(detail) > 0 {
			ev.Raw = map[string]interface{}{"antigravity": detail}
		}
	}
	if !m.opts.SkipStarted {
		m.out = append(m.out, MappedEvent{Event: ev, SourceKind: SourceSession, TimestampMS: stepMillis(first)})
	}
}

func (m *mapper) toolCallEvent(step Step, call ToolCall) schema.Event {
	ev := m.base(step, "tool.invoked", "tool", "Antigravity CLI tool invoked")
	m.applyTool(&ev, call, actionForTool(call.Name))
	ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{
		Name: call.Name,
		Call: &schema.GenAIToolCallInfo{Arguments: call.Arguments},
	}}
	return ev
}

func (m *mapper) toolResultEvent(step Step, call ToolCall) schema.Event {
	action := actionForTool(call.Name)
	ev := m.base(step, action, categoryForAction(action), "Antigravity CLI tool completed")
	m.applyTool(&ev, call, action)
	result := step.Content
	if action == "command.executed" {
		output, exitCode := parseCommandResult(step.Content)
		result = output
		ev.Command.Output = output
		ev.Command.ExitCode = exitCode
		ev.Content = contentMarker(output)
	}
	if action == "mcp.tool_invoked" {
		ev.MCP = &schema.MCPInfo{Tool: call.Name}
	}
	ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{
		Name: call.Name,
		Call: &schema.GenAIToolCallInfo{Arguments: call.Arguments, Result: result},
	}}
	return ev
}

// applyTool fills the tool, command and file blocks from a call's arguments.
func (m *mapper) applyTool(ev *schema.Event, call ToolCall, action string) {
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	if action == "command.executed" {
		command := asymptoteobserve.ToolCommandLine(call.Name, call.Arguments)
		ev.Tool.Command = command
		ev.Command = &schema.CommandInfo{Command: command}
		// The working directory the command ran in, when the conversation has none recorded.
		if cwd, ok := call.Arguments["Cwd"].(string); ok && ev.Session.WorkingDirectory == "" {
			ev.Session.WorkingDirectory = strings.TrimSpace(cwd)
		}
	}
	if path := asymptoteobserve.AntigravityToolPath(call.Arguments); path != "" {
		ev.Tool.Path = path
		if strings.HasPrefix(action, "file.") {
			ev.File = &schema.FileInfo{Path: path, Operation: fileOperation(call.Name)}
		}
	}
}

func (m *mapper) base(step Step, action, category, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: schema.SeverityInfo,
		Fidelity: schema.FidelityObserved,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	ev.Timestamp = normalizeTimestamp(step.CreatedAt)
	ev.Session = &schema.SessionInfo{ID: m.data.Ref.ID}
	if summary := m.data.Ref.Summary; summary != nil {
		ev.Session.WorkingDirectory = summary.WorkingDirectory
	}
	return ev
}

// append records an event mapped from step unless the collector has already written its line, and
// names it by where it was read.
func (m *mapper) append(ev schema.Event, step Step) {
	ev.Event.ID = m.eventID(ev, step)
	if step.Line <= m.opts.MinLine {
		return
	}
	m.out = append(m.out, MappedEvent{Event: ev, SourceKind: SourceTranscript, SourceLine: step.Line, TimestampMS: stepMillis(step)})
}

func stepMillis(step Step) int64 {
	if t := parseTime(step.CreatedAt); !t.IsZero() {
		return t.UnixMilli()
	}
	return 0
}

// eventID names an event by the conversation, the step and its place among that step's events.
// The step index rather than the line or the file is the coordinate, so the same step read from
// transcript_full.jsonl or from transcript.jsonl is the same event, and a sync resent after it
// stopped partway names the events it already wrote the same way.
func (m *mapper) eventID(ev schema.Event, step Step) string {
	key := "line." + strconv.Itoa(step.Line)
	if step.StepIndex != nil {
		key = "step." + strconv.FormatInt(*step.StepIndex, 10)
	}
	if key != m.step {
		m.step, m.ordinal = key, 0
	}
	m.ordinal++
	return antigravityEventID(fmt.Sprintf("%s:%s:%d:%s", m.data.Ref.ID, key, m.ordinal, ev.Event.Action))
}

var exitCodePattern = regexp.MustCompile(`Command exited with code (-?\d+)`)

// parseCommandResult reads a run_command result: the exit code from "Command exited with code N"
// and the output after "Command output:". The output is from a terminal, so its line endings are
// CRLF; they are folded to LF and the trailing line ending dropped. A result with neither marker,
// such as a command still running in the background, is kept whole with no exit code.
func parseCommandResult(content string) (string, *int) {
	var exitCode *int
	if match := exitCodePattern.FindStringSubmatch(content); match != nil {
		if code, err := strconv.Atoi(match[1]); err == nil {
			exitCode = &code
		}
	}
	output := content
	if idx := strings.Index(content, "Command output:"); idx >= 0 {
		output = strings.TrimPrefix(content[idx+len("Command output:"):], "\n")
	}
	output = strings.ReplaceAll(output, "\r\n", "\n")
	return strings.TrimRight(output, "\r\n"), exitCode
}

// usageFor is a planner response's own token counts. Each response reports the counts of the one
// model call that produced it, so they are recorded as reported rather than differenced.
//
// Beacon keeps gen_ai.usage counts disjoint. Antigravity is Gemini-backed, and Gemini's prompt
// count includes cached content, so the cache reads are taken out of the input count -- unless the
// input is smaller than they are and is therefore already uncached, the same rule Beacon applies to
// any source using the semconv names.
func usageFor(step Step) *schema.GenAIUsageInfo {
	input, cacheRead, output := value(step.InputTokens), value(step.CacheReadTokens), value(step.OutputTokens)
	if input <= 0 && cacheRead <= 0 && output <= 0 {
		return nil
	}
	usage := &schema.GenAIUsageInfo{}
	if cacheRead > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &cacheRead}
		if input >= cacheRead {
			input -= cacheRead
		}
	}
	if input > 0 {
		usage.InputTokens = &input
	}
	if output > 0 {
		usage.OutputTokens = &output
	}
	return usage
}

func value(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// actionForTool classifies a call by Antigravity's tool name, from the vocabulary the live hook
// adapter reads too; a call that is no command, file or MCP activity completes as a tool.
func actionForTool(name string) string {
	if action := asymptoteobserve.AntigravityToolAction(name); action != "" {
		return action
	}
	return "tool.completed"
}

func categoryForAction(action string) string {
	switch {
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	default:
		return "tool"
	}
}

func fileOperation(tool string) string {
	switch strings.ToLower(tool) {
	case "view_file", "list_dir", "grep_search", "find_by_name":
		return "read"
	case "write_to_file", "write_file":
		return "write"
	default:
		return "modify"
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
	t, err := asymptoteobserve.ParseTimestamp(ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

var antigravityIDNamespace = [16]byte{
	0x3f, 0x8a, 0x1d, 0x62, 0xc4, 0x57, 0x4e, 0x0b,
	0x9d, 0x26, 0xe1, 0x70, 0x5b, 0xa3, 0x48, 0xcf,
}

// antigravityEventID is a version 5 UUID of coordinate under antigravityIDNamespace.
func antigravityEventID(coordinate string) string {
	digest := sha1.New()
	digest.Write(antigravityIDNamespace[:])
	io.WriteString(digest, coordinate)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
