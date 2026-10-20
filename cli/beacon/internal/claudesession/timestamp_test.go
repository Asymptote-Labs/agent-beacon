package claudesession

import (
	"testing"
	"time"
)

// Claude Code writes bookkeeping records with a sessionId and no timestamp. When one of them is the
// first record of a session, session.started is emitted for it, and it must take the session's own
// time rather than the time of the sync.
func TestSessionStartedWithoutATimestampTakesTheSessionsEarliest(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	records := decodeFixture(t, []string{
		`{"type":"permission-mode","permissionMode":"default","sessionId":"sess-1"}`,
		userLineAt("sess-1", "u1", "add a health endpoint", "2026-10-08T10:00:00Z"),
		userLineAt("sess-1", "u2", "now document it", "2026-10-08T10:05:00Z"),
	})

	mapped := MapSession(ref, records, MapOptions{})
	started := findAction(t, mapped, "session.started")
	if want := "2026-10-08T10:00:00.000000000Z"; started.Timestamp != want {
		t.Fatalf("session.started timestamp = %q, want %q", started.Timestamp, want)
	}
	if mapped[0].SourceLine != 1 {
		t.Fatalf("session.started source line = %d, want the permission-mode record", mapped[0].SourceLine)
	}

	// The event id stays tied to the record's place in the file, not to the time it was given.
	again := MapSession(ref, records, MapOptions{})
	if again[0].DedupID != mapped[0].DedupID {
		t.Fatalf("session.started id changed between syncs: %q then %q", mapped[0].DedupID, again[0].DedupID)
	}
}

// A sweep past a cursor that has not yet recorded a start emits session.started for the first
// record past the cursor, and takes the earliest time past the cursor.
func TestSessionStartedPastTheCursorTakesTheEarliestTimePastIt(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	records := decodeFixture(t, []string{
		`{"type":"file-history-snapshot","snapshot":{}}`,
		userLineAt("sess-1", "u0", "an earlier prompt", "2026-10-08T09:00:00Z"),
		`{"type":"mode","mode":"normal","sessionId":"sess-1"}`,
		userLineAt("sess-1", "u1", "add a health endpoint", "2026-10-08T10:00:00Z"),
	})

	mapped := MapSession(ref, records, MapOptions{MinLine: 2, SkipSessionStarted: false})
	started := findAction(t, mapped, "session.started")
	if want := "2026-10-08T10:00:00.000000000Z"; started.Timestamp != want {
		t.Fatalf("session.started timestamp = %q, want %q", started.Timestamp, want)
	}
}

// A summary record carries no timestamp, so it is given the session's own time too.
func TestSummaryWithoutATimestampTakesTheSessionsTime(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	records := decodeFixture(t, []string{
		`{"type":"summary","summary":"Health endpoint added","leafUuid":"u1","message":{"summary":"Health endpoint added"}}`,
		userLineAt("sess-1", "u1", "add a health endpoint", "2026-10-08T10:00:00Z"),
	})

	mapped := MapSession(ref, records, MapOptions{SkipSessionStarted: true})
	summary := findAction(t, mapped, "session.summary")
	if want := "2026-10-08T10:00:00.000000000Z"; summary.Timestamp != want {
		t.Fatalf("session.summary timestamp = %q, want %q", summary.Timestamp, want)
	}
}

// A file whose records carry no timestamp at all is given its modification time.
func TestRecordsWithoutAnyTimestampTakeTheFilesModificationTime(t *testing.T) {
	modified := time.Date(2026, 10, 7, 18, 30, 0, 0, time.UTC)
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo", ModTimeUnixMS: modified.UnixMilli()}
	records := decodeFixture(t, []string{
		`{"type":"ai-title","title":"Health endpoint","sessionId":"sess-1"}`,
	})

	started := findAction(t, MapSession(ref, records, MapOptions{}), "session.started")
	if want := "2026-10-07T18:30:00.000000000Z"; started.Timestamp != want {
		t.Fatalf("session.started timestamp = %q, want %q", started.Timestamp, want)
	}
}

func userLineAt(sessionID, uuid, text, timestamp string) string {
	return `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":` + quote(text) + `},"uuid":` + quote(uuid) + `,"timestamp":` + quote(timestamp) + `,"cwd":"/tmp/repo","sessionId":` + quote(sessionID) + `,"version":"2.1.154","gitBranch":"main"}`
}
