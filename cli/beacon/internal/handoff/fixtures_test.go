package handoff

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// isolateRuntimeEnv clears the environment variables that move a runtime's default store, so a
// runtime the fixture leaves at its default reads only the test's HOME.
func isolateRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PI_CODING_AGENT_DIR", "PRIME_AGENT_CODING_AGENT_DIR", "DSH_HOME", "OPENCLAW_STATE_DIR", "XDG_CONFIG_HOME", "APPDATA", "CURSOR_CONFIG_DIR"} {
		t.Setenv(name, "")
	}
}

// storeFixture lays down one session store per supported runtime, in the formats each runtime
// writes, under a temporary directory.
type storeFixture struct {
	root string
	dirs StoreDirs
}

func newStoreFixture(t *testing.T) storeFixture {
	t.Helper()
	// The fixture leaves the runtimes it does not write at their defaults; keep those under HOME.
	isolateRuntimeEnv(t)
	root := t.TempDir()
	f := storeFixture{
		root: root,
		dirs: StoreDirs{
			HarnessClaude:   filepath.Join(root, ".claude", "projects"),
			HarnessCodex:    filepath.Join(root, ".codex"),
			HarnessOpenCode: filepath.Join(root, "opencode"),
			HarnessCline:    filepath.Join(root, ".cline"),
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
	project := filepath.Join(f.dirs[HarnessClaude], "-work-api")
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
	older := filepath.Join(f.dirs[HarnessCodex], "sessions", "2026", "09", "20", "rollout-2026-09-20T09-00-00-codex-thread-1.jsonl")
	newer := filepath.Join(f.dirs[HarnessCodex], "sessions", "2026", "09", "21", "rollout-2026-09-21T09-00-00-codex-thread-1.jsonl")
	for _, path := range []string{older, newer} {
		writeFixture(t, path, jsonLine(t, map[string]interface{}{
			"timestamp": "2026-09-21T09:00:00.000Z", "type": "session_meta",
			"payload": map[string]interface{}{"id": "codex-thread-1", "session_id": "codex-thread-1", "cwd": "/work/web"},
		}))
	}
	setModTime(t, older, codexUpdated.Add(-24*time.Hour))
	setModTime(t, newer, codexUpdated)
	writeFixture(t, filepath.Join(f.dirs[HarnessCodex], "session_index.jsonl"), jsonLine(t, map[string]interface{}{
		"id": "codex-thread-1", "thread_name": "Fix the login form",
	}))
}

func (f storeFixture) writeOpenCode(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(f.dirs[HarnessOpenCode], 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dirs[HarnessOpenCode], "opencode.db"))
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
	sessionDir := filepath.Join(f.dirs[HarnessCline], "data", "sessions", "cline-task-1")
	writeFixture(t, filepath.Join(sessionDir, "cline-task-1.json"), mustJSON(t, map[string]interface{}{
		"task": "Write the release notes", "workspacePath": "/work/docs",
	}))
	messages := filepath.Join(sessionDir, "cline-task-1.messages.json")
	writeFixture(t, messages, mustJSON(t, map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "Write the release notes", "ts": clineUpdated.UnixMilli()},
	}}))
	setModTime(t, messages, clineUpdated)

	// The same task id in the older task history must not list twice.
	history := filepath.Join(f.dirs[HarnessCline], "data", "tasks", "cline-task-1", "api_conversation_history.json")
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

// writeTranscripts gives each fixture session a conversation: one request and one reply, plus a
// second turn in the thread's newer Codex rollout file.
func (f storeFixture) writeTranscripts(t *testing.T) {
	t.Helper()
	claudeMain := filepath.Join(f.dirs[HarnessClaude], "-work-api", "claude-sess-1.jsonl")
	writeFixture(t, claudeMain, jsonLine(t, map[string]interface{}{
		"type": "user", "uuid": "u1", "sessionId": "claude-sess-1", "cwd": "/work/api", "timestamp": "2026-09-23T08:59:00.000Z",
		"message": map[string]interface{}{"role": "user", "content": "add a health endpoint"},
	})+jsonLine(t, map[string]interface{}{
		"type": "assistant", "uuid": "a1", "parentUuid": "u1", "sessionId": "claude-sess-1", "cwd": "/work/api", "timestamp": "2026-09-23T08:59:30.000Z",
		"message": map[string]interface{}{"id": "msg_1", "role": "assistant", "model": "claude-sonnet-4-5", "content": []interface{}{
			map[string]interface{}{"type": "text", "text": "Added GET /healthz."},
		}},
	}))
	setModTime(t, claudeMain, claudeUpdated)

	codexTurn := func(prompt, reply string) string {
		return jsonLine(t, map[string]interface{}{
			"timestamp": "2026-09-21T09:00:00.000Z", "type": "session_meta",
			"payload": map[string]interface{}{"id": "codex-thread-1", "session_id": "codex-thread-1", "cwd": "/work/web"},
		}) + jsonLine(t, map[string]interface{}{
			"timestamp": "2026-09-21T09:00:01.000Z", "type": "response_item",
			"payload": map[string]interface{}{"type": "message", "role": "user", "content": []interface{}{map[string]interface{}{"type": "input_text", "text": prompt}}},
		}) + jsonLine(t, map[string]interface{}{
			"timestamp": "2026-09-21T09:00:02.000Z", "type": "response_item",
			"payload": map[string]interface{}{"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": reply}}},
		})
	}
	older := filepath.Join(f.dirs[HarnessCodex], "sessions", "2026", "09", "20", "rollout-2026-09-20T09-00-00-codex-thread-1.jsonl")
	newer := filepath.Join(f.dirs[HarnessCodex], "sessions", "2026", "09", "21", "rollout-2026-09-21T09-00-00-codex-thread-1.jsonl")
	writeFixture(t, older, codexTurn("fix the login form", "Working on it."))
	writeFixture(t, newer, codexTurn("also check the signup form", "The form validates now."))
	setModTime(t, older, codexUpdated.Add(-24*time.Hour))
	setModTime(t, newer, codexUpdated)

	db, err := sql.Open("sqlite", filepath.Join(f.dirs[HarnessOpenCode], "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := openCodeUpdated.UnixMilli() - 900
	for i, turn := range []struct{ role, text string }{{"user", "refactor the cache"}, {"assistant", "Cache refactored."}} {
		msgID := fmt.Sprintf("msg_%d", i)
		created := base + int64(i*100)
		if _, err := db.Exec(`INSERT INTO message VALUES (?, 'ses_parent', ?, ?)`, msgID, created, mustJSON(t, map[string]interface{}{
			"id": msgID, "role": turn.role, "time": map[string]interface{}{"created": created},
		})); err != nil {
			t.Fatal(err)
		}
		partID := fmt.Sprintf("part_%d", i)
		if _, err := db.Exec(`INSERT INTO part VALUES (?, ?, 'ses_parent', ?, ?)`, partID, msgID, created+10, mustJSON(t, map[string]interface{}{
			"id": partID, "type": "text", "text": turn.text,
		})); err != nil {
			t.Fatal(err)
		}
	}

	messages := filepath.Join(f.dirs[HarnessCline], "data", "sessions", "cline-task-1", "cline-task-1.messages.json")
	writeFixture(t, messages, mustJSON(t, map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "Write the release notes", "ts": clineUpdated.Add(-time.Minute).UnixMilli()},
		map[string]interface{}{"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Release notes drafted."}}, "ts": clineUpdated.UnixMilli()},
	}}))
	setModTime(t, messages, clineUpdated)
}
