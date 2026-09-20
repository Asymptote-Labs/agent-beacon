package opencodesession

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestCollectOnceEmitsAToolCompletionAfterTheSessionTimestampStops exercises the shape --watch
// actually sees: OpenCode writes a tool part while the call is running, then rewrites that same row
// when it returns. The part keeps its position, and the session's time_updated does not move, so a
// collector that trusted either one alone would report the call as permanently in flight and never
// write its output, exit code, or completion.
func TestCollectOnceEmitsAToolCompletionAfterTheSessionTimestampStops(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	db := newOpenCodeDB(t, dbPath)
	insertJSON(t, db, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`, "msg_1", "ses_1", int64(1770000000200), map[string]interface{}{
		"id": "msg_1", "role": "assistant", "time": map[string]interface{}{"created": int64(1770000000200)},
	})
	insertJSON(t, db, `INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`, "part_1", "msg_1", "ses_1", int64(1770000000300), map[string]interface{}{
		"id": "part_1", "type": "tool", "tool": "bash", "callID": "call_1",
		"state": map[string]interface{}{"status": "running", "input": map[string]interface{}{"command": "go test ./..."}},
	})

	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	opts := CollectOptions{DataDir: dir, StatePath: statePath, LogPath: logPath, Write: true}

	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 1 {
		t.Fatalf("first sweep emitted %d events, want the invocation", summary.EventsEmitted)
	}
	if actions := loggedActions(t, logPath); len(actions) != 1 || actions[0] != "tool.invoked" {
		t.Fatalf("first sweep wrote %v", actions)
	}
	state := mustLoadState(t, statePath)
	if cursor := state.Traces["sqlite:ses_1"]; cursor == nil || len(cursor.Emitted) != 1 {
		t.Fatalf("cursor did not record what it wrote: %+v", cursor)
	}

	// A second sweep with nothing changed must not re-emit the invocation.
	if summary, err = CollectOnce(opts); err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 0 {
		t.Fatalf("an unchanged sweep emitted %d events", summary.EventsEmitted)
	}

	// The call returns. Only the part row is rewritten: session.time_updated stays where it was.
	updateJSON(t, db, `UPDATE part SET data = ? WHERE id = ?`, map[string]interface{}{
		"id": "part_1", "type": "tool", "tool": "bash", "callID": "call_1",
		"state": map[string]interface{}{
			"status": "completed",
			"input":  map[string]interface{}{"command": "go test ./..."},
			"output": map[string]interface{}{"output": "ok", "metadata": map[string]interface{}{"exit": 0}},
		},
	}, "part_1")

	if summary, err = CollectOnce(opts); err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 1 {
		t.Fatalf("the completion sweep emitted %d events, want 1", summary.EventsEmitted)
	}
	actions := loggedActions(t, logPath)
	if len(actions) != 2 || actions[1] != "command.executed" {
		t.Fatalf("log = %v, want the invocation then the completion", actions)
	}
	// The invocation's id is no longer produced by the trace, so the cursor tracks the completion
	// alone rather than growing a second entry for every call that finishes.
	state = mustLoadState(t, statePath)
	if cursor := state.Traces["sqlite:ses_1"]; cursor == nil || len(cursor.Emitted) != 1 {
		t.Fatalf("cursor = %+v, want only the completion recorded", cursor)
	}

	// With nothing left in flight the settled trace is quiet again.
	if summary, err = CollectOnce(opts); err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 0 {
		t.Fatalf("a settled sweep emitted %d events", summary.EventsEmitted)
	}
}

func newOpenCodeDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, directory TEXT, time_created INTEGER, time_updated INTEGER)`)
	mustExec(t, db, `CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`)
	mustExec(t, db, `CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`)
	mustExec(t, db, `INSERT INTO session (id, title, directory, time_created, time_updated) VALUES ('ses_1', 'Fix tests', '/repo', 1770000000000, 1770000000200)`)
	return db
}

func updateJSON(t *testing.T, db *sql.DB, query string, payload interface{}, args ...interface{}) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, query, append([]interface{}{string(data)}, args...)...)
}

func mustLoadState(t *testing.T, path string) *State {
	t.Helper()
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func loggedActions(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var actions []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var line struct {
			Event struct {
				Action string `json:"action"`
			} `json:"event"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("log line is not an event: %v", err)
		}
		actions = append(actions, line.Event.Action)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return actions
}
