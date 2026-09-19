// Package codexsession reads Codex CLI's local rollout session files and turns them into
// Beacon endpoint telemetry.
//
// This is an after-the-fact collection path. Codex has already committed the JSONL row before
// Beacon sees it, so every mapped event carries harness.collection_method=poll. That is
// deliberately different from Beacon's live Codex OTLP path, which observes Codex logs and traces
// as the runtime emits them.
package codexsession

import (
	"encoding/json"
	"fmt"
)

const Harness = "codex_cli"

type Entry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`

	SessionMeta      *SessionMeta
	TurnContext      *TurnContext
	ResponseItem     *ResponseItem
	EventMessage     *EventMessage
	TokenUsageRecord *TokenUsageRecord
}

type Record struct {
	Line  int
	Entry Entry
}

type SessionMeta struct {
	SessionID     string                 `json:"session_id,omitempty"`
	ID            string                 `json:"id,omitempty"`
	Timestamp     string                 `json:"timestamp,omitempty"`
	CWD           string                 `json:"cwd,omitempty"`
	Originator    string                 `json:"originator,omitempty"`
	CLIVersion    string                 `json:"cli_version,omitempty"`
	Source        string                 `json:"source,omitempty"`
	ThreadSource  string                 `json:"thread_source,omitempty"`
	ModelProvider string                 `json:"model_provider,omitempty"`
	Git           map[string]interface{} `json:"git,omitempty"`
}

type TurnContext struct {
	TurnID         string                 `json:"turn_id,omitempty"`
	RootTurnID     string                 `json:"root_turn_id,omitempty"`
	CWD            string                 `json:"cwd,omitempty"`
	WorkspaceRoots []string               `json:"workspace_roots,omitempty"`
	Model          string                 `json:"model,omitempty"`
	Effort         string                 `json:"effort,omitempty"`
	ApprovalPolicy string                 `json:"approval_policy,omitempty"`
	SandboxPolicy  map[string]interface{} `json:"sandbox_policy,omitempty"`
}

type ResponseItem struct {
	Type    string      `json:"type"`
	ID      string      `json:"id,omitempty"`
	Role    string      `json:"role,omitempty"`
	Content interface{} `json:"content,omitempty"`
	Status  string      `json:"status,omitempty"`
	CallID  string      `json:"call_id,omitempty"`
	Name    string      `json:"name,omitempty"`
	Input   interface{} `json:"input,omitempty"`
	Output  interface{} `json:"output,omitempty"`
}

type EventMessage struct {
	Type               string          `json:"type"`
	ThreadID           string          `json:"thread_id,omitempty"`
	TurnID             string          `json:"turn_id,omitempty"`
	StartedAt          int64           `json:"started_at,omitempty"`
	CompletedAt        int64           `json:"completed_at,omitempty"`
	DurationMS         int64           `json:"duration_ms,omitempty"`
	TimeToFirstTokenMS int64           `json:"time_to_first_token_ms,omitempty"`
	LastAgentMessage   string          `json:"last_agent_message,omitempty"`
	Info               *TokenCountInfo `json:"info,omitempty"`
}

type TokenCountInfo struct {
	TotalTokenUsage *TokenUsage `json:"total_token_usage,omitempty"`
	LastTokenUsage  *TokenUsage `json:"last_token_usage,omitempty"`
	ContextWindow   int64       `json:"model_context_window,omitempty"`
}

type TokenUsageRecord struct {
	ThreadID         string      `json:"thread_id,omitempty"`
	TurnID           string      `json:"turn_id,omitempty"`
	SessionID        string      `json:"session_id,omitempty"`
	RootTurnID       string      `json:"root_turn_id,omitempty"`
	ResponseID       string      `json:"response_id,omitempty"`
	Usage            *TokenUsage `json:"usage,omitempty"`
	TurnTokenUsage   *TokenUsage `json:"turn_token_usage,omitempty"`
	ThreadTokenUsage *TokenUsage `json:"thread_token_usage,omitempty"`
}

type TokenUsage struct {
	InputTokens           int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           int64 `json:"total_tokens,omitempty"`
}

func decodeEntry(line []byte) (*Entry, error) {
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, fmt.Errorf("codex session entry: %w", err)
	}
	if entry.Type == "" {
		return nil, fmt.Errorf("codex session entry: missing type")
	}
	switch entry.Type {
	case "session_meta":
		var payload SessionMeta
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, fmt.Errorf("codex session_meta: %w", err)
		}
		entry.SessionMeta = &payload
	case "turn_context":
		var payload TurnContext
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, fmt.Errorf("codex turn_context: %w", err)
		}
		entry.TurnContext = &payload
	case "response_item":
		var payload ResponseItem
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, fmt.Errorf("codex response_item: %w", err)
		}
		entry.ResponseItem = &payload
	case "event_msg":
		var payload EventMessage
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, fmt.Errorf("codex event_msg: %w", err)
		}
		entry.EventMessage = &payload
	case "token_usage_record":
		var payload TokenUsageRecord
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, fmt.Errorf("codex token_usage_record: %w", err)
		}
		entry.TokenUsageRecord = &payload
	}
	return &entry, nil
}
