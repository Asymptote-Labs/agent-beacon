package cursorsession

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// The compaction record is built after every bubble, so it takes the next order. When a later
// bubble arrives it takes that same order, which the cursor already sits on, so the sweep skipped it for good and
// reported the compaction again under a new id.
func TestCollectGlobalStorageKeepsBubbleAddedAfterCompaction(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	composer := func(lastUpdatedAt, headers string) string {
		return `{"composerId":"c1","createdAt":"2026-09-19T20:00:00Z","lastUpdatedAt":"` + lastUpdatedAt + `",
			"latestConversationSummary":{"lastBubbleId":"a1","summary":{"truncationLastBubbleIdInclusive":"u1","summary":"s"}},
			"fullConversationHeadersOnly":` + headers + `}`
	}
	writeCursorDB(t, dbPath, map[string]string{
		"composerData:c1": composer("2026-09-19T20:00:03Z", `[{"bubbleId":"u1","type":1},{"bubbleId":"a1","type":2}]`),
		"bubbleId:c1:u1":  `{"type":1,"text":"first prompt","createdAt":"2026-09-19T20:00:00Z"}`,
		"bubbleId:c1:a1":  `{"type":2,"text":"first answer","createdAt":"2026-09-19T20:00:01Z"}`,
	})
	statePath := filepath.Join(t.TempDir(), "state.json")
	collect := func() string {
		var out bytes.Buffer
		if _, err := CollectOnce(CollectOptions{GlobalDBPath: dbPath, ProjectsDir: filepath.Join(t.TempDir(), "none"), StatePath: statePath, Print: true, Out: &out}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	first := collect()
	if !strings.Contains(first, "session.compacting") {
		t.Fatalf("first sweep missing compaction event:\n%s", first)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE cursorDiskKV SET value=? WHERE key='composerData:c1'`, []byte(composer("2026-09-19T20:00:04Z", `[{"bubbleId":"u1","type":1},{"bubbleId":"a1","type":2},{"bubbleId":"u2","type":1}]`))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key,value) VALUES ('bubbleId:c1:u2', ?)`, []byte(`{"type":1,"text":"second prompt","createdAt":"2026-09-19T20:00:04Z"}`)); err != nil {
		t.Fatal(err)
	}
	second := collect()
	if !strings.Contains(second, "second prompt") {
		t.Fatalf("prompt added after compaction was never emitted:\n%s", second)
	}
	if strings.Contains(second, "session.compacting") {
		t.Fatalf("compaction reported again on the next sweep:\n%s", second)
	}
}
