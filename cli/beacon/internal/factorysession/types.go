// Package factorysession reads Factory Droid's local session store and maps it to Beacon events.
//
// Factory already has live Beacon support through OTLP plus optional hooks. This package covers
// the other source Factory keeps on disk:
//
//	~/.factory/sessions/<encoded-working-directory>/<session-id>.jsonl
//	~/.factory/sessions/<encoded-working-directory>/<session-id>.settings.json
//
// Reading that store is a poll collection path: Beacon sees committed records after the fact, so
// every event emitted here carries harness.collection_method=poll.
package factorysession

import (
	"encoding/json"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const Harness = "factory"

const (
	RecordSessionStart    = "session_start"
	RecordMessage         = "message"
	RecordTodoState       = "todo_state"
	RecordCompactionState = "compaction_state"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

const (
	BlockText       = "text"
	BlockThinking   = "thinking"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

type Record struct {
	Line      int             `json:"-"`
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Title     string          `json:"title"`
	CWD       string          `json:"cwd"`
	Message   *Message        `json:"message"`
	Raw       json.RawMessage `json:"-"`

	SummaryText   string `json:"summaryText"`
	SummaryTokens *int64 `json:"summaryTokens"`
	AnchorMessage *struct {
		ID string `json:"id"`
	} `json:"anchorMessage"`
}

type Message struct {
	Role       string          `json:"role"`
	Visibility string          `json:"visibility"`
	Model      string          `json:"model"`
	Content    json.RawMessage `json:"content"`
	Thinking   string          `json:"thinking"`
}

type Block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

type Settings struct {
	Model                    string         `json:"model"`
	ProviderLock             string         `json:"providerLock"`
	AssistantActiveTimeMs    int64          `json:"assistantActiveTimeMs"`
	TokenUsage               *TokenUsage    `json:"tokenUsage"`
	InclusiveTokenUsage      *TokenUsage    `json:"inclusiveTokenUsage"`
	ChildInclusiveTokenUsage map[string]any `json:"childInclusiveTokenUsageBySessionId"`
	Raw                      map[string]any `json:"-"`
}

type TokenUsage struct {
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	ThinkingTokens      int64   `json:"thinkingTokens"`
	FactoryCredits      float64 `json:"factoryCredits"`
}

type MappedEvent struct {
	SourceLine int
	DedupID    string
	Event      schema.Event
}
