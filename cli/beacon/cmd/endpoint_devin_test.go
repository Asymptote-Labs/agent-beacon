package cmd

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	_ "modernc.org/sqlite"
)

// endpointDevinLiveCoverage scans the retained log for the earliest hook event per Devin
// session: hook events from either platform spelling count, poll events and other harnesses do
// not, and a malformed line or a rotated archive neither stops nor fools the scan.
func TestEndpointDevinLiveCoverage(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	hook := func(session, ts string) string {
		return fmt.Sprintf(`{"timestamp":%q,"harness":{"name":"devin-cli","collection_method":"hook"},"session":{"id":%q},"event":{"action":"prompt.submitted"}}`, ts, session)
	}
	lines := []string{
		`{"timestamp":"2026-10-01T10:00:00Z","harness":{"name":"devin-cli","collection_method":"poll"},"session":{"id":"sess-poll"}}`,
		`{"timestamp":"2026-10-01T10:00:00Z","harness":{"name":"claude_code","collection_method":"hook"},"session":{"id":"sess-claude"}}`,
		hook("sess-1", "2026-10-02T10:00:00Z"),
		hook("sess-1", "2026-10-01T09:00:00Z"), // the earliest wins
		`{malformed devin line`,
		`{"timestamp":"2026-10-01T10:00:00Z","harness":{"name":"devin","collection_method":"hook"},"session":{"id":"sess-legacy"}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Coverage can sit in a rotated archive on a busy machine.
	archive := hook("sess-archived", "2026-09-15T08:00:00Z")
	if err := os.WriteFile(logPath+".1", []byte(archive+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	coverage := endpointDevinLiveCoverage(logPath)
	if len(coverage) != 3 {
		t.Fatalf("coverage = %v, want three hook-covered sessions", coverage)
	}
	if got := coverage["sess-1"]; !got.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("sess-1 coverage = %s, want the earliest hook event", got)
	}
	if _, ok := coverage["sess-legacy"]; !ok {
		t.Fatal("the legacy devin platform spelling was not counted as live coverage")
	}
	if got := coverage["sess-archived"]; !got.Equal(time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("sess-archived coverage = %s", got)
	}
	for _, absent := range []string{"sess-poll", "sess-claude"} {
		if _, ok := coverage[absent]; ok {
			t.Fatalf("%s counted as hook coverage though it is not a devin hook event", absent)
		}
	}
}

func TestEndpointDevinLiveCoverageWithoutALog(t *testing.T) {
	if got := endpointDevinLiveCoverage(filepath.Join(t.TempDir(), "runtime.jsonl")); got != nil {
		t.Fatalf("coverage = %v, want nil for a missing log", got)
	}
}

func writeDevinFixtureDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE sessions (id TEXT PRIMARY KEY, working_directory TEXT NOT NULL, backend_type TEXT NOT NULL, model TEXT NOT NULL, agent_mode TEXT NOT NULL, created_at INTEGER NOT NULL, last_activity_at INTEGER NOT NULL, title TEXT, main_chain_id INTEGER, shell_last_seen_index INTEGER DEFAULT 0, cogs_json TEXT, workspace_dirs TEXT, hidden INTEGER NOT NULL DEFAULT 0, metadata TEXT);
CREATE TABLE message_nodes (row_id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, node_id INTEGER NOT NULL, parent_node_id INTEGER, chat_message TEXT NOT NULL, created_at INTEGER NOT NULL, metadata TEXT, FOREIGN KEY (session_id) REFERENCES sessions(id), UNIQUE(session_id, node_id));
INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode, created_at, last_activity_at)
VALUES ('s1', '/repo', 'local', 'swe-1-7', 'standard', 1700000000, 1700000000);
INSERT INTO message_nodes (session_id, node_id, chat_message, created_at)
VALUES ('s1', 1, '{"role":"user","message_id":"u1","content":"hello","metadata":{"created_at":"2023-11-14T22:13:20.000000Z","is_user_input":true}}', 1700000100);`)
	if err != nil {
		t.Fatalf("fixture db: %v", err)
	}
}

func devinCmdFixture(t *testing.T) (dbPath, statePath, logPath string) {
	t.Helper()
	root := t.TempDir()
	dbPath = filepath.Join(root, "sessions.db")
	statePath = filepath.Join(root, "state", "devin.json")
	logPath = filepath.Join(root, "logs", "runtime.jsonl")
	writeDevinFixtureDB(t, dbPath)

	// Command flags are package-level, so each test sets every one it depends on.
	endpointDevinOpts.dbPath = dbPath
	endpointDevinOpts.statePath = statePath
	endpointDevinOpts.logPath = logPath
	endpointDevinOpts.print = false
	endpointDevinOpts.watch = false
	endpointDevinOpts.rotateBytes = 0
	endpointOpts.jsonOutput = false
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointDevinOpts.dbPath = ""
		endpointDevinOpts.statePath = ""
		endpointDevinOpts.logPath = ""
		endpointDevinOpts.rotateBytes = 0
		endpointOpts.jsonOutput = false
	})
	return dbPath, statePath, logPath
}

func runDevinSync(t *testing.T) (string, error) {
	t.Helper()
	var out bytes.Buffer
	endpointDevinSyncCmd.SetOut(&out)
	endpointDevinSyncCmd.SetErr(&out)
	t.Cleanup(func() {
		endpointDevinSyncCmd.SetOut(nil)
		endpointDevinSyncCmd.SetErr(nil)
	})
	err := runEndpointDevinSync(endpointDevinSyncCmd, nil)
	return out.String(), err
}

// --print is the dry run: it prints the mapped events and must write neither the runtime log nor
// the cursor, so running it twice shows the same output.
func TestEndpointDevinSyncPrintWritesNothing(t *testing.T) {
	_, statePath, logPath := devinCmdFixture(t)
	endpointDevinOpts.print = true

	out, err := runDevinSync(t)
	if err != nil {
		t.Fatalf("sync --print: %v", err)
	}
	if !strings.Contains(out, `"prompt.submitted"`) {
		t.Fatalf("print output has no prompt event:\n%s", out)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("--print wrote the runtime log")
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("--print wrote a cursor file")
	}
}

func TestEndpointDevinSyncWritesAndReports(t *testing.T) {
	_, _, logPath := devinCmdFixture(t)

	out, err := runDevinSync(t)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Devin CLI sync: 1 sessions, 1 changed") {
		t.Fatalf("report = %q", out)
	}
	info, err := os.Stat(logPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("runtime log not written: %v", err)
	}
}

func TestEndpointDevinSyncRejectsARotateBytesBelowTheDefault(t *testing.T) {
	_, statePath, logPath := devinCmdFixture(t)
	endpointDevinOpts.rotateBytes = 1024

	_, err := runDevinSync(t)
	if err == nil || !strings.Contains(err.Error(), "--rotate-bytes") {
		t.Fatalf("err = %v, want a --rotate-bytes error", err)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Error("a refused --rotate-bytes still wrote the runtime log")
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Error("a refused --rotate-bytes still advanced the cursor")
	}
	endpointDevinOpts.rotateBytes = writer.DefaultRotateBytes
	if _, err := runDevinSync(t); err != nil {
		t.Fatalf("sync at the default rotate size: %v", err)
	}
}
