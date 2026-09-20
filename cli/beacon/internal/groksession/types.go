package groksession

import (
	"encoding/json"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const Harness = "grok"

type SessionRef struct {
	ID            string
	Workspace     string
	SourcePath    string
	ModTimeUnixMS int64
	Summary       *Summary
}

type Summary struct {
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	GeneratedTitle  string `json:"generated_title"`
	SessionSummary  string `json:"session_summary"`
	CurrentModelID  string `json:"current_model_id"`
	HeadBranch      string `json:"head_branch"`
	HeadCommit      string `json:"head_commit"`
	SessionKind     string `json:"session_kind"`
	NumMessages     int    `json:"num_messages"`
	NumChatMessages int    `json:"num_chat_messages"`
	ChatFormat      int    `json:"chat_format_version"`
	LastActiveAt    string `json:"last_active_at"`
	RequestID       string `json:"request_id"`
	GrokHome        string `json:"grok_home"`
}

type PromptContext struct {
	WorkingDirectory string `json:"working_directory"`
	OSName           string `json:"os_name"`
	ShellPath        string `json:"shell_path"`
	Version          int    `json:"version"`
	PromptMode       string `json:"prompt_mode"`
	IsNonInteractive bool   `json:"is_non_interactive"`
}

type ChatMessage struct {
	Type             string          `json:"type"`
	Content          json.RawMessage `json:"content"`
	Reasoning        *Reasoning      `json:"reasoning,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ModelID          string          `json:"model_id,omitempty"`
	ModelFingerprint string          `json:"model_fingerprint,omitempty"`
	SyntheticReason  string          `json:"synthetic_reason,omitempty"`
	Index            int             `json:"-"`
}

type Reasoning struct {
	Text      string `json:"text,omitempty"`
	Encrypted string `json:"encrypted,omitempty"`
	ID        string `json:"id,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type LifecycleEvent struct {
	Type      string `json:"type"`
	TS        string `json:"ts"`
	SessionID string `json:"session_id,omitempty"`
	// TurnNumber is a pointer because Grok numbers turns from zero. As a plain int with omitempty,
	// the first turn's number is indistinguishable from a row that carries no turn number at all,
	// and reporting either as the other invents or drops a fact the source stated.
	TurnNumber               *int   `json:"turn_number,omitempty"`
	ModelID                  string `json:"model_id,omitempty"`
	ToolName                 string `json:"tool_name,omitempty"`
	Decision                 string `json:"decision,omitempty"`
	WaitMS                   int64  `json:"wait_ms,omitempty"`
	DurationMS               int64  `json:"duration_ms,omitempty"`
	Outcome                  string `json:"outcome,omitempty"`
	CancellationCategory     string `json:"cancellation_category,omitempty"`
	ConversationMessageCount int    `json:"conversation_message_count,omitempty"`
	Index                    int    `json:"-"`
}

type SessionData struct {
	Ref       SessionRef
	Prompt    *PromptContext
	Chat      []ChatMessage
	Lifecycle []LifecycleEvent
	// ChatLines and LifecycleLines are how many lines each JSONL file held, which is not len(Chat)
	// or len(Lifecycle): a blank or unparseable line counts here and produces no record. They are
	// what the collector's line cursor is comparable against.
	ChatLines      int
	LifecycleLines int
	TerminalLogs   map[string]string
}

// SourceKind names where a mapped event came from, so the collector can keep one cursor per
// append-only source file instead of re-reading a session whole. A Grok session directory has two
// of them -- chat_history.jsonl and events.jsonl -- and they grow independently, so a single
// "how far did we get" number cannot describe both.
type SourceKind string

const (
	// SourceSession marks an event derived from the session as a whole (summary.json plus the
	// first turn_started row) rather than from a numbered line of either log. There is exactly
	// one, session.started, and the cursor records it as a flag rather than a position.
	SourceSession SourceKind = "session"
	// SourceChat marks an event mapped from a chat_history.jsonl line.
	SourceChat SourceKind = "chat"
	// SourceLifecycle marks an event mapped from an events.jsonl line.
	SourceLifecycle SourceKind = "lifecycle"
)

// MapOptions carries the collector's cursor into the mapper. The mapper always reads the whole
// session -- a tool_result line names a tool call recorded in an earlier assistant line, so the
// context has to be rebuilt every sweep -- but emits only what lies past the cursor.
type MapOptions struct {
	// MinChatLine is the last chat_history.jsonl line already written; lines at or below it are
	// skipped.
	MinChatLine int
	// MinLifecycleLine is the last events.jsonl line already written.
	MinLifecycleLine int
	// SkipStarted suppresses session.started once it has been written for this session.
	SkipStarted bool
}

type MappedEvent struct {
	Event schema.Event
	// SourceKind and SourceLine locate the record this event was mapped from, which is what the
	// collector advances its cursor over.
	SourceKind SourceKind
	SourceLine int
}

type Stats struct {
	Malformed int
}

func parseMillis(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
