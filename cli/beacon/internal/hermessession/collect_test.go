package hermessession

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestCollectOnceMapsHermesStateDBAndAdvancesCursor(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")

	summary, err := CollectOnce(CollectOptions{
		DBPath:    dbPath,
		StatePath: statePath,
		LogPath:   logPath,
		Write:     true,
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("CollectOnce returned error: %v", err)
	}
	if summary.Sessions != 1 || summary.SessionsChanged != 1 || summary.EventsEmitted == 0 || summary.Errors != 0 {
		t.Fatalf("summary = %#v, want one changed session with events and no errors", summary)
	}

	events := readEvents(t, logPath)
	actions := eventActions(events)
	want := []string{
		"session.started",
		"prompt.submitted",
		"agent.reasoning",
		"tool.invoked",
		"command.executed",
		"token.usage",
		"session.ended",
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", actions, want)
	}

	command := findAction(t, events, "command.executed")
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command event = %#v, want command.command", command.Command)
	}
	if command.Command.ExitCode == nil || *command.Command.ExitCode != 0 {
		t.Fatalf("exit code = %#v, want 0", command.Command.ExitCode)
	}
	if command.Harness.CollectionMethod != schema.CollectionMethodPoll {
		t.Fatalf("collection method = %q, want poll", command.Harness.CollectionMethod)
	}
	if command.Event.Fidelity != schema.FidelityObserved {
		t.Fatalf("fidelity = %q, want observed", command.Event.Fidelity)
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	cursor := state.Sessions["hermes-session-1"]
	if cursor == nil || cursor.LastMessageID != 3 || !cursor.Started || cursor.EndedAtMS == 0 {
		t.Fatalf("cursor = %#v, want collected through message 3 and ended", cursor)
	}

	second, err := CollectOnce(CollectOptions{
		DBPath:    dbPath,
		StatePath: statePath,
		LogPath:   logPath,
		Write:     true,
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	if second.EventsEmitted != 0 || second.SessionsChanged != 0 {
		t.Fatalf("second summary = %#v, want no new events", second)
	}
	if got := len(readEvents(t, logPath)); got != len(events) {
		t.Fatalf("event count after second run = %d, want %d", got, len(events))
	}
}

func TestMapSessionClassifiesFileTool(t *testing.T) {
	session := Session{ID: "s1", Model: "anthropic/claude-opus-4.6", CWD: "/repo", StartedAtMS: 1000}
	messages := []Message{
		{
			ID:          1,
			SessionID:   "s1",
			Role:        "assistant",
			TimestampMS: 2000,
			ToolCalls:   `[{"id":"call_read","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]`,
		},
		{
			ID:          2,
			SessionID:   "s1",
			Role:        "tool",
			ToolCallID:  "call_read",
			ToolName:    "read_file",
			TimestampMS: 3000,
			Content:     `{"content":"hello"}`,
		},
	}

	mapped := MapSession(session, messages, &Cursor{Started: true})
	fileEvent := findMappedAction(t, mapped, "file.read")
	if fileEvent.File == nil || fileEvent.File.Path != "README.md" || fileEvent.File.Operation != "read" {
		t.Fatalf("file event = %#v, want README.md read", fileEvent.File)
	}
	if fileEvent.Model != "claude-opus-4.6" {
		t.Fatalf("model = %q, want normalized model", fileEvent.Model)
	}
}

func createHermesFixtureDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	schemaSQL := `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    user_id TEXT,
    model TEXT,
    model_config TEXT,
    system_prompt TEXT,
    parent_session_id TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    end_reason TEXT,
    message_count INTEGER DEFAULT 0,
    tool_call_count INTEGER DEFAULT 0,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    cache_write_tokens INTEGER DEFAULT 0,
    reasoning_tokens INTEGER DEFAULT 0,
    cwd TEXT,
    billing_provider TEXT,
    billing_base_url TEXT,
    billing_mode TEXT,
    estimated_cost_usd REAL,
    actual_cost_usd REAL,
    cost_status TEXT,
    cost_source TEXT,
    pricing_version TEXT,
    title TEXT,
    api_call_count INTEGER DEFAULT 0,
    handoff_state TEXT,
    handoff_platform TEXT,
    handoff_error TEXT,
    rewind_count INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_call_id TEXT,
    tool_calls TEXT,
    tool_name TEXT,
    timestamp REAL NOT NULL,
    token_count INTEGER,
    finish_reason TEXT,
    reasoning TEXT,
    reasoning_content TEXT,
    reasoning_details TEXT,
    codex_reasoning_items TEXT,
    codex_message_items TEXT,
    platform_message_id TEXT,
    observed INTEGER DEFAULT 0,
    active INTEGER NOT NULL DEFAULT 1
);`
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO sessions (id, source, model, cwd, started_at, ended_at, end_reason, message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, estimated_cost_usd, title)
VALUES ('hermes-session-1', 'cli', 'anthropic/claude-opus-4.6', '/repo', 1780632830.5, 1780632999.25, 'cli_close', 3, 1, 100, 20, 7, 3, 5, 0.01, 'fixture');
INSERT INTO messages (id, session_id, role, content, timestamp, active)
VALUES (1, 'hermes-session-1', 'user', 'run tests', 1780632831.0, 1);
INSERT INTO messages (id, session_id, role, tool_calls, timestamp, reasoning_content, finish_reason, active)
VALUES (2, 'hermes-session-1', 'assistant', '[{"id":"call_terminal","call_id":"call_terminal","function":{"name":"terminal","arguments":"{\"command\":\"go test ./...\",\"workdir\":\"/repo\"}"}}]', 1780632832.0, 'I should run the tests.', 'tool_calls', 1);
INSERT INTO messages (id, session_id, role, tool_call_id, tool_name, content, timestamp, active)
VALUES (3, 'hermes-session-1', 'tool', 'call_terminal', 'terminal', '{"output":"ok","exit_code":0,"error":null}', 1780632833.0, 1);
`); err != nil {
		t.Fatalf("insert fixture rows: %v", err)
	}
}

func readEvents(t *testing.T, path string) []schema.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var out []schema.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	return out
}

func eventActions(events []schema.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Event.Action)
	}
	return out
}

func findAction(t *testing.T, events []schema.Event, action string) schema.Event {
	t.Helper()
	for _, event := range events {
		if event.Event.Action == action {
			return event
		}
	}
	t.Fatalf("action %q not found in %v", action, eventActions(events))
	return schema.Event{}
}

func findMappedAction(t *testing.T, mapped []MappedEvent, action string) schema.Event {
	t.Helper()
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			return item.Event
		}
	}
	t.Fatalf("action %q not found in mapped events", action)
	return schema.Event{}
}
