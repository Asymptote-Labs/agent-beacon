package fxsession

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

type sweepFixture struct {
	root      string
	statePath string
	logPath   string
}

func newSweepFixture(t *testing.T) sweepFixture {
	t.Helper()
	dir := t.TempDir()
	return sweepFixture{
		root:      filepath.Join(dir, "sessions"),
		statePath: filepath.Join(dir, "state", "fx.json"),
		logPath:   filepath.Join(dir, "logs", "runtime.jsonl"),
	}
}

func (f sweepFixture) options() CollectOptions {
	return CollectOptions{
		SessionsDir: f.root,
		StatePath:   f.statePath,
		Write:       true,
		LogPath:     f.logPath,
		UserMode:    true,
	}
}

// writeSessionLog lays out a session whose manifest matches exactly what was written, which is what
// fx leaves behind once a turn is committed.
func (f sweepFixture) writeSessionLog(t *testing.T, id string, lines []string) {
	t.Helper()
	manifest := manifestJSON(testGeneration, logBytes(lines), len(lines), "/repo")
	writeSession(t, f.root, id, lines, manifest)
}

func (f sweepFixture) logLines(t *testing.T) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read runtime log: %v", err)
	}
	var events []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode runtime log line: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func TestSweepWritesFxActivityIntoTheRuntimeLog(t *testing.T) {
	f := newSweepFixture(t)
	f.writeSessionLog(t, testSessionID, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)})

	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.Sessions != 1 || summary.SessionsChanged != 1 {
		t.Errorf("summary = %+v, want one session, one changed", summary)
	}
	if summary.EventsEmitted == 0 {
		t.Fatal("no events emitted")
	}

	events := f.logLines(t)
	if len(events) != summary.EventsEmitted {
		t.Fatalf("wrote %d lines for %d emitted events", len(events), summary.EventsEmitted)
	}
	for _, event := range events {
		if event.Harness.Name != Harness {
			t.Errorf("harness = %q", event.Harness.Name)
		}
		// The writer stamps a deterministic id on every line. Without one, the same fx record read
		// twice cannot be recognized as the same event downstream.
		if event.Event.ID == "" {
			t.Errorf("%s has no event id", event.Event.Action)
		}
	}
}

// The property the cursor exists for. A machine sweeping every minute must not append a session's
// history again every minute.
func TestSecondSweepOverUnchangedSessionsWritesNothing(t *testing.T) {
	f := newSweepFixture(t)
	f.writeSessionLog(t, testSessionID, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)})

	first, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	before := len(f.logLines(t))

	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second.EventsEmitted != 0 {
		t.Errorf("second sweep emitted %d events, want 0", second.EventsEmitted)
	}
	if second.SessionsChanged != 0 {
		t.Errorf("second sweep reported %d changed sessions, want 0", second.SessionsChanged)
	}
	if after := len(f.logLines(t)); after != before {
		t.Errorf("runtime log grew from %d to %d lines on a sweep with nothing new", before, after)
	}
	if first.EventsEmitted == 0 {
		t.Fatal("first sweep emitted nothing, so this proves nothing")
	}
}

// A session that gained a turn between sweeps must contribute only that turn.
func TestSweepEmitsOnlyWhatIsNewSinceTheCursor(t *testing.T) {
	f := newSweepFixture(t)
	f.writeSessionLog(t, testSessionID, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)})
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	firstCount := len(f.logLines(t))

	f.writeSessionLog(t, testSessionID, []string{
		sessionStartedLine(1, "/repo"),
		assistantTurnLine(2),
		usageCheckpointLine(3, 4200, 880, 0.75),
	})
	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second.EventsEmitted != 1 {
		t.Fatalf("second sweep emitted %d events, want just the new checkpoint", second.EventsEmitted)
	}
	events := f.logLines(t)
	if len(events) != firstCount+1 {
		t.Fatalf("log has %d lines, want %d", len(events), firstCount+1)
	}
	last := events[len(events)-1]
	if last.Event.Action != "token.usage" {
		t.Errorf("new line action = %q, want token.usage", last.Event.Action)
	}
	// The delta is measured against the snapshot the earlier sweep already saw, which the mapper
	// re-derives by reading the log from the start rather than by storing it in the cursor.
	if last.GenAI == nil || last.GenAI.Usage == nil || last.GenAI.Usage.CostUSD == nil {
		t.Fatalf("usage event carries no cost: %+v", last.GenAI)
	}
}

// fx compacts a session's log by rewriting it under a new generation with the sequence restarted at
// 1. A cursor holding only a sequence would skip everything up to the old high-water mark; a cursor
// that forgot the session had started would report it as starting twice.
func TestCompactedLogIsNeitherSkippedNorRestarted(t *testing.T) {
	f := newSweepFixture(t)
	f.writeSessionLog(t, testSessionID, []string{
		sessionStartedLine(1, "/repo"),
		assistantTurnLine(2),
		usageCheckpointLine(3, 100, 20, 0.05),
	})
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	beforeCompaction := len(f.logLines(t))

	// What fx leaves after compacting: a new generation, a fresh session_started at sequence 1, and
	// the turns that follow it. The compacted history itself is gone from the log.
	const newGeneration = "aaaaaaaabbbbbbbbccccccccdddddddd"
	rewrite := func(line string) string {
		return strings.Replace(line, testGeneration, newGeneration, 1)
	}
	lines := []string{
		rewrite(sessionStartedLine(1, "/repo")),
		rewrite(assistantTurnLine(2)),
	}
	manifest := manifestJSON(newGeneration, logBytes(lines), 2, "/repo")
	writeSession(t, f.root, testSessionID, lines, manifest)

	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second.EventsEmitted == 0 {
		t.Fatal("nothing collected after compaction: the sequence restart hid the new generation")
	}
	events := f.logLines(t)
	starts := 0
	for _, event := range events {
		if event.Event.Action == "session.started" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("session.started appears %d times, want 1 -- compaction is not a new session", starts)
	}
	if len(events) <= beforeCompaction {
		t.Errorf("log did not grow after compaction: %d lines then %d", beforeCompaction, len(events))
	}
}

// The cursor is a position in fx's log, so its size must not grow with the session's history. A
// dedup-id set would grow without bound on a machine that runs fx daily.
func TestCursorStaysOneRecordPerSession(t *testing.T) {
	f := newSweepFixture(t)
	lines := []string{sessionStartedLine(1, "/repo")}
	for seq := 2; seq <= 12; seq++ {
		lines = append(lines, assistantTurnLine(seq))
	}
	f.writeSessionLog(t, testSessionID, lines)
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	state, err := LoadState(f.statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Sessions) != 1 {
		t.Fatalf("state holds %d sessions, want 1", len(state.Sessions))
	}
	cursor := state.Sessions[testSessionID]
	if cursor == nil {
		t.Fatal("no cursor for the swept session")
	}
	if cursor.LastSeq != 12 {
		t.Errorf("cursor.LastSeq = %d, want 12", cursor.LastSeq)
	}
	if cursor.Generation != testGeneration {
		t.Errorf("cursor.Generation = %q", cursor.Generation)
	}
	if !cursor.Started {
		t.Error("cursor does not record that the session was already reported as started")
	}
	data, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	// A cursor file that grew with the number of turns would be the tell that ids are being stored.
	if len(data) > 2048 {
		t.Errorf("cursor file is %d bytes for one session; it should be a position, not a set", len(data))
	}
}

// One unreadable session must not cost the sweep the sessions around it -- and the sweep must still
// say something went wrong rather than reporting a clean run.
func TestOneUnreadableSessionDoesNotStopTheSweep(t *testing.T) {
	f := newSweepFixture(t)
	good := "1770000000009-9-999999"
	f.writeSessionLog(t, good, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)})

	// A session whose log is a directory: readable listing, unreadable log.
	broken := "1770000000001-1-111111"
	brokenDir := filepath.Join(f.root, broken)
	if err := os.MkdirAll(filepath.Join(brokenDir, EventsFileName, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	// The broken session is skipped at listing time (its log is not a file), so the sweep is clean
	// and the good session is collected. What must not happen is the good session being lost.
	if summary.EventsEmitted == 0 {
		t.Fatal("the readable session was not collected")
	}
	events := f.logLines(t)
	for _, event := range events {
		if event.Session == nil || event.Session.ID != good {
			t.Fatalf("unexpected session in the log: %+v", event.Session)
		}
	}
}

// A corrupt line inside an otherwise good session is counted and reported, and the records around
// it are still collected. A sweep that reported a clean run over a damaged log would be worse than
// one that collected nothing.
func TestCorruptLineIsReportedAndTheRestIsStillCollected(t *testing.T) {
	f := newSweepFixture(t)
	lines := []string{
		sessionStartedLine(1, "/repo"),
		`{"schema_version":1,"log_generation":"x","seq":2,"kind":"history_turn_committed","payload":{`,
		usageCheckpointLine(3, 10, 5, 0.02),
	}
	// No manifest: the byte watermark would not match a hand-corrupted log.
	writeSession(t, f.root, testSessionID, lines, "")

	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.MalformedLines != 1 {
		t.Errorf("summary.MalformedLines = %d, want 1", summary.MalformedLines)
	}
	if summary.EventsEmitted == 0 {
		t.Error("the readable records around the corrupt line were dropped")
	}
}

// --print is a dry run: it shows what would be collected without writing the runtime log and
// without advancing any cursor, so running it twice shows the same thing both times.
func TestPrintModeWritesNothingAndKeepsNoCursor(t *testing.T) {
	f := newSweepFixture(t)
	f.writeSessionLog(t, testSessionID, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)})

	var first, second bytes.Buffer
	opts := CollectOptions{SessionsDir: f.root, Print: true, Out: &first}
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("first print sweep: %v", err)
	}
	opts.Out = &second
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("second print sweep: %v", err)
	}

	if first.Len() == 0 {
		t.Fatal("print mode produced no output")
	}
	if first.String() != second.String() {
		t.Error("two dry runs disagreed, so one of them consumed state")
	}
	if _, err := os.Stat(f.logPath); !os.IsNotExist(err) {
		t.Error("print mode wrote the runtime log")
	}
	if _, err := os.Stat(f.statePath); !os.IsNotExist(err) {
		t.Error("print mode wrote a cursor file")
	}
	for _, line := range strings.Split(strings.TrimSpace(first.String()), "\n") {
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("print output is not one JSON event per line: %v", err)
		}
	}
}

// A machine that has never run fx is the common case for most installs. It must produce a clean,
// empty result rather than an error a status command has to explain away.
func TestSweepOverAMissingSessionsDirectoryIsCleanAndEmpty(t *testing.T) {
	f := newSweepFixture(t)
	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary != (Summary{}) {
		t.Errorf("summary = %+v, want a zero summary", summary)
	}
	if _, err := os.Stat(f.logPath); !os.IsNotExist(err) {
		t.Error("a sweep with nothing to collect created a runtime log")
	}
}

// A cursor file written by a future version of the collector is discarded rather than
// misinterpreted: re-collecting is harmless, and reading a cursor under the wrong meaning would
// skip events permanently.
func TestCursorFileFromAnotherVersionIsDiscardedNotMisread(t *testing.T) {
	f := newSweepFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"version":99,"sessions":{"` + testSessionID + `":{"generation":"` + testGeneration + `","last_seq":9999,"started":true}}}`
	if err := os.WriteFile(f.statePath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	f.writeSessionLog(t, testSessionID, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)})

	summary, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.EventsEmitted == 0 {
		t.Fatal("the sweep trusted a cursor from another version and collected nothing")
	}
}

// The manifest is what makes a sweep over a machine with hundreds of old sessions cheap. It must
// not be what decides correctness: a manifest that runs ahead of the log cannot be allowed to
// advance the cursor past records nobody read.
func TestCursorFollowsRecordsReadNotTheManifestsClaim(t *testing.T) {
	f := newSweepFixture(t)
	lines := []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}
	// A manifest claiming sequence 900 over a log that holds two records.
	manifest := manifestJSON(testGeneration, logBytes(lines), 900, "/repo")
	writeSession(t, f.root, testSessionID, lines, manifest)

	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	state, err := LoadState(f.statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := state.Sessions[testSessionID].LastSeq; got != 2 {
		t.Fatalf("cursor.LastSeq = %d, want 2 -- the manifest's claim was trusted over the log", got)
	}
}

// Every event that reaches the runtime log has been through the writer's sanitizer, which redacts
// what looks like a credential. An fx session that printed a token must not put it in the log.
func TestSecretsInFxOutputAreRedactedByTheWriter(t *testing.T) {
	f := newSweepFixture(t)
	line := strings.Replace(assistantTurnLine(2),
		`"status":"failure","output":"1 test failed"`,
		`"status":"failure","output":"api_key=sk-abcdefghijklmnopqrstuvwxyz012345 leaked"`, 1)
	f.writeSessionLog(t, testSessionID, []string{sessionStartedLine(1, "/repo"), line})

	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if strings.Contains(string(data), "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatal("a credential printed by an fx command reached the runtime log verbatim")
	}
	if !strings.Contains(string(data), "REDACTED") {
		t.Error("the command output was dropped rather than redacted")
	}
}
