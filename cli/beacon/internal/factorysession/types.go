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

// UsageTotals is one cumulative reading of a session's settings usage. It is also the shape of
// the baseline the collector keeps per session.
//
// The fields follow Factory's own split, which is the Anthropic one: inputTokens counts uncached
// input only, with cache reads and cache writes in fields of their own, so the canonical usage
// carries them side by side without subtracting one from another.
type UsageTotals struct {
	InputTokens         int64   `json:"input_tokens,omitempty"`
	OutputTokens        int64   `json:"output_tokens,omitempty"`
	CacheCreationTokens int64   `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64   `json:"cache_read_tokens,omitempty"`
	ThinkingTokens      int64   `json:"thinking_tokens,omitempty"`
	FactoryCredits      float64 `json:"factory_credits,omitempty"`
}

// SettingsUsageTotals picks the session's own cumulative usage out of its settings.
//
// tokenUsage is the session's own usage. inclusiveTokenUsage adds the usage of the child sessions
// listed in childInclusiveTokenUsageBySessionId; those children are Factory sessions with settings
// of their own, which Beacon collects separately, so counting the inclusive total on the parent
// would count each child twice. The inclusive total is used only when tokenUsage is absent and no
// child is listed, the one case in which it is the session's own usage. Both readings are then the
// same quantity, so a session whose settings move from one field to the other keeps one consistent
// baseline.
func SettingsUsageTotals(settings *Settings) *UsageTotals {
	if settings == nil {
		return nil
	}
	usage := settings.TokenUsage
	if usage == nil && len(settings.ChildInclusiveTokenUsage) == 0 {
		usage = settings.InclusiveTokenUsage
	}
	if usage == nil {
		return nil
	}
	return &UsageTotals{
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		ThinkingTokens:      usage.ThinkingTokens,
		FactoryCredits:      usage.FactoryCredits,
	}
}

// since is how much each field grew from previous. A field that went down means the settings
// were rewritten rather than that usage was refunded: it contributes nothing, and because the
// collector then stores the lower reading as the new baseline, growth after the rewrite is still
// counted. That is the per-field rule the Codex and Copilot session readers apply to their
// cumulative totals; it can undercount across a rewrite but never double counts one.
func (u UsageTotals) since(previous UsageTotals) UsageTotals {
	grow := func(current, prior int64) int64 {
		if current <= prior {
			return 0
		}
		return current - prior
	}
	credits := u.FactoryCredits - previous.FactoryCredits
	if credits < 0 {
		credits = 0
	}
	return UsageTotals{
		InputTokens:         grow(u.InputTokens, previous.InputTokens),
		OutputTokens:        grow(u.OutputTokens, previous.OutputTokens),
		CacheCreationTokens: grow(u.CacheCreationTokens, previous.CacheCreationTokens),
		CacheReadTokens:     grow(u.CacheReadTokens, previous.CacheReadTokens),
		ThinkingTokens:      grow(u.ThinkingTokens, previous.ThinkingTokens),
		FactoryCredits:      credits,
	}
}

func (u UsageTotals) empty() bool {
	return u == UsageTotals{}
}

type MappedEvent struct {
	SourceLine int
	DedupID    string
	Event      schema.Event
}
