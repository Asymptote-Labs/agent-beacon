package opencodesession

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestFileURLKeepsAWindowsDriveLetterOutOfTheAuthority(t *testing.T) {
	// The inputs are already slash-separated, which is what filepath.ToSlash hands fileURL, so the
	// Windows shapes are exercised on every platform rather than only on the Windows runner.
	for _, tc := range []struct {
		name, slashed, want string
	}{
		{"windows drive", "C:/Users/me/AppData/Roaming/opencode/opencode.db", "file:///C:/Users/me/AppData/Roaming/opencode/opencode.db"},
		{"posix", "/home/me/.config/opencode/opencode.db", "file:///home/me/.config/opencode/opencode.db"},
		{"unc share", "//server/share/opencode/opencode.db", "file://server/share/opencode/opencode.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := fileURL(tc.slashed)
			if got := u.String(); got != tc.want {
				t.Fatalf("fileURL(%q) = %q, want %q", tc.slashed, got, tc.want)
			}
		})
	}
}

func TestSQLiteFileURIAsksForReadOnly(t *testing.T) {
	uri := sqliteFileURI(filepath.Join(t.TempDir(), "opencode.db"))
	if !strings.HasPrefix(uri, "file:///") {
		// Without the file: prefix modernc.org/sqlite drops the query, and the driver's own flags
		// would open the store READWRITE|CREATE.
		t.Fatalf("uri = %q, want a file:// URI", uri)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "" {
		t.Fatalf("uri host = %q, want none", parsed.Host)
	}
	if mode := parsed.Query().Get("mode"); mode != "ro" {
		t.Fatalf("mode = %q, want ro", mode)
	}
}

func TestOpenSQLiteReadOnlyDoesNotCreateTheDatabase(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "opencode.db")
	db, err := openSQLiteReadOnly(missing)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err == nil {
		t.Fatal("ping on a missing database succeeded; mode=ro is not reaching sqlite")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("read-only open created %s", missing)
	}
}
