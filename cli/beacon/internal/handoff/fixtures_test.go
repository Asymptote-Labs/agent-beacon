package handoff

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Fixture timestamps, oldest first, so ordering assertions read in one direction.
var (
	clineUpdated    = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	codexUpdated    = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	openCodeUpdated = time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	claudeUpdated   = time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
)

// storeFixture lays down one session store per supported runtime, in the formats each runtime
// writes, under a temporary directory.
type storeFixture struct {
	root string
	dirs StoreDirs
}

func newStoreFixture(t *testing.T) storeFixture {
	t.Helper()
	root := t.TempDir()
	f := storeFixture{
		root: root,
		dirs: StoreDirs{
			ClaudeProjects: filepath.Join(root, ".claude", "projects"),
			Codex:          filepath.Join(root, ".codex"),
			OpenCode:       filepath.Join(root, "opencode"),
			Cline:          filepath.Join(root, ".cline"),
		},
	}
	f.writeClaude(t)
	f.writeCodex(t)
	f.writeOpenCode(t)
	f.writeCline(t)
	return f
}

func (f storeFixture) writeClaude(t *testing.T) {
	t.Helper()
	project := filepath.Join(f.dirs.ClaudeProjects, "-work-api")
	main := filepath.Join(project, "claude-sess-1.jsonl")
	writeFixture(t, main, jsonLine(t, map[string]interface{}{
		"type": "user", "uuid": "u1", "sessionId": "claude-sess-1", "cwd": "/work/api", "gitBranch": "feat/health",
		"timestamp": "2026-09-23T08:59:00.000Z",
		"message":   map[string]interface{}{"role": "user", "content": "add a health endpoint"},
	}))
	writeFixture(t, filepath.Join(project, "sessions-index.json"), mustJSON(t, map[string]interface{}{
		"version": 1,
		"entries": []interface{}{map[string]interface{}{
			"sessionId": "claude-sess-1", "fullPath": main, "projectPath": "/work/api",
			"gitBranch": "feat/health", "summary": "Add a\nhealth endpoint", "firstPrompt": "add a health endpoint",
		}},
	}))
	setModTime(t, main, claudeUpdated)

	sub := filepath.Join(project, "claude-sess-1", "subagents", "agent-a.jsonl")
	writeFixture(t, sub, jsonLine(t, map[string]interface{}{
		"type": "user", "uuid": "s1", "sessionId": "claude-sess-1", "isSidechain": true,
		"message": map[string]interface{}{"role": "user", "content": "inspect the router"},
	}))
	setModTime(t, sub, claudeUpdated.Add(time.Minute))
}

func (f storeFixture) writeCodex(t *testing.T) {
	t.Helper()
	// A thread with two rollout files, both carrying the thread id.
	older := filepath.Join(f.dirs.Codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T09-00-00-codex-thread-1.jsonl")
	newer := filepath.Join(f.dirs.Codex, "sessions", "2026", "09", "21", "rollout-2026-09-21T09-00-00-codex-thread-1.jsonl")
	for _, path := range []string{older, newer} {
		writeFixture(t, path, jsonLine(t, map[string]interface{}{
			"timestamp": "2026-09-21T09:00:00.000Z", "type": "session_meta",
			"payload": map[string]interface{}{"id": "codex-thread-1", "session_id": "codex-thread-1", "cwd": "/work/web"},
		}))
	}
	setModTime(t, older, codexUpdated.Add(-24*time.Hour))
	setModTime(t, newer, codexUpdated)
	writeFixture(t, filepath.Join(f.dirs.Codex, "session_index.jsonl"), jsonLine(t, map[string]interface{}{
		"id": "codex-thread-1", "thread_name": "Fix the login form",
	}))
}

func (f storeFixture) writeOpenCode(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(f.dirs.OpenCode, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dirs.OpenCode, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, directory TEXT, parent_id TEXT, time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	updated := openCodeUpdated.UnixMilli()
	if _, err := db.Exec(`INSERT INTO session VALUES ('ses_parent', 'Refactor the cache', '/work/cache', NULL, ?, ?)`, updated-1000, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session VALUES ('ses_child', 'Explore cache callers', '/work/cache', 'ses_parent', ?, ?)`, updated-500, updated-100); err != nil {
		t.Fatal(err)
	}
}

func (f storeFixture) writeCline(t *testing.T) {
	t.Helper()
	sessionDir := filepath.Join(f.dirs.Cline, "data", "sessions", "cline-task-1")
	writeFixture(t, filepath.Join(sessionDir, "cline-task-1.json"), mustJSON(t, map[string]interface{}{
		"task": "Write the release notes", "workspacePath": "/work/docs",
	}))
	messages := filepath.Join(sessionDir, "cline-task-1.messages.json")
	writeFixture(t, messages, mustJSON(t, map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "Write the release notes", "ts": clineUpdated.UnixMilli()},
	}}))
	setModTime(t, messages, clineUpdated)

	// The same task id in the older task history must not list twice.
	history := filepath.Join(f.dirs.Cline, "data", "tasks", "cline-task-1", "api_conversation_history.json")
	writeFixture(t, history, mustJSON(t, []interface{}{
		map[string]interface{}{"role": "user", "content": "Write the release notes", "ts": clineUpdated.Add(time.Hour).UnixMilli()},
	}))
	setModTime(t, history, clineUpdated.Add(time.Hour))
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setModTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func jsonLine(t *testing.T, value interface{}) string {
	return mustJSON(t, value) + "\n"
}
