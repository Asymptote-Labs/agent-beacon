package handoff

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Bounds on a brief. A brief is read by an agent as the start of a new session, so it favours the
// end of the session and a size a model reads in one pass.
const (
	BriefMaxBytes       = 48 << 10
	briefTailEntries    = 30
	briefEntryRunes     = 1200
	briefRequestRunes   = 800
	briefCommandsListed = 12
	briefFilesListed    = 40
)

// Where a brief's content came from.
const (
	FromSessionStore = "session_store"
	FromRuntimeLog   = "runtime_log"
)

// Brief is what one session left behind, arranged for whoever picks it up next.
type Brief struct {
	Session     Session
	From        string
	GeneratedAt time.Time

	FirstRequest  string
	LatestRequest string
	LastMessage   string
	Branch        string

	Files    []FileChange
	Commands []CommandRun
	Tail     []TailEntry

	Prompts      int
	Messages     int
	CommandCount int
	ToolFailures int
}

// FileChange is a file the session created or modified, and how often.
type FileChange struct {
	Path       string
	Operations []string
	Count      int
}

// CommandRun is one shell command the session ran.
type CommandRun struct {
	Command  string
	ExitCode *int
}

// TailEntry is one step of the session, rendered in the recent-activity section.
type TailEntry struct {
	Kind string
	At   string
	Head string
	Body string
}

// Tail entry kinds.
const (
	TailUser        = "user"
	TailAssistant   = "assistant"
	TailCommand     = "command"
	TailFile        = "file"
	TailToolFailure = "tool failure"
	TailMCP         = "mcp"
)

// BuildBrief arranges a session's events into a brief. Every event passes through the same
// redaction and truncation the runtime log writer applies before any of it is used, so a brief
// never holds a secret the log would not.
func BuildBrief(session Session, events []schema.Event, from string, now time.Time) Brief {
	brief := Brief{Session: session, From: from, GeneratedAt: now.UTC(), Branch: session.Branch}
	files := map[string]*FileChange{}
	var fileOrder []string
	for _, raw := range events {
		// The brief has its own byte budget, so no event-size cap applies here.
		event := asymptoteobserve.SanitizeEvent(raw, math.MaxInt)
		if event.Branch != "" {
			brief.Branch = event.Branch
		}
		at := briefTime(event.Timestamp)
		switch action := event.Event.Action; {
		case action == "prompt.submitted":
			text := promptOf(event)
			if text == "" {
				continue
			}
			brief.Prompts++
			if brief.FirstRequest == "" {
				brief.FirstRequest = text
			}
			brief.LatestRequest = text
			brief.Tail = append(brief.Tail, TailEntry{Kind: TailUser, At: at, Body: text})
		case action == "agent.message":
			text := dashboard.RetainedOutputText(event.GenAI, asymptoteobserve.GenAIPartTypeText)
			if text == "" {
				continue
			}
			brief.Messages++
			brief.LastMessage = text
			brief.Tail = append(brief.Tail, TailEntry{Kind: TailAssistant, At: at, Body: text})
		case action == "command.executed":
			command := commandOf(event)
			if command == "" {
				continue
			}
			brief.CommandCount++
			var exit *int
			var output string
			if event.Command != nil {
				exit = event.Command.ExitCode
				output = event.Command.Output
			}
			brief.Commands = append(brief.Commands, CommandRun{Command: command, ExitCode: exit})
			brief.Tail = append(brief.Tail, TailEntry{Kind: TailCommand, At: at, Head: command + exitSuffix(exit), Body: output})
		case action == "file.created" || action == "file.modified":
			if event.File == nil || event.File.Path == "" {
				continue
			}
			path := event.File.Path
			change := files[path]
			if change == nil {
				change = &FileChange{Path: path}
				files[path] = change
				fileOrder = append(fileOrder, path)
			}
			change.Count++
			op := strings.TrimPrefix(action, "file.")
			if !contains(change.Operations, op) {
				change.Operations = append(change.Operations, op)
			}
			brief.Tail = append(brief.Tail, TailEntry{Kind: TailFile, At: at, Head: op + " " + path, Body: event.File.Diff})
		case action == "tool.failed":
			brief.ToolFailures++
			brief.Tail = append(brief.Tail, TailEntry{Kind: TailToolFailure, At: at, Head: toolName(event), Body: toolResult(event)})
		case action == "mcp.tool_invoked":
			brief.Tail = append(brief.Tail, TailEntry{Kind: TailMCP, At: at, Head: mcpName(event), Body: toolArguments(event)})
		}
	}
	for _, path := range fileOrder {
		brief.Files = append(brief.Files, *files[path])
	}
	return brief
}

// briefTime shows an event timestamp to the second, in UTC.
func briefTime(ts string) string {
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return parsed.UTC().Format(time.RFC3339)
}

func promptOf(event schema.Event) string {
	if event.Prompt != nil {
		return strings.TrimSpace(event.Prompt.Text)
	}
	return ""
}

func commandOf(event schema.Event) string {
	if event.Command != nil && strings.TrimSpace(event.Command.Command) != "" {
		return strings.TrimSpace(event.Command.Command)
	}
	if event.Tool != nil {
		return strings.TrimSpace(event.Tool.Command)
	}
	return ""
}

func toolName(event schema.Event) string {
	if event.Tool != nil && event.Tool.Name != "" {
		return event.Tool.Name
	}
	if event.GenAI != nil && event.GenAI.Tool != nil && event.GenAI.Tool.Name != "" {
		return event.GenAI.Tool.Name
	}
	return "tool"
}

func mcpName(event schema.Event) string {
	if event.MCP != nil && (event.MCP.Server != "" || event.MCP.Tool != "") {
		return strings.Trim(event.MCP.Server+" "+event.MCP.Tool, " ")
	}
	return toolName(event)
}

func toolArguments(event schema.Event) string {
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Call == nil {
		return ""
	}
	return compactValue(event.GenAI.Tool.Call.Arguments)
}

func toolResult(event schema.Event) string {
	if event.GenAI != nil && event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil {
		if result := compactValue(event.GenAI.Tool.Call.Result); result != "" {
			return result
		}
	}
	return strings.TrimSpace(event.Message)
}

func compactValue(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func exitSuffix(exit *int) string {
	if exit == nil {
		return ""
	}
	return fmt.Sprintf(" (exit %d)", *exit)
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// Render writes the brief as Markdown no larger than BriefMaxBytes, dropping the oldest
// recent-activity entries first when it would be larger.
func (b Brief) Render() string {
	tail := b.Tail
	if len(tail) > briefTailEntries {
		tail = tail[len(tail)-briefTailEntries:]
	}
	for {
		out := b.render(tail, len(b.Tail)-len(tail))
		if len(out) <= BriefMaxBytes || len(tail) == 0 {
			return truncateBytes(out, BriefMaxBytes)
		}
		tail = tail[1:]
	}
}

func (b Brief) render(tail []TailEntry, omitted int) string {
	var w strings.Builder
	s := b.Session
	w.WriteString("# Handoff brief\n\n")
	fmt.Fprintf(&w, "Beacon wrote this brief from a %s session stored on this machine, so the session can be picked up again. ", inline(RuntimeLabel(s.Harness)))
	w.WriteString("It is a summary, not the full transcript: long messages, command output and diffs are cut, secrets Beacon recognises are redacted, and nothing the runtime kept only in memory is here.")
	if b.From == FromRuntimeLog {
		w.WriteString(" The runtime's own session file was not available, so this brief comes from Beacon's runtime log, which keeps less of each step.")
	}
	w.WriteString("\n\n## Source session\n\n")
	field(&w, "Runtime", inline(RuntimeLabel(s.Harness))+" ("+code(s.Harness)+")")
	field(&w, "Session", code(s.ID))
	if s.Title != "" {
		field(&w, "Title", inline(s.Title))
	}
	field(&w, "Directory", code(s.Directory))
	field(&w, "Branch", code(b.Branch))
	if !s.UpdatedAt.IsZero() {
		field(&w, "Last activity", s.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if s.SourcePath != "" {
		field(&w, "Session file", code(s.SourcePath))
	}
	field(&w, "Brief written", b.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&w, "- Activity: %d prompts, %d agent messages, %d commands, %d files changed, %d tool failures\n",
		b.Prompts, b.Messages, b.CommandCount, len(b.Files), b.ToolFailures)

	w.WriteString("\n## Where it stood\n\n")
	quoted(&w, "First request", b.FirstRequest)
	if b.LatestRequest != b.FirstRequest {
		quoted(&w, "Latest request", b.LatestRequest)
	}
	quoted(&w, "Last agent message", b.LastMessage)

	if len(b.Files) > 0 {
		w.WriteString("## Files changed\n\n")
		files := b.Files
		if len(files) > briefFilesListed {
			files = files[:briefFilesListed]
		}
		for _, f := range files {
			fmt.Fprintf(&w, "- %s (%s", code(f.Path), inline(strings.Join(f.Operations, ", ")))
			if f.Count > 1 {
				fmt.Fprintf(&w, ", %d times", f.Count)
			}
			w.WriteString(")\n")
		}
		if extra := len(b.Files) - len(files); extra > 0 {
			fmt.Fprintf(&w, "- …and %d more\n", extra)
		}
		w.WriteString("\n")
	}

	if len(b.Commands) > 0 {
		w.WriteString("## Recent commands\n\n")
		commands := b.Commands
		if len(commands) > briefCommandsListed {
			commands = commands[len(commands)-briefCommandsListed:]
		}
		for _, c := range commands {
			fmt.Fprintf(&w, "- %s%s\n", code(clip(c.Command, 200)), exitSuffix(c.ExitCode))
		}
		w.WriteString("\n")
	}

	w.WriteString("## Recent activity\n\n")
	if omitted > 0 {
		fmt.Fprintf(&w, "_%d earlier steps are not shown._\n\n", omitted)
	}
	if len(tail) == 0 {
		w.WriteString("_No activity was recorded._\n\n")
	}
	for _, entry := range tail {
		fmt.Fprintf(&w, "### %s", entry.Kind)
		if entry.Head != "" {
			fmt.Fprintf(&w, ": %s", inline(clip(entry.Head, 200)))
		}
		if entry.At != "" {
			fmt.Fprintf(&w, " · %s", inline(entry.At))
		}
		w.WriteString("\n\n")
		if body := clip(entry.Body, briefEntryRunes); body != "" {
			fenced(&w, body)
		}
	}

	w.WriteString("## Before continuing\n\n")
	w.WriteString("- The working tree may have changed since this session ended. Check `git status` and the files above before relying on them.\n")
	w.WriteString("- Say what you understand the task to be, and confirm it with the user, before changing anything.\n")
	return w.String()
}

func field(w *strings.Builder, name, value string) {
	if value == "" {
		value = "unknown"
	}
	fmt.Fprintf(w, "- %s: %s\n", name, value)
}

// code writes value as an inline code span on one line. The delimiter is longer than any backtick
// run inside, so the value cannot end the span early.
func code(value string) string {
	value = inline(value)
	if value == "" {
		return ""
	}
	longest, run := 0, 0
	for _, r := range value {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	delim := strings.Repeat("`", longest+1)
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		return delim + " " + value + " " + delim
	}
	return delim + value + delim
}

// inline reduces value to one line of text. Every field outside a fenced block goes through it: a
// path, title or branch carrying a newline would otherwise start its own line, and a line such as
// "## Before continuing" would read as the brief's own structure.
func inline(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 || r == 0x85 {
			return ' '
		}
		return r
	}, value)
	return oneLine(value)
}

func quoted(w *strings.Builder, name, text string) {
	fmt.Fprintf(w, "**%s**\n\n", name)
	text = clip(text, briefRequestRunes)
	if text == "" {
		w.WriteString("_None recorded._\n\n")
		return
	}
	fenced(w, text)
}

// fenced writes text as a code block whose fence is longer than any backtick run inside it, so
// content can never close the block early and be read as brief structure.
func fenced(w *strings.Builder, text string) {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	fmt.Fprintf(w, "%s\n%s\n%s\n\n", fence, text, fence)
}

// clip cuts text to max runes, marking the cut.
func clip(text string, max int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimSpace(string(runes[:max])) + " […]"
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8Start(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	if len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
