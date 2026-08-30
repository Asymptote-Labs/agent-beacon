package fxsession

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// The turn record is the payload the whole integration rests on: it is the only place fx records
// what the agent did. This test reads one off a fixture transcribed from fx's writers and asserts
// every field the mapper will later read, because a silently-nil sub-record here becomes a missing
// event downstream with nothing to explain it.
func TestDecodeAssistantTurnCarriesEveryExecutionRecord(t *testing.T) {
	event, err := DecodeEnvelope([]byte(assistantTurnLine(3)))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if event.Kind != KindHistoryTurnCommitted {
		t.Fatalf("kind = %q, want %q", event.Kind, KindHistoryTurnCommitted)
	}
	if event.Seq != 3 || event.LogGeneration != testGeneration {
		t.Errorf("envelope identity = seq %d generation %q, want seq 3 generation %q",
			event.Seq, event.LogGeneration, testGeneration)
	}
	committed := event.TurnCommitted
	if committed == nil {
		t.Fatal("TurnCommitted is nil for a history_turn_committed event")
	}
	if committed.TotalInputTokens != 4200 || committed.TotalOutputTokens != 880 {
		t.Errorf("session totals = %d/%d, want 4200/880", committed.TotalInputTokens, committed.TotalOutputTokens)
	}
	turn := committed.Turn
	if turn.Kind != TurnKindAssistant {
		t.Fatalf("turn kind = %q, want %q", turn.Kind, TurnKindAssistant)
	}
	if turn.User == nil || turn.User.Text != "add a health endpoint and run the tests" {
		t.Fatalf("user prompt not decoded: %+v", turn.User)
	}
	if turn.Assistant == nil || *turn.Assistant == "" {
		t.Error("assistant text missing")
	}
	if turn.Execution == nil {
		t.Fatal("execution missing")
	}
	if got := len(turn.Execution.ToolSteps); got != 2 {
		t.Fatalf("tool steps = %d, want 2", got)
	}

	read := turn.Execution.ToolSteps[0]
	if len(read.ToolCalls) != 1 || read.ToolCalls[0].Name != "read_file" {
		t.Fatalf("first step calls = %+v", read.ToolCalls)
	}
	if read.ToolCalls[0].ArgumentsJSON != `{"path":"src/server.ts"}` {
		t.Errorf("arguments_json = %q, want the tool's own JSON preserved verbatim", read.ToolCalls[0].ArgumentsJSON)
	}
	if len(read.ToolResults) != 1 || read.ToolResults[0].Status != ToolStatusSuccess {
		t.Fatalf("first step results = %+v", read.ToolResults)
	}

	work := turn.Execution.ToolSteps[1]
	if len(work.ToolCalls) != 2 || len(work.ToolResults) != 2 {
		t.Fatalf("second step = %d calls / %d results, want 2/2", len(work.ToolCalls), len(work.ToolResults))
	}
	edit := work.ToolResults[0]
	if edit.CommittedFile == nil {
		t.Fatal("edit result carries no committed_file_presentation, so no diff would reach the log")
	}
	if edit.CommittedFile.Path != "src/server.ts" || edit.CommittedFile.Kind != "edited" {
		t.Errorf("committed file = %+v", edit.CommittedFile)
	}
	if edit.CommittedFile.Additions != 1 || edit.CommittedFile.Deletions != 1 {
		t.Errorf("diff counts = +%d/-%d, want +1/-1", edit.CommittedFile.Additions, edit.CommittedFile.Deletions)
	}
	if got := len(edit.CommittedFile.Lines); got != 3 {
		t.Errorf("diff lines = %d, want 3", got)
	}
	if edit.CommittedFile.LifecycleD == nil || edit.CommittedFile.LifecycleD.CallID != "call_edit_1" {
		t.Errorf("lifecycle id = %+v, want the edit's call id", edit.CommittedFile.LifecycleD)
	}

	command := work.ToolResults[1]
	if command.Status != ToolStatusFailure {
		t.Errorf("command status = %q, want %q", command.Status, ToolStatusFailure)
	}
	if command.CommandProcess == nil || command.CommandProcess.Kind != ProcessExitCode {
		t.Fatalf("command process outcome = %+v", command.CommandProcess)
	}
	if command.CommandProcess.Value == nil || *command.CommandProcess.Value != 1 {
		t.Errorf("exit code = %+v, want 1", command.CommandProcess.Value)
	}
	// fx stored 13 of 4096 bytes. Both numbers have to survive: a reader that sees only the stored
	// size cannot tell a short output from a truncated one.
	if command.OutputBytes != 4096 || command.StoredOutputBytes != 13 || !command.Truncated {
		t.Errorf("output accounting = %d/%d truncated=%v, want 4096/13 truncated=true",
			command.OutputBytes, command.StoredOutputBytes, command.Truncated)
	}
	if len(command.PermissionFeedback) != 1 || command.PermissionFeedback[0] != "approved by rule" {
		t.Errorf("permission feedback = %+v", command.PermissionFeedback)
	}

	if got := len(turn.Execution.Files); got != 2 {
		t.Fatalf("file evidence = %d records, want 2", got)
	}
	if turn.Execution.Files[0].Action != FileActionRead || turn.Execution.Files[1].Action != FileActionEdit {
		t.Errorf("file actions = %q/%q, want read/edit",
			turn.Execution.Files[0].Action, turn.Execution.Files[1].Action)
	}
	summary := turn.Execution.TurnSummary
	if summary == nil {
		t.Fatal("turn summary missing, so the turn's own token counts would be lost")
	}
	if summary.TokenProgress.InputTokens != 1200 || summary.TokenProgress.OutputTokens != 340 {
		t.Errorf("turn tokens = %d/%d, want 1200/340",
			summary.TokenProgress.InputTokens, summary.TokenProgress.OutputTokens)
	}
	// fx says the output count is an estimate. Carrying the flag is what keeps Beacon from
	// presenting an estimate as a measurement.
	if !summary.TokenProgress.InputExact || summary.TokenProgress.OutputExact {
		t.Errorf("exactness flags = in %v / out %v, want true/false",
			summary.TokenProgress.InputExact, summary.TokenProgress.OutputExact)
	}
}

// A session's cumulative totals and its per-turn counts are different numbers in the same record.
// Reading one for the other reports a session's entire history as the cost of its last turn, which
// is the kind of error a token report shows without ever looking wrong.
func TestSessionTotalsAndTurnTokensAreDistinctFields(t *testing.T) {
	event, err := DecodeEnvelope([]byte(assistantTurnLine(3)))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	committed := event.TurnCommitted
	if committed.TotalInputTokens == uint64(committed.Turn.Execution.TurnSummary.TokenProgress.InputTokens) {
		t.Fatal("fixture no longer distinguishes cumulative totals from per-turn counts")
	}
}

func TestDecodeSessionStartedCarriesWorkspaceAndPreferences(t *testing.T) {
	event, err := DecodeEnvelope([]byte(sessionStartedLine(1, "/home/dev/api")))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	started := event.SessionStarted
	if started == nil {
		t.Fatal("SessionStarted is nil")
	}
	if started.ID != testSessionID {
		t.Errorf("session id = %q, want %q", started.ID, testSessionID)
	}
	if started.WorkspaceRoot != "/home/dev/api" {
		t.Errorf("workspace root = %q", started.WorkspaceRoot)
	}
	if started.Preferences == nil || started.Preferences.Model != "anthropic/claude-opus-4" {
		t.Fatalf("preferences = %+v", started.Preferences)
	}
	if started.Preferences.Provider != "gateway" {
		t.Errorf("provider = %q, want gateway", started.Preferences.Provider)
	}
}

func TestDecodeUsageCheckpointCarriesCostAndCacheTokens(t *testing.T) {
	event, err := DecodeEnvelope([]byte(usageCheckpointLine(4, 4200, 880, 0.1234)))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	usage := event.UsageCheckpointed
	if usage == nil {
		t.Fatal("UsageCheckpointed is nil")
	}
	if usage.InputTokens != 4200 || usage.OutputTokens != 880 {
		t.Errorf("tokens = %d/%d, want 4200/880", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CacheReadTokens != 900 || usage.CacheWriteTokens != 120 {
		t.Errorf("cache tokens = %d/%d, want 900/120", usage.CacheReadTokens, usage.CacheWriteTokens)
	}
	if usage.TotalCost != 0.1234 {
		t.Errorf("total cost = %v, want 0.1234", usage.TotalCost)
	}
	if len(usage.Models) != 1 || usage.Models[0].Model != "anthropic/claude-opus-4" {
		t.Fatalf("model breakdown = %+v", usage.Models)
	}
	if usage.Models[0].ReasoningTokens == nil || *usage.Models[0].ReasoningTokens != 64 {
		t.Errorf("reasoning tokens = %+v, want 64", usage.Models[0].ReasoningTokens)
	}
}

// fx rejects an events.jsonl frame whose schema_version it does not know rather than reading it
// leniently, because a version bump means the fields moved. Beacon skips the line for the same
// reason -- but skipping one line must not cost the rest of the session.
func TestUnsupportedSchemaVersionSkipsTheLineNotTheSession(t *testing.T) {
	future := strings.Replace(assistantTurnLine(3), `"schema_version":1`, `"schema_version":99`, 1)
	lines := []string{sessionStartedLine(1, "/repo"), future, usageCheckpointLine(4, 10, 5, 0.01)}

	events, stats := decodeAll(t, strings.Join(lines, "\n")+"\n")
	if len(events) != 2 {
		t.Fatalf("decoded %d events, want 2 (the start and the usage checkpoint)", len(events))
	}
	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1", stats.Skipped)
	}
	if stats.Malformed != 0 {
		t.Errorf("stats.Malformed = %d, want 0 -- a future version is not corruption", stats.Malformed)
	}
	if _, err := DecodeEnvelope([]byte(future)); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("DecodeEnvelope error = %v, want ErrUnsupportedSchema", err)
	}
}

// fx writes kinds Beacon has no mapping for -- recovery checkpoints, and the chunked state blob it
// writes when it compacts a log. They must be stepped over silently: they describe fx's own
// storage, not anything the agent did.
func TestStorageOnlyKindsAreSkipped(t *testing.T) {
	for _, kind := range []string{
		KindRecoveryCheckpointSet, KindRecoveryCheckpointClear,
		KindStateReplacementStarted, KindStateReplacementChunk, KindStateReplacementCommited,
		"some_kind_fx_adds_in_2027",
	} {
		t.Run(kind, func(t *testing.T) {
			line := frame(2, 1770000002000, kind, `{"anything":true}`)
			events, stats := decodeAll(t, line+"\n")
			if len(events) != 0 {
				t.Fatalf("decoded %d events for %q, want 0", len(events), kind)
			}
			if stats.Skipped != 1 || stats.Malformed != 0 {
				t.Errorf("stats = %+v, want one skip and no malformed line", stats)
			}
		})
	}
}

// A log Beacon reads while fx is writing it ends mid-frame. That tail is not corruption and must
// not be reported as such: the frame is simply not there yet, and the next sweep reads it whole.
func TestPartialTrailingFrameIsNotTreatedAsCorruption(t *testing.T) {
	complete := sessionStartedLine(1, "/repo")
	partial := assistantTurnLine(2)
	body := complete + "\n" + partial[:len(partial)/2]

	events, stats := decodeAll(t, body)
	if len(events) != 1 {
		t.Fatalf("decoded %d events, want 1", len(events))
	}
	if !stats.PartialTail {
		t.Error("stats.PartialTail = false, want true for a log that ends mid-frame")
	}
	if stats.Malformed != 0 {
		t.Errorf("stats.Malformed = %d, want 0 -- an unfinished append is not a damaged record", stats.Malformed)
	}
}

// A line that is complete but not decodable is corruption, and one corrupt line is not a reason to
// abandon the good lines around it. Both halves of that sentence are asserted here.
func TestMalformedLineIsCountedAndSteppedOver(t *testing.T) {
	body := strings.Join([]string{
		sessionStartedLine(1, "/repo"),
		`{"schema_version":1,"log_generation":"x","seq":2,"kind":"history_turn_committed","payload":{`,
		usageCheckpointLine(3, 10, 5, 0.02),
	}, "\n") + "\n"

	events, stats := decodeAll(t, body)
	if len(events) != 2 {
		t.Fatalf("decoded %d events, want 2", len(events))
	}
	if stats.Malformed != 1 {
		t.Errorf("stats.Malformed = %d, want 1", stats.Malformed)
	}
	if stats.FirstError == nil {
		t.Error("stats.FirstError is nil; a caller has nothing to report")
	}
}

// A frame larger than fx will ever write is corruption -- two frames with a lost newline, or a
// partially overwritten file. The decoder must discard it without buffering it, and must keep
// reading afterwards.
func TestOversizedFrameIsDiscardedAndReadingContinues(t *testing.T) {
	oversized := `{"schema_version":1,"kind":"history_turn_committed","payload":"` +
		strings.Repeat("x", MaxFrameBytes+1024) + `"}`
	body := oversized + "\n" + sessionStartedLine(2, "/repo") + "\n"

	events, stats := decodeAll(t, body)
	if len(events) != 1 {
		t.Fatalf("decoded %d events, want the one good line after the oversized frame", len(events))
	}
	if stats.Malformed != 1 {
		t.Errorf("stats.Malformed = %d, want 1", stats.Malformed)
	}
	if stats.PartialTail {
		t.Error("stats.PartialTail = true; an oversized frame is corruption, not an append in flight")
	}
}

// Blank lines and \r\n endings both occur: the first from an editor that touched the file, the
// second from a log copied through a Windows tool. Neither is a record and neither is damage.
func TestBlankLinesAndCarriageReturnsAreTolerated(t *testing.T) {
	body := "\n" + sessionStartedLine(1, "/repo") + "\r\n" + "\n" + usageCheckpointLine(2, 1, 1, 0.0) + "\n"
	events, stats := decodeAll(t, body)
	if len(events) != 2 {
		t.Fatalf("decoded %d events, want 2", len(events))
	}
	if stats.Malformed != 0 {
		t.Errorf("stats.Malformed = %d, want 0", stats.Malformed)
	}
}

// fx writes a byte sequence that is not valid UTF-8 as {"encoding":"base64","data":...} rather than
// as a JSON string. Those are the sessions worth reading -- the agent read a binary file, or a
// command printed a stray byte -- so a decoder that assumed a plain string would drop exactly the
// turns an investigator came for.
func TestDurableStringDecodesBothOfFxEncodings(t *testing.T) {
	var plain DurableString
	if err := json.Unmarshal([]byte(`"hello"`), &plain); err != nil {
		t.Fatalf("plain string: %v", err)
	}
	if plain != "hello" {
		t.Errorf("plain = %q, want %q", plain, "hello")
	}

	var binary DurableString
	if err := json.Unmarshal([]byte(`{"encoding":"base64","data":"/w=="}`), &binary); err != nil {
		t.Fatalf("base64 object: %v", err)
	}
	if string(binary) != "\xff" {
		t.Errorf("binary = %q, want the decoded 0xff byte", string(binary))
	}

	var missing DurableString
	if err := json.Unmarshal([]byte(`null`), &missing); err != nil {
		t.Fatalf("null: %v", err)
	}
	if missing != "" {
		t.Errorf("null decoded to %q, want the empty string", missing)
	}

	if _, err := (DurableString("ok")).MarshalJSON(); err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	roundTrip, err := json.Marshal(binary)
	if err != nil {
		t.Fatalf("marshal binary: %v", err)
	}
	if !strings.Contains(string(roundTrip), `"encoding":"base64"`) {
		t.Errorf("binary re-encoded as %s, want fx's base64 form", roundTrip)
	}
}

// An unsupported encoding is a decode failure rather than a silently empty field. A tool output
// that Beacon cannot read must not reach the log as an empty string, which would read as "the tool
// returned nothing".
func TestDurableStringRejectsUnknownEncoding(t *testing.T) {
	var value DurableString
	err := json.Unmarshal([]byte(`{"encoding":"rot13","data":"uryyb"}`), &value)
	if err == nil {
		t.Fatal("unknown encoding decoded without error")
	}
	if !strings.Contains(err.Error(), "rot13") {
		t.Errorf("error = %v, want it to name the encoding", err)
	}
}

func TestDecodeEnvelopeRejectsRecordsWithNoKind(t *testing.T) {
	_, err := DecodeEnvelope([]byte(`{"schema_version":1,"log_generation":"x","seq":1,"payload":{}}`))
	if err == nil {
		t.Fatal("envelope with no kind decoded without error")
	}
}

// A mapped kind with no payload is a broken record, not an empty one: fx never writes a
// history_turn_committed without a turn, so accepting it would put an event with no content into
// the log.
func TestMappedKindWithoutPayloadIsMalformed(t *testing.T) {
	line := `{"schema_version":1,"log_generation":"x","seq":1,"event_id":"y","timestamp_ms":1,"kind":"history_turn_committed"}`
	if _, err := DecodeEnvelope([]byte(line)); err == nil {
		t.Fatal("history_turn_committed with no payload decoded without error")
	}
}

func decodeAll(t *testing.T, body string) ([]Event, Stats) {
	t.Helper()
	decoder := NewDecoder(strings.NewReader(body), 0)
	var events []Event
	for {
		event, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Decoder.Next: %v", err)
		}
		events = append(events, *event)
	}
	return events, decoder.Stats()
}
