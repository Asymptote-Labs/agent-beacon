package dashboard

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// The writer may truncate in place without changing the first line. Both generations then have
// the same device, inode, head and second-line offset, even when their public event IDs repeat.
func historyGenerationFixture(t *testing.T, name string) (head, old, fresh []byte) {
	t.Helper()
	event := func(session, id, text string) schema.Event {
		e := testSchemaEvent("2026-06-11T10:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a")
		e.Session = &schema.SessionInfo{ID: name + "-" + session}
		e.Event.ID = id
		e.Prompt = &schema.PromptInfo{Text: text}
		return e
	}
	lines := marshalEvents(t,
		event("head", "", "identical generation head"),
		event("old", "repeated-public-id", "retained generation "+strings.Repeat("older ", 300)),
		event("fresh", "repeated-public-id", "fresh generation canary"))
	return lines[0], lines[1], lines[2]
}

func openGenerationStore(t *testing.T) *historyStore {
	t.Helper()
	s, err := openHistoryStore(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

func assertGenerationTrace(t *testing.T, path, name string) {
	t.Helper()
	show, ok, err := ShowTrace(path, "session:cursor:"+name, TraceQuery{Limit: 10})
	if err != nil || !ok || len(show.Events) != 1 {
		t.Fatalf("ShowTrace(%s): found=%v events=%d err=%v; want the retained source event", name, ok, len(show.Events), err)
	}
}

func generationRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if data, ok := value.([]byte); ok {
				values[i] = bytes.Clone(data)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHistoryReconcilesSameHeadRewrittenGeneration(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "rewrite")
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	s := openGenerationStore(t)
	before := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`)
	versions := generationRows(t, s.db, `SELECT key, value FROM meta WHERE key IN ('schema_version', 'derivation_version', 'line_codec') ORDER BY key`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeTestLog(t, path, head, fresh)
	rewritten, err := os.Stat(path)
	if err != nil || !os.SameFile(info, rewritten) || rewritten.Size() >= info.Size() {
		t.Fatalf("fixture must shrink the same inode: %v", err)
	}
	t.Run("raw control", func(t *testing.T) {
		withoutHistory(t)
		assertGenerationTrace(t, path, "rewrite-fresh")
	})
	assertGenerationTrace(t, path, "rewrite-fresh")
	assertGenerationTrace(t, path, "rewrite-old")
	after := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`)
	if len(after) != 3 || !reflect.DeepEqual(before, after[:2]) {
		t.Fatalf("rewrite changed retained IDs/bytes or duplicated the unchanged head: before=%v after=%v", before, after)
	}
	for _, level := range []string{"event", "trace"} {
		found, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "fresh generation canary"}, ResultLevel: level, Limit: 10})
		if err != nil || found.TotalMatched != 1 {
			t.Fatalf("%s search after rewrite = %#v, %v", level, found, err)
		}
	}
	// Losing just the checkpoint or the upgrade marker must replay identical retained bytes once.
	for _, reset := range []string{`UPDATE files SET offset = 0, lines = 0`, `DELETE FROM meta WHERE key LIKE 'generation_reconciled:%'`} {
		if _, err := s.db.Exec(reset); err != nil {
			t.Fatal(err)
		}
		if _, err := TraceStoreStatus(path); err != nil {
			t.Fatal(err)
		}
		if got := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`); !reflect.DeepEqual(after, got) {
			t.Fatalf("unchanged crash replay duplicated or rewrote events: %v", got)
		}
	}
	// A second rewrite must retire a different retained row without colliding with the first.
	next := bytes.Replace(fresh, []byte("fresh generation canary"), []byte("next"), 1)
	next = bytes.Replace(next, []byte("rewrite-fresh"), []byte("rewrite-next"), 1)
	writeTestLog(t, path, head, next)
	assertGenerationTrace(t, path, "rewrite-next")
	assertGenerationTrace(t, path, "rewrite-fresh")
	nextRows := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`)
	if len(nextRows) != 4 || !reflect.DeepEqual(after, nextRows[:3]) {
		t.Fatal("a later generation changed or replaced retained event identities/bytes")
	}
	if _, err := EnableHistoryStore(path, HistoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.setMeta("derivation_version", "1"); err != nil {
		t.Fatal(err)
	}
	assertGenerationTrace(t, path, "rewrite-old")
	assertGenerationTrace(t, path, "rewrite-next")
	if got := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`); !reflect.DeepEqual(nextRows, got) {
		t.Fatal("force catch-up or derived rebuild changed retained identities/bytes")
	}
	if got := generationRows(t, s.db, `SELECT key, value FROM meta WHERE key IN ('schema_version', 'derivation_version', 'line_codec') ORDER BY key`); !reflect.DeepEqual(versions, got) {
		t.Fatalf("generation repair changed the schema, derivation or codec: %v", got)
	}
	assertGenerationSearchIntegrity(t, s.db)
}

// Reproduce the old production state, including the broken catch-up's advanced EOF checkpoint.
// A future-shrink fix alone cannot discover the fresh row from this state on normal reopen.
func seedSkippedGeneration(t *testing.T, s *historyStore, path string, head, fresh []byte) int64 {
	t.Helper()
	writeTestLog(t, path, head, fresh)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := s.sourceID(s.db, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE files SET offset = ?, size = ?, lines = 2 WHERE source_id = ?`, info.Size(), info.Size(), sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM meta WHERE key = ?`, fmt.Sprintf("generation_reconciled:%d", sourceID)); err != nil {
		t.Fatal(err)
	}
	return sourceID
}

func TestHistoryRecoversSkippedGenerationsForEachSource(t *testing.T) {
	withHistory(t)
	s := (*historyStore)(nil)
	paths := make([]string, 2)
	for i := range paths {
		head, old, fresh := historyGenerationFixture(t, fmt.Sprintf("source%d", i))
		paths[i] = newTestLog(t, [][]byte{head, old})
		optIn(t, paths[i])
		if s == nil {
			s = openGenerationStore(t)
		}
		seedSkippedGeneration(t, s, paths[i], head, fresh)
	}
	for i, path := range paths {
		assertGenerationTrace(t, path, fmt.Sprintf("source%d-fresh", i))
		assertGenerationTrace(t, path, fmt.Sprintf("source%d-old", i))
	}
	var markers, events int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key LIKE 'generation_reconciled:%'`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if markers != 2 || events != 6 {
		t.Fatalf("per-source reconciliation: markers=%d events=%d, want 2 and 6", markers, events)
	}
}

func generationSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	for _, table := range []string{"meta", "sources", "files", "events", "traces", "trace_checkpoints", "event_fts", "trace_fts"} {
		out[table] = generationRows(t, db, `SELECT * FROM `+table+` ORDER BY 1`)
	}
	return out
}

func assertGenerationSearchIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"event_fts", "trace_fts"} {
		if _, err := db.Exec(`INSERT INTO ` + table + ` (` + table + `, rank) VALUES ('integrity-check', 1)`); err != nil {
			t.Fatalf("%s content/index integrity: %v", table, err)
		}
	}
}

func TestHistoryGenerationReconciliationRollsBackAndRetries(t *testing.T) {
	for _, fault := range []struct{ name, boundary string }{
		{"retirement", "BEFORE UPDATE OF file_key ON events"},
		{"event", "BEFORE INSERT ON events WHEN NEW.trace_key = 'session:cursor:fault-fresh' AND NOT EXISTS (SELECT 1 FROM events WHERE source_id = NEW.source_id AND file_key = NEW.file_key AND offset = NEW.offset)"},
		{"checkpoint", "BEFORE UPDATE ON files"},
		{"summary", "BEFORE INSERT ON traces WHEN NEW.trace_key = 'session:cursor:fault-fresh'"},
		{"marker", "BEFORE INSERT ON meta WHEN NEW.key LIKE 'generation_reconciled:%'"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			head, old, fresh := historyGenerationFixture(t, "fault")
			path := traceStoreIndexedLog(t, [][]byte{head, old})
			s := openGenerationStore(t)
			seedSkippedGeneration(t, s, path, head, fresh)
			before := generationSnapshot(t, s.db)
			if _, err := s.db.Exec(`CREATE TRIGGER generation_fault ` + fault.boundary + ` BEGIN SELECT RAISE(ABORT, 'generation fault'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.catchUp(path, nil, false); err == nil || !strings.Contains(err.Error(), "generation fault") {
				t.Fatalf("fault did not reach %s boundary: %v", fault.name, err)
			}
			if after := generationSnapshot(t, s.db); !reflect.DeepEqual(before, after) {
				t.Fatalf("%s failure partly committed rows, indexes, summaries, checkpoint or marker", fault.name)
			}
			assertGenerationSearchIntegrity(t, s.db)
			if _, err := s.db.Exec(`DROP TRIGGER generation_fault`); err != nil {
				t.Fatal(err)
			}
			s.close()
			s = openGenerationStore(t)
			if _, err := s.catchUp(path, nil, false); err != nil {
				t.Fatal(err)
			}
			assertGenerationTrace(t, path, "fault-fresh")
			assertGenerationTrace(t, path, "fault-old")
			after := generationSnapshot(t, s.db)
			if _, err := s.catchUp(path, nil, false); err != nil {
				t.Fatal(err)
			}
			if got := generationSnapshot(t, s.db); !reflect.DeepEqual(after, got) {
				t.Fatal("successful reopen retry was not idempotent")
			}
			assertGenerationSearchIntegrity(t, s.db)
		})
	}
}

func TestHistoryGenerationReconciliationRollsBackSearchInsertFailure(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "search-fault")
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	s := openGenerationStore(t)
	seedSkippedGeneration(t, s, path, head, fresh)
	// SQLite forbids triggers on virtual tables. A constrained replacement exercises the same
	// prepared search insert failing after retirement and the new event insert have succeeded.
	for _, stmt := range []string{
		`DROP TABLE event_fts`,
		`CREATE TABLE event_fts (rowid INTEGER PRIMARY KEY, hay TEXT CHECK (hay NOT LIKE '%fresh generation canary%'))`,
		`INSERT INTO event_fts (rowid, hay) SELECT id, hay FROM events`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	before := generationSnapshot(t, s.db)
	if _, err := s.catchUp(path, nil, false); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("search insert fault was not reached: %v", err)
	}
	if after := generationSnapshot(t, s.db); !reflect.DeepEqual(before, after) {
		t.Fatal("search insert failure partly committed reconciliation")
	}
	for _, stmt := range []string{
		`DROP TABLE event_fts`,
		`CREATE VIRTUAL TABLE event_fts USING fts5(hay, content='events', content_rowid='id', tokenize='trigram case_sensitive 1')`,
		`INSERT INTO event_fts (event_fts) VALUES ('rebuild')`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.close()
	s = openGenerationStore(t)
	if _, err := s.catchUp(path, nil, false); err != nil {
		t.Fatal(err)
	}
	assertGenerationTrace(t, path, "search-fault-fresh")
	assertGenerationSearchIntegrity(t, s.db)
}

func TestHistoryGenerationReconciliationReadsRetainedArchive(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "archive")
	old = bytes.Replace(old, []byte(`"id":"repeated-public-id"`), []byte(`"id":""`), 1)
	fresh = bytes.Replace(fresh, []byte(`"id":"repeated-public-id"`), []byte(`"id":""`), 1)
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	s := openGenerationStore(t)
	seedSkippedGeneration(t, s, path, head, fresh)
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	writeTestLog(t, path)
	assertGenerationTrace(t, path, "archive-fresh")
	assertGenerationTrace(t, path, "archive-old")
	for trace, wantID := range map[string]string{"archive-fresh": "archive-1-line-2", "archive-old": "line-2", "archive-head": "archive-1-line-1"} {
		show, ok, err := ShowTrace(path, "session:cursor:"+trace, TraceQuery{Limit: 10})
		if err != nil || !ok || len(show.Events) != 1 || show.Events[0].ID != wantID {
			t.Fatalf("%s retained/current generation identity = %#v, %v; want %s", trace, show, err, wantID)
		}
	}
	assertGenerationSearchIntegrity(t, s.db)
	// Removing a fully read archive must preserve its stored-only generations, without replay.
	if err := os.Remove(path + ".1"); err != nil {
		t.Fatal(err)
	}
	assertGenerationTrace(t, path, "archive-fresh")
	assertGenerationTrace(t, path, "archive-old")
	if got := generationRows(t, s.db, `SELECT id FROM events ORDER BY id`); len(got) != 3 {
		t.Fatalf("archive removal/reopen changed retained rows: %v", got)
	}
}

func TestHistoryRewrittenGenerationRetiresCoveredAndTruncatedRows(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "tail")
	old = bytes.Replace(old, []byte(`"id":"repeated-public-id"`), []byte(`"id":""`), 1)
	fresh = bytes.Replace(fresh, []byte(`"id":"repeated-public-id"`), []byte(`"id":""`), 1)
	short := bytes.Replace(old, []byte("retained generation "+strings.Repeat("older ", 300)), []byte("old"), 1)
	path := traceStoreIndexedLog(t, [][]byte{head, short, old, old})
	s := openGenerationStore(t)
	before := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`)
	writeTestLog(t, path, head, fresh)
	assertGenerationTrace(t, path, "tail-fresh")
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	writeTestLog(t, path)
	// Force an ordinary rotation catch-up; archive-only admission is a separate change.
	if _, err := EnableHistoryStore(path, HistoryOptions{}); err != nil {
		t.Fatal(err)
	}
	show, ok, err := ShowTrace(path, "session:cursor:tail-old", TraceQuery{Limit: 10})
	if err != nil || !ok || len(show.Events) != 3 {
		t.Fatalf("retained older generation = %#v, %v", show, err)
	}
	for i, event := range show.Events {
		if event.ID != fmt.Sprintf("line-%d", i+2) {
			t.Fatalf("old row covered by a replacement line or beyond EOF followed current rotation: %s", event.ID)
		}
	}
	// The matching prefix belongs to the current generation and legitimately follows rotation.
	after := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events WHERE id > 1 ORDER BY id`)
	if len(after) != 4 || !reflect.DeepEqual(before[1:], after[:3]) {
		t.Fatal("retiring old coordinates changed retained row IDs, public IDs or compressed bytes")
	}
	freshShow, ok, err := ShowTrace(path, "session:cursor:tail-fresh", TraceQuery{Limit: 10})
	if err != nil || !ok || freshShow.Events[0].ID != "archive-1-line-2" {
		t.Fatalf("current generation did not follow rotation: %#v, %v", freshShow, err)
	}
	assertGenerationSearchIntegrity(t, s.db)
}

func TestHistoryGenerationReconciliationConcurrentReaders(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "concurrent")
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	first, second := openGenerationStore(t), openGenerationStore(t)
	seedSkippedGeneration(t, first, path, head, fresh)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, s := range []*historyStore{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.catchUp(path, nil, false); err != nil {
				t.Errorf("concurrent reconciliation: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	assertGenerationTrace(t, path, "concurrent-fresh")
	if got := generationRows(t, first.db, `SELECT id FROM events ORDER BY id`); len(got) != 3 {
		t.Fatalf("competing reconciliation duplicated events: %v", got)
	}
	assertGenerationSearchIntegrity(t, first.db)
}

func TestHistoryGenerationReconciliationCannotRecoverUnavailableSource(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "missing")
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	s := openGenerationStore(t)
	seedSkippedGeneration(t, s, path, head, fresh)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.catchUp(path, nil, false); err != nil {
		t.Fatal(err)
	}
	assertGenerationTrace(t, path, "missing-old")
	if _, ok, err := ShowTrace(path, "session:cursor:missing-fresh", TraceQuery{Limit: 10}); err != nil || ok {
		t.Fatalf("unavailable source invented an unstored event: found=%v err=%v", ok, err)
	}
	if got := generationRows(t, s.db, `SELECT id FROM events ORDER BY id`); len(got) != 2 {
		t.Fatalf("unavailable source changed retained events: %v", got)
	}
}

func TestHistoryGenerationReconciliationHonorsRetention(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "expired")
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	s := openGenerationStore(t)
	seedSkippedGeneration(t, s, path, head, fresh)
	if err := s.setMeta("retention_days", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.catchUp(path, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := generationRows(t, s.db, `SELECT id FROM events ORDER BY id`); len(got) != 0 {
		t.Fatalf("reconciliation resurrected expired traces: %v", got)
	}
	assertGenerationSearchIntegrity(t, s.db)
}

func runBlockedGenerationReader(s *historyStore, path string, result chan<- error) {
	_, err := s.catchUp(path, nil, true)
	result <- err
}

func waitForBlockedGenerationReader(t *testing.T) {
	t.Helper()
	stack := make([]byte, 1<<20)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		frames := string(stack[:runtime.Stack(stack, true)])
		for _, goroutine := range strings.Split(frames, "\n\n") {
			if strings.Contains(goroutine, "runBlockedGenerationReader(") && strings.Contains(goroutine, "(*historyStore).lockExclusive(") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("older catch-up did not reach the held history lock")
}

func TestHistoryGenerationOlderSnapshotDoesNotRetireNewerAppend(t *testing.T) {
	head, old, fresh := historyGenerationFixture(t, "blocked")
	path := traceStoreIndexedLog(t, [][]byte{head, old})
	s := openGenerationStore(t)
	before := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`)
	// A private gate holds only the older reader. Its database and source are the real ones,
	// so the newer normal catch-up can commit before the older reader acquires its lock.
	older := *s
	older.path += ".blocked-reader"
	unlock, err := older.lockExclusive()
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(unlock)
	result := make(chan error, 1)
	finished := false
	defer func() {
		release()
		if !finished {
			<-result
		}
	}()
	go runBlockedGenerationReader(&older, path, result)
	waitForBlockedGenerationReader(t)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(append(bytes.Clone(fresh), '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append: %v, close: %v", writeErr, closeErr)
	}
	if _, err := s.catchUp(path, nil, false); err != nil {
		t.Fatal(err)
	}
	release()
	err = <-result
	finished = true
	if err != nil {
		t.Fatal(err)
	}
	status, err := TraceStoreStatus(path)
	if err != nil || status.Events != 3 {
		t.Fatalf("blocked older reader duplicated a newer append: events=%d err=%v", status.Events, err)
	}
	if retired := generationRows(t, s.db, `SELECT id FROM events WHERE file_key LIKE 'retired:%'`); len(retired) != 0 {
		t.Fatalf("older snapshot retired live append coordinates: %v", retired)
	}
	after := generationRows(t, s.db, `SELECT id, record_id, event_id, line FROM events ORDER BY id`)
	if len(after) != 3 || !reflect.DeepEqual(before, after[:2]) {
		t.Fatal("ordinary append changed retained row IDs, public IDs or bytes")
	}
	assertGenerationTrace(t, path, "blocked-fresh")
	assertGenerationSearchIntegrity(t, s.db)
}

func TestHistoryGenerationDetectsShrinkWithinPreviouslyPartialTail(t *testing.T) {
	for _, boundary := range []string{"beyond checkpoint", "at checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			head, old, fresh := historyGenerationFixture(t, "partial")
			path := traceStoreIndexedLog(t, [][]byte{head, old})
			s := openGenerationStore(t)
			complete, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.Write(bytes.Repeat([]byte("x"), 10*1024))
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("partial append: %v, close: %v", writeErr, closeErr)
			}
			if _, err := s.catchUp(path, nil, false); err != nil {
				t.Fatal(err)
			}
			partial, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			padding := 3000
			if boundary == "at checkpoint" {
				padding = len(old) - len(fresh) + len("fresh generation canary")
			}
			fresh = bytes.Replace(fresh, []byte("fresh generation canary"), bytes.Repeat([]byte("n"), padding), 1)
			writeTestLog(t, path, head, fresh)
			rewritten, err := os.Stat(path)
			if err != nil || !os.SameFile(complete, rewritten) || rewritten.Size() < complete.Size() || rewritten.Size() >= partial.Size() {
				t.Fatalf("fixture must shrink the observed file while reaching its complete checkpoint: %v", err)
			}
			if boundary == "at checkpoint" && rewritten.Size() != complete.Size() {
				t.Fatal("fixture must end exactly at the old complete checkpoint")
			}
			assertGenerationTrace(t, path, "partial-fresh")
			assertGenerationTrace(t, path, "partial-old")
			if got := generationRows(t, s.db, `SELECT id FROM events ORDER BY id`); len(got) != 3 {
				t.Fatalf("partial-tail generation replay changed event count: %v", got)
			}
			assertGenerationSearchIntegrity(t, s.db)
		})
	}
}

func TestHistoryRewrittenGenerationRetiresNonEventReplacement(t *testing.T) {
	for _, replacement := range []struct{ name, line string }{{"blank", " "}, {"malformed", `{"broken":`}} {
		t.Run(replacement.name, func(t *testing.T) {
			head, old, fresh := historyGenerationFixture(t, "non-event")
			old = bytes.Replace(old, []byte(`"id":"repeated-public-id"`), []byte(`"id":""`), 1)
			fresh = bytes.Replace(fresh, []byte(`"id":"repeated-public-id"`), []byte(`"id":""`), 1)
			path := traceStoreIndexedLog(t, [][]byte{head, old})
			s := openGenerationStore(t)
			query := `SELECT id, record_id, event_id, line FROM events WHERE trace_key = 'session:cursor:non-event-old'`
			before := generationRows(t, s.db, query)
			writeTestLog(t, path, head, []byte(replacement.line), fresh)
			assertGenerationTrace(t, path, "non-event-fresh")
			if err := os.Rename(path, path+".1"); err != nil {
				t.Fatal(err)
			}
			writeTestLog(t, path)
			if _, err := EnableHistoryStore(path, HistoryOptions{}); err != nil {
				t.Fatal(err)
			}
			show, ok, err := ShowTrace(path, "session:cursor:non-event-fresh", TraceQuery{Limit: 10})
			if err != nil || !ok || len(show.Events) != 1 || show.Events[0].ID != "archive-1-line-3" {
				t.Fatalf("current event after non-event replacement = %#v, %v", show, err)
			}
			t.Run("raw archive parity", func(t *testing.T) {
				withoutHistory(t)
				raw, ok, err := ShowTrace(path, "session:cursor:non-event-fresh", TraceQuery{Limit: 10})
				if err != nil || !ok || mustJSON(t, raw) != mustJSON(t, show) {
					t.Fatalf("current archived trace differs from raw scan: %v", err)
				}
			})
			if _, err := s.db.Exec(`DELETE FROM meta WHERE key LIKE 'generation_reconciled:%'`); err != nil {
				t.Fatal(err)
			}
			assertGenerationTrace(t, path, "non-event-old")
			if got := generationRows(t, s.db, query); !reflect.DeepEqual(before, got) {
				t.Fatal("non-event replacement or archive replay mutated retained IDs/bytes")
			}
			if got := generationRows(t, s.db, `SELECT id FROM events ORDER BY id`); len(got) != 3 {
				t.Fatalf("non-event replacement/replay duplicated events: %v", got)
			}
			assertGenerationSearchIntegrity(t, s.db)
		})
	}
}
