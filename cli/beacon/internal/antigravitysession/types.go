// Package antigravitysession reads the conversations Antigravity CLI (`agy`) commits under
// ~/.gemini/antigravity-cli and maps them to endpoint events marked
// harness.collection_method=poll.
//
// One conversation is one brain directory, ~/.gemini/antigravity-cli/brain/<conversationId>/, whose
// .system_generated/logs/transcript_full.jsonl holds one step per line. The conversation's working
// directory, title and parent conversation live beside it in conversation_summaries.db.
package antigravitysession

import (
	"encoding/json"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// Harness is the canonical harness name every event from this collector carries. It is the name
// asymptoteobserve.NormalizeHarnessName gives "antigravity" and the one the live hooks write, so
// a conversation collected both ways is one runtime in every report.
const Harness = "antigravity_cli"

// SessionRef locates one conversation.
type SessionRef struct {
	// ID is the conversationId, the brain directory's name, which is also the session id the live
	// hooks put on their events.
	ID string
	// SourcePath is the brain directory.
	SourcePath string
	// TranscriptPath is the transcript the conversation is read from: transcript_full.jsonl, or
	// transcript.jsonl when the full transcript is absent.
	TranscriptPath string
	// Full says TranscriptPath is transcript_full.jsonl, whose tool arguments are typed values.
	// transcript.jsonl carries each argument JSON-encoded as a string.
	Full bool
	// ModTimeUnixMS is the transcript's modification time.
	ModTimeUnixMS int64
	// Summary is the conversation's row in conversation_summaries.db, or nil when there is none.
	Summary *Summary
}

// Summary is what Beacon reads from a conversation_summaries.db row. The table also holds a
// protobuf blob, which is not read.
type Summary struct {
	Title string
	// WorkingDirectory is the first of the row's workspace_uris, as a local path.
	WorkingDirectory     string
	ProjectID            string
	ParentConversationID string
	NestingDepth         int64
}

// Step is one line of a transcript.
type Step struct {
	// StepIndex is a pointer because the first step is numbered zero, and a step with no index
	// must not be mistaken for it.
	StepIndex       *int64     `json:"step_index,omitempty"`
	Source          string     `json:"source,omitempty"`
	Type            string     `json:"type"`
	Status          string     `json:"status,omitempty"`
	CreatedAt       string     `json:"created_at,omitempty"`
	Content         string     `json:"content,omitempty"`
	Thinking        string     `json:"thinking,omitempty"`
	InputTokens     *int64     `json:"input_tokens,omitempty"`
	CacheReadTokens *int64     `json:"cache_read_tokens,omitempty"`
	OutputTokens    *int64     `json:"output_tokens,omitempty"`
	ToolCalls       []ToolCall `json:"tool_calls,omitempty"`
	// Line is the step's physical line number in the transcript, which the collector's cursor
	// counts in.
	Line int `json:"-"`
}

// ToolCall is one call in a planner response. Antigravity gives a call no id: its result is the
// next GENERIC step in order.
type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
	// Arguments is Args decoded, with transcript.jsonl's string-encoded values decoded too.
	Arguments map[string]interface{} `json:"-"`
}

// SessionData is one conversation as read.
type SessionData struct {
	Ref   SessionRef
	Steps []Step
	// Lines is how many lines the transcript held, which is not len(Steps): a blank or
	// unparseable line counts here and produces no step. It is what the collector's line cursor
	// is comparable against.
	Lines int
}

// SourceKind names where a mapped event came from.
type SourceKind string

const (
	// SourceSession marks session.started, which is derived from the conversation as a whole and
	// is recorded in the cursor as a flag rather than a position.
	SourceSession SourceKind = "session"
	// SourceTranscript marks an event mapped from a numbered transcript line.
	SourceTranscript SourceKind = "transcript"
)

// MapOptions carries the collector's cursor into the mapper. The mapper always reads the whole
// conversation -- a GENERIC step is the result of a call named on an earlier planner line, so the
// pairing has to be rebuilt every sweep -- but emits only what lies past the cursor.
type MapOptions struct {
	// MinLine is the last transcript line already written; lines at or below it are skipped.
	MinLine int
	// SkipStarted suppresses session.started once it has been written for this conversation.
	SkipStarted bool
}

type MappedEvent struct {
	Event      schema.Event
	SourceKind SourceKind
	SourceLine int
	// TimestampMS is the source step's created_at, zero when it had none. The collector compares
	// it against live hook coverage.
	TimestampMS int64
}

type Stats struct {
	Malformed int
}
