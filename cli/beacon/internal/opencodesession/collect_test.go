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

// OpenCode 2.x lists sessions in session_v2 and keeps their history in session_message, migrating
// 1.x sessions into both under the same ids and timestamps. The migrated copy must not be written
// twice, and what 2.x added afterwards must be.
func TestCollectOnceReadsOpenCode2Sessions(t *testing.T) {
	dir := t.TempDir()
	db := newOpenCodeDB(t, filepath.Join(dir, "opencode.db"))
	insertJSON(t, db, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`, "msg_1", "ses_1", int64(1770000000100), map[string]interface{}{"id": "msg_1", "role": "user"})
	insertJSON(t, db, `INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`, "prt_1", "msg_1", "ses_1", int64(1770000000100), map[string]interface{}{"id": "prt_1", "type": "text", "text": "old prompt"})
	mustExec(t, db, `CREATE TABLE session_v2 (id TEXT PRIMARY KEY, directory TEXT NOT NULL, title TEXT, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, time_archived INTEGER)`)
	mustExec(t, db, `CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`)
	mustExec(t, db, `INSERT INTO session_v2 (id, directory, time_created, time_updated) VALUES ('ses_1', '/repo', 1770000000000, 1770000000900), ('ses_2', '/repo2', 1770000001000, 1770000001500)`)
	insert := `INSERT INTO session_message (id, session_id, type, seq, time_created, data) VALUES (?, ?, ?, ?, ?, ?)`
	insertJSON(t, db, insert, "msg_1", "ses_1", "user", 1, int64(1770000000100), map[string]interface{}{"text": "old prompt"})
	insertJSON(t, db, insert, "msg_2", "ses_1", "user", 2, int64(1770000000800), map[string]interface{}{"text": "resumed in 2.x"})
	insertJSON(t, db, insert, "msg_3", "ses_2", "user", 1, int64(1770000001100), map[string]interface{}{"text": "run tests"})
	insertJSON(t, db, insert, "msg_4", "ses_2", "assistant", 2, int64(1770000001200), map[string]interface{}{
		"model": map[string]interface{}{"id": "claude-sonnet-4-5", "providerID": "anthropic"},
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": "Running them."},
			map[string]interface{}{"type": "tool", "id": "call_1", "name": "bash",
				"state": map[string]interface{}{"status": "completed", "input": map[string]interface{}{"command": "go test ./..."},
					"content": []interface{}{map[string]interface{}{"type": "text", "text": "ok"}}, "metadata": map[string]interface{}{"exit": 0}},
				"time": map[string]interface{}{"created": 1770000001300, "completed": 1770000001400}},
		},
		"tokens": map[string]interface{}{"input": 100, "output": 20, "reasoning": 5, "cache": map[string]interface{}{"read": 0, "write": 0}},
		"cost":   0.01,
	})
	// A completed compaction carries the usage of the summarizing model call.
	insertJSON(t, db, insert, "msg_5", "ses_2", "compaction", 3, int64(1770000001450), map[string]interface{}{
		"status": "completed", "model": map[string]interface{}{"id": "claude-sonnet-4-5", "providerID": "anthropic"},
		"tokens": map[string]interface{}{"input": 400, "output": 50, "reasoning": 0, "cache": map[string]interface{}{"read": 0, "write": 0}},
		"cost":   0.02,
	})

	logPath := filepath.Join(dir, "runtime.jsonl")
	if _, err := CollectOnce(CollectOptions{DataDir: dir, StatePath: filepath.Join(dir, "state.json"), LogPath: logPath, Write: true}); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, action := range loggedActions(t, logPath) {
		got[action]++
	}
	want := map[string]int{"prompt.submitted": 3, "agent.message": 1, "command.executed": 1, "token.usage": 2}
	for action, n := range want {
		if got[action] != n {
			t.Fatalf("logged %v, want %v", got, want)
		}
	}
}
