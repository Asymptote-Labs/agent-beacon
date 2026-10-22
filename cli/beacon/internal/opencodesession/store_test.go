package opencodesession

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteStoreReadsMessagesPartsAndTokenUsage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE session (
		id TEXT PRIMARY KEY,
		title TEXT,
		directory TEXT,
		time_created INTEGER,
		time_updated INTEGER
	)`)
	mustExec(t, db, `CREATE TABLE message (
		id TEXT PRIMARY KEY,
		session_id TEXT,
		time_created INTEGER,
		data TEXT
	)`)
	mustExec(t, db, `CREATE TABLE part (
		id TEXT PRIMARY KEY,
		message_id TEXT,
		session_id TEXT,
		time_created INTEGER,
		data TEXT
	)`)
	mustExec(t, db, `INSERT INTO session (id, title, directory, time_created, time_updated) VALUES ('ses_1', 'Fix tests', '/repo', 1770000000000, 1770000002000)`)
	insertJSON(t, db, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`, "msg_user", "ses_1", 1770000000100, map[string]interface{}{
		"id": "msg_user", "role": "user", "time": map[string]interface{}{"created": 1770000000100},
	})
	insertJSON(t, db, `INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`, "part_user", "msg_user", "ses_1", 1770000000100, map[string]interface{}{
		"id": "part_user", "type": "text", "text": "please run tests",
	})
	insertJSON(t, db, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`, "msg_assistant", "ses_1", 1770000000200, map[string]interface{}{
		"id": "msg_assistant", "role": "assistant", "modelID": "claude-sonnet-4-5", "time": map[string]interface{}{"created": 1770000000200},
		"tokens": map[string]interface{}{"input": 100, "output": 20, "reasoning": 3, "cache": map[string]interface{}{"read": 7, "write": 5}},
		"cost":   0.01,
	})
	insertJSON(t, db, `INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`, "part_tool", "msg_assistant", "ses_1", 1770000000300, map[string]interface{}{
		"id": "part_tool", "type": "tool", "tool": "bash", "callID": "call_1",
		"state": map[string]interface{}{
			"status": "completed",
			"input":  map[string]interface{}{"command": "go test ./..."},
			"output": map[string]interface{}{"output": "ok"},
			"time":   map[string]interface{}{"start": 1770000000300, "end": 1770000000400},
			"metadata": map[string]interface{}{
				"exit": 0,
			},
		},
	})

	store, err := NewStore(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	if refs[0].ID != "ses_1" || refs[0].Directory != "/repo" || refs[0].Kind != SourceSQLite {
		t.Fatalf("ref = %+v", refs[0])
	}
	records, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want prompt, tool, usage", len(records))
	}
	if records[0].Type != "user_message" || records[1].Type != "tool_result" || records[2].Type != "token_usage" {
		t.Fatalf("record types = %s, %s, %s", records[0].Type, records[1].Type, records[2].Type)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func insertJSON(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	last := len(args) - 1
	data, err := json.Marshal(args[last])
	if err != nil {
		t.Fatal(err)
	}
	args[last] = string(data)
	mustExec(t, db, query, args...)
}
