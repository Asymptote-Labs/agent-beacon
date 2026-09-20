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
	Type                     string `json:"type"`
	TS                       string `json:"ts"`
	SessionID                string `json:"session_id,omitempty"`
	TurnNumber               int    `json:"turn_number,omitempty"`
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
	Ref          SessionRef
	Prompt       *PromptContext
	Chat         []ChatMessage
	Lifecycle    []LifecycleEvent
	TerminalLogs map[string]string
}

type MappedEvent struct {
	Event     schema.Event
	SourceSeq int
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
