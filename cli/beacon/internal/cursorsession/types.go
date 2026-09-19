// Package cursorsession reads Cursor's local conversation stores and converts
// committed session records into Beacon endpoint telemetry.
//
// Cursor has two durable stores that matter here. Recent Composer sessions live
// in the VS Code global storage SQLite database under cursorDiskKV keys such as
// `composerData:<id>` and `bubbleId:<id>:<bubble>`. Cursor Agent transcripts
// are JSON/JSONL files under ~/.cursor/projects/*/agent-transcripts. Both are
// local records written after the fact, so every event produced here is marked
// harness.collection_method=poll.
package cursorsession

import (
	"encoding/json"
	"time"
)

const Harness = "cursor"

type SourceKind string

const (
	SourceGlobalStorage SourceKind = "global_storage"
	SourceTranscript    SourceKind = "agent_transcript"
)

type TraceRef struct {
	ID               string
	Kind             SourceKind
	SourcePath       string
	Title            string
	Preview          string
	Workspace        string
	UpdatedAtUnixMS  int64
	CreatedAtUnixMS  int64
	SizeBytes        int64
	RelationshipType string
	RelatedTo        string
}

type Record struct {
	Order              int
	NativeID           string
	Type               string
	Subtype            string
	TimestampMS        int64
	DurationMS         int64
	ContextUsedTokens  int64
	ContextLimitTokens int64
	Content            string
	Model              string
	CallID             string
	ToolName           string
	Args               map[string]interface{}
	Output             string
	Status             string
	Data               map[string]interface{}
}

type composerData struct {
	ComposerID                  string                   `json:"composerId"`
	Name                        string                   `json:"name"`
	Text                        string                   `json:"text"`
	RichText                    string                   `json:"richText"`
	CreatedAt                   jsonTimeOrNumber         `json:"createdAt"`
	LastUpdatedAt               jsonTimeOrNumber         `json:"lastUpdatedAt"`
	FullConversationHeadersOnly []conversationHeader     `json:"fullConversationHeadersOnly"`
	ModelConfig                 *composerModelConfig     `json:"modelConfig"`
	Context                     map[string]interface{}   `json:"context"`
	LatestConversationSummary   *latestConversationState `json:"latestConversationSummary"`
	SubagentComposerIDs         []string                 `json:"subagentComposerIds"`
	ContextTokens               int64                    `json:"context_tokens"`
	ContextTokensCamel          int64                    `json:"contextTokens"`
	ContextWindowSize           int64                    `json:"context_window_size"`
	ContextWindowSizeCamel      int64                    `json:"contextWindowSize"`
	ContextTokenBudget          int64                    `json:"contextTokenBudget"`
}

type composerModelConfig struct {
	ModelName string `json:"modelName"`
}

type conversationHeader struct {
	BubbleID string `json:"bubbleId"`
	Type     int    `json:"type"`
}

type bubbleData struct {
	Type              int                    `json:"type"`
	Text              string                 `json:"text"`
	CreatedAt         jsonTimeOrNumber       `json:"createdAt"`
	TurnDurationMS    int64                  `json:"turnDurationMs"`
	ServerBubbleID    string                 `json:"serverBubbleId"`
	WorkspaceURIs     []string               `json:"workspaceUris"`
	Context           map[string]interface{} `json:"context"`
	ConversationState string                 `json:"conversationState"`
	Thinking          *struct {
		Text string `json:"text"`
	} `json:"thinking"`
	ThinkingDurationMS int64 `json:"thinkingDurationMs"`
	ContextTokens      int64 `json:"context_tokens"`
	ContextTokensCamel int64 `json:"contextTokens"`
	ContextWindowSize  int64 `json:"context_window_size"`
	ContextWindowCamel int64 `json:"contextWindowSize"`
	ContextTokenBudget int64 `json:"contextTokenBudget"`
	AllThinkingBlocks  []struct {
		Thinking string `json:"thinking"`
	} `json:"allThinkingBlocks"`
	ToolFormerData *toolFormerData `json:"toolFormerData"`
	ErrorDetails   *struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	} `json:"errorDetails"`
}

type toolFormerData struct {
	Name           string                 `json:"name"`
	Params         string                 `json:"params"`
	Result         string                 `json:"result"`
	Status         string                 `json:"status"`
	AdditionalData map[string]interface{} `json:"additionalData"`
}

type latestConversationState struct {
	LastBubbleID string               `json:"lastBubbleId"`
	Summary      *conversationSummary `json:"summary"`
}

type conversationSummary struct {
	TruncationLastBubbleIDInclusive       string `json:"truncationLastBubbleIdInclusive"`
	PreviousConversationSummaryBubbleID   string `json:"previousConversationSummaryBubbleId"`
	ClientShouldStartSendingFromInclusive string `json:"clientShouldStartSendingFromInclusiveBubbleId"`
	Summary                               string `json:"summary"`
	IncludesToolResults                   *bool  `json:"includesToolResults"`
}

type transcriptRecord map[string]interface{}

type jsonTimeOrNumber struct {
	Millis int64
}

func (t *jsonTimeOrNumber) UnmarshalJSON(data []byte) error {
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		t.Millis = int64(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, s); err == nil {
		t.Millis = parsed.UnixMilli()
		return nil
	}
	if parsed, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s); err == nil {
		t.Millis = parsed.UnixMilli()
		return nil
	}
	return nil
}
