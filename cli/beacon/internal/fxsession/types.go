// Package fxsession reads the session store that fx (vercel-labs/fx) keeps on disk and turns it
// into Beacon endpoint telemetry.
//
// fx is the one supported runtime with no third-party observation surface at all. Its lifecycle
// hooks are Zig handlers compiled into the binary rather than commands a settings file can point
// at, it ships no OpenTelemetry export, and its plugin story is MCP -- which describes tools fx
// calls, not what fx did. What it does have is a durable, append-only record of every session:
//
//	~/.fx/sessions/<session-id>/
//	  events.jsonl   append-only event log, one JSON envelope per line
//	  session.json   manifest projection: the committed prefix of events.jsonl, plus metadata
//
// So Beacon reads that. The consequence for anyone reading the resulting events is recorded on
// every one of them as harness.collection_method=poll: Beacon sees what a turn produced once the
// turn was committed, not the agent as it works. A pre-tool hook can hold a tool call; this cannot.
//
// This file declares the decoded shape of fx's records. The field names and enum spellings come
// from fx's own writers (src/core/session/session_codec.zig and session_event.zig), which encode
// JSON by hand rather than through a schema, so every name here was read off the writer that
// produces it rather than inferred from the reader that consumes it.
package fxsession

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Event kinds fx writes into events.jsonl. The list is fx's complete Kind enum; Beacon decodes the
// payload of the ones it maps and skips the rest by name rather than by absence, so a kind fx adds
// later is skipped as unknown rather than mistaken for one of these.
const (
	KindSessionStarted           = "session_started"
	KindPreferencesChanged       = "preferences_changed"
	KindWorkspaceRebound         = "workspace_rebound"
	KindHistoryTurnCommitted     = "history_turn_committed"
	KindUsageCheckpointed        = "usage_checkpointed"
	KindRecoveryCheckpointSet    = "recovery_checkpoint_set"
	KindRecoveryCheckpointClear  = "recovery_checkpoint_cleared"
	KindStateReplacementStarted  = "state_replacement_started"
	KindStateReplacementChunk    = "state_replacement_chunk"
	KindStateReplacementCommited = "state_replacement_committed"
)

// SupportedSchemaVersion is the only events.jsonl envelope version fx writes, and the only one this
// decoder accepts. fx itself rejects any other value rather than reading it leniently; Beacon skips
// the line for the same reason, since a version bump means the fields below moved.
const SupportedSchemaVersion = 1

// Turn kinds. fx commits four shapes of history turn, and which one a turn is decides what is
// present: only `assistant` and `background_command` carry a full execution record, `interrupted`
// carries the tool call that was in flight when the user stopped it, and `compacted_summary` is
// fx's own summary of turns it dropped rather than a turn the user ran.
const (
	TurnKindAssistant        = "assistant"
	TurnKindBackgroundCmd    = "background_command"
	TurnKindInterrupted      = "interrupted"
	TurnKindCompactedSummary = "compacted_summary"
)

// Tool result statuses (fx's PersistedToolStatus).
const (
	ToolStatusSuccess = "success"
	ToolStatusFailure = "failure"
)

// File actions fx attributes to a tool call (fx's FileEvidenceAction).
const (
	FileActionRead    = "read"
	FileActionWrite   = "write"
	FileActionEdit    = "edit"
	FileActionDelete  = "delete"
	FileActionRename  = "rename"
	FileActionCopy    = "copy"
	FileActionSearch  = "search"
	FileActionList    = "list"
	FileActionUnknown = "unknown"
)

// Process outcome kinds fx records for a command (fx's CommandProcessPresentation). `value` carries
// the exit status or signal number for the first two and is null for the others.
const (
	ProcessExitCode            = "exit_code"
	ProcessSignal              = "signal"
	ProcessTimedOut            = "timed_out"
	ProcessOutputCaptureFailed = "output_capture_failed"
)

// Envelope is one line of events.jsonl: fx's frame around a payload.
//
// Seq is monotonic within LogGeneration and restarts when fx compacts the log, which is why the
// collector's cursor stores both. Payload stays raw so a caller can decode only the kinds it maps.
type Envelope struct {
	SchemaVersion int             `json:"schema_version"`
	LogGeneration string          `json:"log_generation"`
	Seq           uint64          `json:"seq"`
	EventID       string          `json:"event_id"`
	TimestampMS   int64           `json:"timestamp_ms"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
}

// Event is a decoded envelope. Exactly one of the payload pointers is set for a kind Beacon maps;
// all of them are nil for a kind it skips, which is why Kind is kept on the struct rather than
// implied by which pointer is non-nil.
type Event struct {
	Envelope
	SessionStarted     *SessionStarted
	PreferencesChanged *Preferences
	WorkspaceRebound   *WorkspaceRebound
	TurnCommitted      *TurnCommitted
	UsageCheckpointed  *UsageSnapshot
}

// SessionStarted is fx's first event in a log. It also reappears as seq 1 of a *new* generation
// after fx compacts the log, so a collector that emits a Beacon session.started for every one of
// these emits a second start for a session that never restarted. See the cursor in collect.go.
type SessionStarted struct {
	ID                  string         `json:"id"`
	CreatedAtMS         int64          `json:"created_at_ms"`
	OriginWorkspaceRoot DurableString  `json:"origin_workspace_root"`
	WorkspaceRoot       DurableString  `json:"workspace_root"`
	ConversationLang    string         `json:"conversation_language"`
	Preferences         *Preferences   `json:"preferences"`
	Usage               *UsageSnapshot `json:"usage"`
}

// Preferences is the model configuration in force: fx's provider (gateway, codex, grok), the model
// id as that provider names it, the reasoning effort, and whether fast mode is on.
type Preferences struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	FastMode *bool  `json:"fast_mode"`
}

// WorkspaceRebound is fx resuming a session in a different directory. Sessions are global in fx and
// portable across workspaces, so the working directory of a session is not fixed for its lifetime.
type WorkspaceRebound struct {
	PreviousWorkspaceRoot DurableString `json:"previous_workspace_root"`
	WorkspaceRoot         DurableString `json:"workspace_root"`
}

// TurnCommitted is fx's record of one completed turn: the prompt, everything the agent did, and the
// session's running token totals at that point.
//
// TotalInput/TotalOutputTokens are cumulative for the session, not for this turn. Per-turn tokens
// live in Turn.Execution.TurnSummary.TokenProgress, and mixing the two up would report a session's
// entire history as the cost of its last turn.
type TurnCommitted struct {
	ConversationLang  string `json:"conversation_language"`
	TotalInputTokens  uint64 `json:"total_input_tokens"`
	TotalOutputTokens uint64 `json:"total_output_tokens"`
	WorkID            string `json:"work_id"`
	Turn              Turn   `json:"turn"`
}

// Turn is one entry of fx's conversation history. The four kinds share a user prompt and diverge
// after it; see the TurnKind constants.
type Turn struct {
	Kind      string     `json:"kind"`
	User      *UserTurn  `json:"user"`
	Assistant *string    `json:"assistant"`
	Execution *Execution `json:"execution"`

	// background_command
	LogPath   DurableString `json:"log_path"`
	ExpectURL bool          `json:"expect_url"`
	URL       *string       `json:"url"`

	// interrupted
	ToolCall           *ToolCall `json:"tool_call"`
	CompletedToolNames []string  `json:"completed_tool_names"`
	TerminalReason     string    `json:"terminal_reason"`

	// compacted_summary
	Summary          DurableString `json:"summary"`
	RemovedTurnCount int           `json:"removed_turn_count"`
	CompactionCount  int           `json:"compaction_count"`
}

// UserTurn is the prompt as the user submitted it, plus any images attached to it.
type UserTurn struct {
	Text   DurableString `json:"text"`
	Images []UserImage   `json:"images"`
	WorkID string        `json:"work_id"`
}

// UserImage is one attachment. Beacon records the path and media type, never the bytes: the image
// itself is a file on the machine, and copying it into a log line would put unbounded binary
// content into a stream meant to be greppable.
type UserImage struct {
	ID        int64         `json:"id"`
	Path      DurableString `json:"path"`
	MediaType DurableString `json:"media_type"`
}

// Execution is what the agent did during a turn: the model/tool steps it took, the files it
// touched, and how long the turn ran.
type Execution struct {
	SchemaVersion int          `json:"schema_version"`
	ToolSteps     []ToolStep   `json:"tool_steps"`
	Files         []FileRecord `json:"files"`
	TurnSummary   *TurnSummary `json:"turn_summary"`
}

// ToolStep is one model step: the assistant text that preceded the calls, the calls it made, and
// their results. Calls and results are separate lists linked by tool call id, and a call can be
// present with no result when the turn ended before it completed.
type ToolStep struct {
	Assistant   *string      `json:"assistant"`
	ToolCalls   []ToolCall   `json:"tool_calls"`
	ToolResults []ToolResult `json:"tool_results"`
}

// ToolCall is one tool invocation as the model requested it. ID is fx's own identifier for the
// call, which is what links it to its result and is what Beacon promotes to gen_ai.tool.call.id.
type ToolCall struct {
	ID             DurableString `json:"id"`
	Name           DurableString `json:"name"`
	ArgumentsJSON  DurableString `json:"arguments_json"`
	ProviderResult *string       `json:"provider_result"`
}

// ToolResult is the outcome fx persisted for one call.
//
// Output is what fx stored, which is not always what the tool produced: OutputBytes is the true
// size, StoredOutputBytes is what survived fx's own limit, and Truncated says the two differ. All
// three are carried through to Beacon rather than collapsed, because a reader who cannot tell a
// short output from a truncated one cannot tell what the agent actually saw.
type ToolResult struct {
	ToolCallID         DurableString    `json:"tool_call_id"`
	ToolName           DurableString    `json:"tool_name"`
	Status             string           `json:"status"`
	Output             DurableString    `json:"output"`
	OutputHandle       *string          `json:"output_handle"`
	Preview            *string          `json:"preview"`
	OutputBytes        int64            `json:"output_bytes"`
	StoredOutputBytes  int64            `json:"stored_output_bytes"`
	Truncated          bool             `json:"truncated"`
	ProviderNative     bool             `json:"provider_native"`
	CreatedAtMS        int64            `json:"created_at_ms"`
	PermissionFeedback []DurableString  `json:"permission_feedback"`
	CommittedFile      *CommittedFile   `json:"committed_file_presentation"`
	CommandProcess     *ProcessOutcome  `json:"command_process_presentation"`
	TerminalAction     *TerminalOutcome `json:"terminal_action_presentation"`
}

// CommittedFile is the diff fx committed for a write or edit: the path, whether the file was added
// or edited, the unified-diff lines, and the line counts.
type CommittedFile struct {
	Path       DurableString `json:"path"`
	Kind       string        `json:"kind"`
	Lines      []DiffLine    `json:"lines"`
	Additions  int           `json:"additions"`
	Deletions  int           `json:"deletions"`
	Truncated  bool          `json:"truncated"`
	LifecycleD *Lifecycle    `json:"lifecycle_id"`
}

// DiffLine is one line of fx's committed diff. Kind is context, addition, deletion, elision, or
// notice -- the last two are fx's own markers for omitted regions, not file content.
type DiffLine struct {
	Kind    string        `json:"kind"`
	OldLine *int          `json:"old_line"`
	NewLine *int          `json:"new_line"`
	Text    DurableString `json:"text"`
}

// Lifecycle ties a committed file back to the turn and tool call that produced it.
type Lifecycle struct {
	TurnID int64         `json:"turn_id"`
	CallID DurableString `json:"call_id"`
}

// ProcessOutcome is how a command ended: an exit code, a signal, a timeout, or a failure to capture
// its output. Value is null for the last two, which is why it is a pointer.
type ProcessOutcome struct {
	Kind  string `json:"kind"`
	Value *int   `json:"value"`
}

// TerminalOutcome is how a durable terminal action ended. fx uses durable terminal sessions for
// long-lived work (servers, watchers), where "the command exited" is one outcome among several.
type TerminalOutcome struct {
	Kind    string           `json:"kind"`
	Code    string           `json:"code"`
	Outcome *TerminalReturns `json:"outcome"`
}

// TerminalReturns is the nested outcome of a returned terminal action: started, condition_met,
// safety_ceiling, cancelled, exited (with a code), or signal (with a number).
type TerminalReturns struct {
	Kind  string `json:"kind"`
	Value *int   `json:"value"`
}

// FileRecord is fx's per-turn evidence that a tool touched a file: which file, which call, what it
// did, and whether it succeeded. It exists alongside CommittedFile because not every file action
// produces a diff -- a read, a delete, or a failed write all leave a record here and nothing there.
type FileRecord struct {
	Path         DurableString `json:"path"`
	NewPath      *string       `json:"new_path"`
	ToolCallID   DurableString `json:"tool_call_id"`
	ToolName     DurableString `json:"tool_name"`
	Action       string        `json:"action"`
	Status       string        `json:"status"`
	FullFileSeen bool          `json:"model_view_covers_full_file"`
	Stale        bool          `json:"stale"`
}

// TurnSummary is fx's timing and token accounting for one turn.
//
// InputExact/OutputExact matter: when a provider does not return usage, fx estimates the counts and
// says so here. Beacon carries the flags through rather than presenting an estimate as a measured
// number.
type TurnSummary struct {
	StartedAtMS        int64         `json:"started_at_ms"`
	CompletedAtMS      int64         `json:"completed_at_ms"`
	ThinkingDurationMS int64         `json:"thinking_duration_ms"`
	TurnDurationMS     int64         `json:"turn_duration_ms"`
	TokenProgress      TokenProgress `json:"token_progress"`
}

// TokenProgress is one turn's token counts. Unlike TurnCommitted's totals, these are per turn.
type TokenProgress struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	InputExact   bool  `json:"input_exact"`
	OutputExact  bool  `json:"output_exact"`
}

// UsageSnapshot is fx's cumulative usage for a session: tokens, cost, and per-model breakdown.
//
// Cumulative is the word that matters. Every checkpoint replaces the previous one rather than
// adding to it, so emitting a snapshot's numbers as an event's gen_ai.usage would count a session's
// whole history once per checkpoint in any rollup that sums events. The collector emits the
// difference between consecutive snapshots instead; see usageDelta in mapper.go.
type UsageSnapshot struct {
	Billing                string       `json:"billing"`
	APIDurationMS          int64        `json:"api_duration_ms"`
	WallDurationMS         int64        `json:"wall_duration_ms"`
	TotalCost              float64      `json:"total_cost"`
	InputTokens            int64        `json:"input_tokens"`
	OutputTokens           int64        `json:"output_tokens"`
	CacheReadTokens        int64        `json:"cache_read_tokens"`
	CacheWriteTokens       int64        `json:"cache_write_tokens"`
	BillableWebSearchCalls int64        `json:"billable_web_search_calls"`
	LinesAdded             int64        `json:"lines_added"`
	LinesRemoved           int64        `json:"lines_removed"`
	Models                 []ModelUsage `json:"models"`
}

// ModelUsage is one model's share of a usage snapshot.
type ModelUsage struct {
	Model            string  `json:"model"`
	TotalCost        float64 `json:"total_cost"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  *int64  `json:"reasoning_tokens"`
}

// DurableString is a string field in fx's session records, which is either a JSON string or, when
// the bytes are not valid UTF-8, the object {"encoding":"base64","data":"..."}.
//
// fx writes every user-supplied byte sequence this way -- a path, a prompt, a tool's output, a diff
// line -- because those bytes come from a filesystem and a terminal, neither of which promises
// UTF-8. A decoder that assumed a plain string would fail on exactly the sessions worth looking at:
// the one that read a binary file or ran a command that emitted a stray 0x80.
//
// The decoded bytes are kept as-is rather than being re-encoded or rejected. Go strings hold
// arbitrary bytes, and the JSON encoder that writes the Beacon event substitutes U+FFFD for
// anything invalid at that point -- so the invalid byte is visible as a replacement character in
// the log instead of failing the line or silently dropping the field.
type DurableString string

func (s DurableString) String() string { return string(s) }

// UnmarshalJSON accepts both of fx's encodings. A null decodes to the empty string, which is what
// the optional fields (an absent url, a nil assistant) mean.
func (s *DurableString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*s = ""
		return nil
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*s = DurableString(text)
		return nil
	}
	var wrapper struct {
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return fmt.Errorf("fx durable string: %w", err)
	}
	if wrapper.Encoding != "base64" {
		return fmt.Errorf("fx durable string: unsupported encoding %q", wrapper.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(wrapper.Data)
	if err != nil {
		return fmt.Errorf("fx durable string: %w", err)
	}
	// fx only uses the base64 form for bytes that are not valid UTF-8, and rejects a base64 payload
	// that decodes to valid UTF-8 as a malformed record. Beacon accepts it rather than failing the
	// line: the bytes are unambiguous either way, and refusing them would drop a whole turn over a
	// disagreement about how it was spelled.
	*s = DurableString(decoded)
	return nil
}

// MarshalJSON round-trips through the same two forms, so a decoded record re-encodes to something
// fx would accept. Tests use it; nothing in the collector writes fx records back.
func (s DurableString) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(string(s)) {
		return json.Marshal(string(s))
	}
	return json.Marshal(struct {
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}{Encoding: "base64", Data: base64.StdEncoding.EncodeToString([]byte(s))})
}

// ErrUnsupportedSchema is returned for an envelope whose schema_version is not the one this
// decoder understands. Callers skip the line; see Decoder.Next.
var ErrUnsupportedSchema = errors.New("unsupported fx event schema version")
