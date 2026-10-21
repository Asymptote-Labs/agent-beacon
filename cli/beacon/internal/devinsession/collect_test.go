package devinsession

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// The store's real DDL, verbatim: a fixture must break if this collector drifts from the schema
// Devin CLI actually writes.
const devinFixtureDDL = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, working_directory TEXT NOT NULL, backend_type TEXT NOT NULL, model TEXT NOT NULL, agent_mode TEXT NOT NULL, created_at INTEGER NOT NULL, last_activity_at INTEGER NOT NULL, title TEXT, main_chain_id INTEGER, shell_last_seen_index INTEGER DEFAULT 0, cogs_json TEXT, workspace_dirs TEXT, hidden INTEGER NOT NULL DEFAULT 0, metadata TEXT);
CREATE TABLE message_nodes (row_id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, node_id INTEGER NOT NULL, parent_node_id INTEGER, chat_message TEXT NOT NULL, created_at INTEGER NOT NULL, metadata TEXT, FOREIGN KEY (session_id) REFERENCES sessions(id), UNIQUE(session_id, node_id));
CREATE INDEX idx_message_nodes_session ON message_nodes(session_id);
`

func openFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(devinFixtureDDL); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	return db
}

func addSession(t *testing.T, db *sql.DB, id string, fields map[string]interface{}) {
	t.Helper()
	session := map[string]interface{}{
		"working_directory": "/repo",
		"backend_type":      "local",
		"model":             "swe-1-7",
		"agent_mode":        "standard",
		"created_at":        1700000000,
		"last_activity_at":  1700000000,
		"title":             "fixture session",
		"metadata":          `{"total_acu_cost":1.5,"total_credit_cost":3}`,
	}
	for key, value := range fields {
		session[key] = value
	}
	_, err := db.Exec(`INSERT INTO sessions
(id, working_directory, backend_type, model, agent_mode, created_at, last_activity_at, title, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		session["working_directory"], session["backend_type"], session["model"], session["agent_mode"],
		session["created_at"], session["last_activity_at"], session["title"], session["metadata"])
	if err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

func addNode(t *testing.T, db *sql.DB, sessionID string, nodeID int64, chatMessage string, createdAtSec int64, nodeMetadata interface{}) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO message_nodes (session_id, node_id, chat_message, created_at, metadata)
VALUES (?, ?, ?, ?, ?)`, sessionID, nodeID, chatMessage, createdAtSec, nodeMetadata)
	if err != nil {
		t.Fatalf("insert node %s/%d: %v", sessionID, nodeID, err)
	}
}

func chatJSON(fields map[string]interface{}) string {
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func userMsg(messageID, text, iso string) string {
	return chatJSON(map[string]interface{}{
		"role":       "user",
		"message_id": messageID,
		"content":    text,
		"metadata":   map[string]interface{}{"created_at": iso, "is_user_input": true},
	})
}

type assistantFixture struct {
	Content    string
	Thinking   string
	Model      string
	Finish     string
	RequestID  string
	Metrics    map[string]interface{}
	ToolCalls  []map[string]interface{}
	Extensions map[string]interface{}
	CreatedAt  string
}

func assistantMsg(messageID string, f assistantFixture) string {
	meta := map[string]interface{}{}
	if f.CreatedAt != "" {
		meta["created_at"] = f.CreatedAt
	}
	if f.Model != "" {
		meta["generation_model"] = f.Model
	}
	if f.Metrics != nil {
		meta["metrics"] = f.Metrics
	}
	if f.Finish != "" {
		meta["finish_reason"] = f.Finish
	}
	if f.RequestID != "" {
		meta["request_id"] = f.RequestID
	}
	if f.Extensions != nil {
		meta["extensions"] = f.Extensions
	}
	msg := map[string]interface{}{
		"role":       "assistant",
		"message_id": messageID,
		"content":    f.Content,
		"metadata":   meta,
	}
	if f.Thinking != "" {
		msg["thinking"] = map[string]interface{}{"thinking": f.Thinking, "signature": "sig"}
	}
	if len(f.ToolCalls) > 0 {
		msg["tool_calls"] = f.ToolCalls
	}
	return chatJSON(msg)
}

func toolCallFixture(id, name string, args map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"id": id, "name": name, "arguments": args, "index": 0, "kind": "function"}
}

func toolMsg(messageID, callID, content, iso string, ext map[string]interface{}) string {
	return chatJSON(map[string]interface{}{
		"role":         "tool",
		"message_id":   messageID,
		"tool_call_id": callID,
		"content":      content,
		"metadata":     map[string]interface{}{"created_at": iso, "extensions": ext},
	})
}

func summaryMsg(messageID, text, iso string) string {
	return chatJSON(map[string]interface{}{
		"role":       "system",
		"message_id": messageID,
		"content":    text,
		"metadata": map[string]interface{}{
			"created_at": iso,
			"extensions": map[string]interface{}{"devin-rs/summary": map[string]interface{}{"source": "async_file_compactor"}},
		},
	})
}

type fixturePaths struct {
	db    string
	state string
	log   string
}

func newFixturePaths(t *testing.T) fixturePaths {
	t.Helper()
	dir := t.TempDir()
	return fixturePaths{
		db:    filepath.Join(dir, "sessions.db"),
		state: filepath.Join(dir, "devin-state.json"),
		log:   filepath.Join(dir, "runtime.jsonl"),
	}
}

func collectOptions(p fixturePaths) CollectOptions {
	return CollectOptions{DBPath: p.db, StatePath: p.state, LogPath: p.log, Write: true, UserMode: true}
}

func readEvents(t *testing.T, path string) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var events []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev schema.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func eventActions(events []schema.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Event.Action)
	}
	return out
}

func findAction(t *testing.T, events []schema.Event, action string) schema.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Event.Action == action {
			return ev
		}
	}
	t.Fatalf("no %s event in %v", action, eventActions(events))
	return schema.Event{}
}

func devinRaw(ev schema.Event) map[string]interface{} {
	raw, _ := ev.Raw["devin"].(map[string]interface{})
	return raw
}

// The canonical fixture: one session with an operator prompt, an assistant turn that thinks,
// replies, runs a command and reports metrics, and a compaction marker row.
func seedBasicSession(t *testing.T, db *sql.DB, sessionID string) {
	t.Helper()
	addSession(t, db, sessionID, nil)
	addNode(t, db, sessionID, 1, userMsg("u1", "fix the flaky test", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)
	addNode(t, db, sessionID, 2, assistantMsg("a1", assistantFixture{
		Content:   "I will run the test",
		Thinking:  "let me look at the test first",
		Model:     "swe-1-7",
		Finish:    "stop",
		RequestID: "req-1",
		Metrics:   map[string]interface{}{"input_tokens": 100, "output_tokens": 20, "cache_read_tokens": 5, "cache_creation_tokens": 3},
		ToolCalls: []map[string]interface{}{toolCallFixture("call-1", "exec", map[string]interface{}{"command": "go test ./...", "workdir": "/repo"})},
		CreatedAt: "2023-11-14T22:13:40.123456Z",
	}), 1700000200, nil)
	addNode(t, db, sessionID, 3, toolMsg("t1", "call-1", "ok  ./pkg\nPASS", "2023-11-14T22:13:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"cwd": "/repo", "exit": map[string]interface{}{"exit_code": 0, "terminal_id": "term-1"}},
	}), 1700000300, nil)
	addNode(t, db, sessionID, 4, summaryMsg("s1", "<summary>the test was fixed</summary>", "2023-11-14T22:14:00.000000Z"), 1700000400,
		`{"summarized_from":2,"extensions":{"compact/prior_node_ids":[1,2,3]}}`)
}

func TestCollectOnceMapsDevinStoreAndAdvancesCursor(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	seedBasicSession(t, db, "quiet-meadow")

	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.Sessions != 1 || summary.SessionsChanged != 1 || summary.Errors != 0 {
		t.Fatalf("summary = %#v", summary)
	}

	events := readEvents(t, p.log)
	want := []string{
		"session.started",
		"prompt.submitted",
		"agent.reasoning",
		"agent.message",
		"tool.invoked",
		"token.usage",
		"command.executed",
		"session.compacted",
	}
	if strings.Join(eventActions(events), ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", eventActions(events), want)
	}

	started := findAction(t, events, "session.started")
	if got := devinRaw(started)["hidden"]; got != false {
		t.Fatalf("session.started raw hidden = %v", got)
	}
	if got := devinRaw(started)["total_acu_cost"]; got != 1.5 {
		t.Fatalf("session.started raw total_acu_cost = %v, want the credit value raw, not cost_usd", got)
	}

	prompt := findAction(t, events, "prompt.submitted")
	if prompt.Prompt == nil || prompt.Prompt.Text != "fix the flaky test" {
		t.Fatalf("prompt = %#v", prompt.Prompt)
	}

	msg := findAction(t, events, "agent.message")
	if msg.Model != "swe-1-7" {
		t.Fatalf("agent.message model = %q, want the row's generation_model", msg.Model)
	}
	if msg.GenAI == nil || msg.GenAI.Request == nil || msg.GenAI.Request.Model != "swe-1-7" {
		t.Fatalf("agent.message gen_ai.request = %#v", msg.GenAI)
	}

	invoked := findAction(t, events, "tool.invoked")
	if invoked.GenAI == nil || invoked.GenAI.Tool == nil || invoked.GenAI.Tool.Call == nil || invoked.GenAI.Tool.Call.ID != "call-1" {
		t.Fatalf("tool.invoked gen_ai.tool.call = %#v, want the call id the hook path records", invoked.GenAI)
	}

	usage := findAction(t, events, "token.usage")
	if usage.GenAI == nil || usage.GenAI.Usage == nil || usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 100 {
		t.Fatalf("usage = %#v", usage.GenAI)
	}
	if usage.GenAI.Usage.CacheRead == nil || *usage.GenAI.Usage.CacheRead.InputTokens != 5 {
		t.Fatalf("usage cache_read = %#v", usage.GenAI.Usage.CacheRead)
	}
	if usage.Model != "swe-1-7" {
		t.Fatalf("usage model = %q", usage.Model)
	}

	command := findAction(t, events, "command.executed")
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command = %#v", command.Command)
	}
	if command.Command.ExitCode == nil || *command.Command.ExitCode != 0 {
		t.Fatalf("exit code = %#v", command.Command.ExitCode)
	}
	if command.Harness.Name != Harness || command.Harness.CollectionMethod != schema.CollectionMethodPoll {
		t.Fatalf("harness = %#v", command.Harness)
	}

	compacted := findAction(t, events, "session.compacted")
	if got := devinRaw(compacted)["summarized_from"]; got != float64(2) {
		t.Fatalf("compacted raw = %v", devinRaw(compacted))
	}

	state, err := LoadState(p.state)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	cursor := state.Sessions["quiet-meadow"]
	if cursor == nil || !cursor.Started || cursor.LastRowID != 4 || cursor.LastModel != "swe-1-7" {
		t.Fatalf("cursor = %#v", cursor)
	}
	if len(cursor.Recent) == 0 {
		t.Fatal("cursor kept no recent message ids; re-inserted rows would re-emit")
	}

	second, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	if second.EventsEmitted != 0 || second.SessionsChanged != 0 {
		t.Fatalf("second summary = %#v, want no new events", second)
	}
	if got := len(readEvents(t, p.log)); got != len(events) {
		t.Fatalf("second sweep wrote events: %d != %d", got, len(events))
	}
}

func TestCollectOnceReadsOnlyAppendedRows(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, userMsg("u1", "first", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	before := readEvents(t, p.log)

	addNode(t, db, "s1", 2, assistantMsg("a1", assistantFixture{Content: "second answer", Model: "swe-1-7", CreatedAt: "2023-11-14T22:15:00.000000Z"}), 1700000500, nil)
	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	after := readEvents(t, p.log)
	if got := eventActions(after[len(before):]); len(got) != 1 || got[0] != "agent.message" {
		t.Fatalf("appended sweep emitted %v, want just agent.message", got)
	}
	if summary.EventsEmitted != 1 {
		t.Fatalf("emitted = %d, want 1", summary.EventsEmitted)
	}
}

func TestCollectOnceAttributesPerTurnModelAndRecordsModelChanges(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", map[string]interface{}{"model": "claude-haiku-5-5-low"})
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{Content: "answer on model A", Model: "swe-1-7", CreatedAt: "2023-11-14T22:13:40.000000Z"}), 1700000100, nil)
	addNode(t, db, "s1", 2, assistantMsg("a2", assistantFixture{Content: "answer on model B", Model: "claude-haiku-5-5-low", CreatedAt: "2023-11-14T22:14:40.000000Z"}), 1700000200, nil)
	addNode(t, db, "s1", 3, userMsg("u1", "and now?", "2023-11-14T22:15:00.000000Z"), 1700000300, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	events := readEvents(t, p.log)

	var models []string
	for _, ev := range events {
		if ev.Event.Action == "agent.message" {
			models = append(models, ev.Model)
		}
	}
	if strings.Join(models, ",") != "swe-1-7,claude-haiku-5-5-low" {
		t.Fatalf("agent.message models = %v, want the per-turn models in order", models)
	}

	var changes []schema.Event
	for _, ev := range events {
		if ev.Event.Action == "session.context" {
			changes = append(changes, ev)
		}
	}
	if len(changes) != 1 {
		t.Fatalf("session.context events = %d, want exactly one model_change", len(changes))
	}
	change := devinRaw(changes[0])
	if change["change"] != "model_change" || change["from"] != "swe-1-7" || change["to"] != "claude-haiku-5-5-low" {
		t.Fatalf("model_change raw = %v", change)
	}
	if changes[0].Model != "claude-haiku-5-5-low" {
		t.Fatalf("model_change event model = %q", changes[0].Model)
	}

	// A prompt carries the model of the turn it belongs to: the last real generation_model.
	prompt := findAction(t, events, "prompt.submitted")
	if prompt.Model != "claude-haiku-5-5-low" {
		t.Fatalf("prompt after the switch has model %q, want claude-haiku-5-5-low", prompt.Model)
	}
}

func TestCollectOnceMapsFailingAndPassingCommands(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture("call-fail", "exec", map[string]interface{}{"command": "make test"})},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)
	addNode(t, db, "s1", 2, toolMsg("t1", "call-fail", "compile error", "2023-11-14T22:13:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": false, "kind": "execute", "failure_reason": "ToolError"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 1}},
	}), 1700000200, nil)
	addNode(t, db, "s1", 3, assistantMsg("a2", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture("call-pass", "exec", map[string]interface{}{"command": "make test"})},
		CreatedAt: "2023-11-14T22:14:40.000000Z",
	}), 1700000300, nil)
	addNode(t, db, "s1", 4, toolMsg("t2", "call-pass", "all good", "2023-11-14T22:14:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 0}},
	}), 1700000400, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	var commands []schema.Event
	for _, ev := range readEvents(t, p.log) {
		if ev.Event.Action == "command.executed" {
			commands = append(commands, ev)
		}
	}
	if len(commands) != 2 {
		t.Fatalf("command.executed = %d, want 2", len(commands))
	}
	failed, passed := commands[0], commands[1]
	if failed.Severity != schema.SeverityMedium || failed.Error == nil || failed.Error.Type != "command_failed" {
		t.Fatalf("failed command = %#v / %#v", failed.Severity, failed.Error)
	}
	if failed.GenAI.Tool.Call.ID != "call-fail" || *failed.Command.ExitCode != 1 {
		t.Fatalf("failed command identity = %#v", failed)
	}
	if passed.Severity != schema.SeverityInfo || passed.Error != nil {
		t.Fatalf("passing command = %#v", passed)
	}
}

func TestCollectOnceMapsAnEditToFileModified(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture("call-1", "edit", map[string]interface{}{"file_path": "main.go", "old_string": "a", "new_string": "b"})},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)
	addNode(t, db, "s1", 2, toolMsg("t1", "call-1", "edited", "2023-11-14T22:13:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "edit"},
	}), 1700000200, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	file := findAction(t, readEvents(t, p.log), "file.modified")
	if file.File == nil || file.File.Path != "main.go" || file.File.Operation != "modify" || file.File.Language != "go" {
		t.Fatalf("file = %#v", file.File)
	}
}

func TestResumeCopyRowsWithPriorNodeIDsAreSkipped(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, userMsg("u1", "hello", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)
	addNode(t, db, "s1", 2, assistantMsg("a1", assistantFixture{
		Content: "hi", Model: "swe-1-7",
		Metrics:   map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000200, nil)
	// A resume re-inserts the history as new nodes pointing back at what they copy. The
	// re-inserted prompt and assistant row keep their message_ids, so both must be skipped --
	// including the usage a naive cursor-position read would emit a second time.
	addNode(t, db, "s1", 3, userMsg("u1", "hello", "2023-11-14T22:13:20.000000Z"), 1700000300,
		`{"extensions":{"compact/prior_node_ids":[1]}}`)
	addNode(t, db, "s1", 4, assistantMsg("a1", assistantFixture{
		Content: "hi", Model: "swe-1-7",
		Metrics:   map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000400, `{"extensions":{"compact/prior_node_ids":[2]}}`)

	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	actions := eventActions(readEvents(t, p.log))
	count := map[string]int{}
	for _, a := range actions {
		count[a]++
	}
	if count["prompt.submitted"] != 1 || count["token.usage"] != 1 || count["agent.message"] != 1 {
		t.Fatalf("re-inserted rows were re-emitted: %v", actions)
	}
	if summary.EventsEmitted != len(actions) {
		t.Fatalf("emitted = %d, actions %v", summary.EventsEmitted, actions)
	}
}

func TestAdjacentReInsertWithoutPriorNodeIDsIsSkipped(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{Content: "same", Model: "swe-1-7", CreatedAt: "2023-11-14T22:13:40.000000Z"}), 1700000100, nil)
	// Devin re-inserts assistant rows with the same message_id and no prior ids, usually adjacent.
	addNode(t, db, "s1", 2, assistantMsg("a1", assistantFixture{Content: "same", Model: "swe-1-7", CreatedAt: "2023-11-14T22:13:40.000000Z"}), 1700000200, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	count := 0
	for _, ev := range readEvents(t, p.log) {
		if ev.Event.Action == "agent.message" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("agent.message = %d, want the adjacent re-insert skipped", count)
	}
}

func TestReInsertOnALaterSweepIsSkippedByThePersistedRing(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{Content: "first", Model: "swe-1-7", CreatedAt: "2023-11-14T22:13:40.000000Z"}), 1700000100, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	before := len(readEvents(t, p.log))

	// The same message_id written again rows later -- the ring the cursor persisted is what
	// catches it, since cursor position alone would read it as new.
	addNode(t, db, "s1", 2, assistantMsg("a1", assistantFixture{Content: "first", Model: "swe-1-7", CreatedAt: "2023-11-14T22:13:40.000000Z"}), 1700000200, nil)
	addNode(t, db, "s1", 3, assistantMsg("a2", assistantFixture{Content: "second", Model: "swe-1-7", CreatedAt: "2023-11-14T22:14:40.000000Z"}), 1700000300, nil)
	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	after := readEvents(t, p.log)
	if summary.EventsEmitted != 1 {
		t.Fatalf("second sweep emitted %d events, want only the genuinely new row", summary.EventsEmitted)
	}
	var messages []schema.Event
	for _, ev := range after[before:] {
		if ev.Event.Action == "agent.message" {
			messages = append(messages, ev)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("re-emitted agent.message %d times across sweeps", len(messages))
	}
}

func TestPriorNodeRowWithADifferentMessageIDIsNewContent(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, userMsg("u1", "hello", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)
	// A compaction summary names the nodes it condensed but carries its own message_id: that is
	// new content, not a copy, and must be collected.
	addNode(t, db, "s1", 2, assistantMsg("a-new", assistantFixture{Content: "new turn", Model: "swe-1-7", CreatedAt: "2023-11-14T22:14:40.000000Z"}), 1700000200,
		`{"extensions":{"compact/prior_node_ids":[1]}}`)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	msg := findAction(t, readEvents(t, p.log), "agent.message")
	if msg.GenAI == nil {
		t.Fatalf("prior-node row with a new message_id was skipped")
	}
}

func TestPendingToolCallResolvesAcrossSweeps(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture("call-1", "exec", map[string]interface{}{"command": "sleep 1 && echo done"})},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	state, err := LoadState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	if state.Sessions["s1"].PendingCalls["call-1"].Command != "sleep 1 && echo done" {
		t.Fatalf("pending call = %#v, want the command kept for a later result", state.Sessions["s1"].PendingCalls)
	}
	// Only the classification fields persist: an edit or write call's arguments can be
	// hundreds of kilobytes and are not needed to map its result.
	if raw, _ := json.Marshal(state.Sessions["s1"].PendingCalls); strings.Contains(string(raw), "arguments") {
		t.Fatalf("pending call persisted full arguments: %s", raw)
	}

	addNode(t, db, "s1", 2, toolMsg("t1", "call-1", "done", "2023-11-14T22:14:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 0}},
	}), 1700000200, nil)
	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	command := findAction(t, readEvents(t, p.log), "command.executed")
	if command.Command == nil || command.Command.Command != "sleep 1 && echo done" {
		t.Fatalf("result did not resolve the pending call's command: %#v", command.Command)
	}
	state, err = LoadState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions["s1"].PendingCalls["call-1"]; ok {
		t.Fatal("resolved call is still pending")
	}
}

func TestLiveCoverageSuppressesWhatHooksAlreadyRecorded(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	// The hook's first event for this session lands between the prompt and the assistant turn.
	cutover := time.Date(2023, 11, 14, 22, 13, 45, 0, time.UTC)
	addNode(t, db, "s1", 1, userMsg("u1", "early prompt", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)
	addNode(t, db, "s1", 2, assistantMsg("a1", assistantFixture{
		Content: "answer the hook could not see", Model: "swe-1-7",
		Metrics:   map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		ToolCalls: []map[string]interface{}{toolCallFixture("call-1", "exec", map[string]interface{}{"command": "ls"})},
		CreatedAt: "2023-11-14T22:14:00.000000Z",
	}), 1700000200, nil)
	addNode(t, db, "s1", 3, toolMsg("t1", "call-1", "files", "2023-11-14T22:14:10.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 0}},
	}), 1700000300, nil)

	opts := collectOptions(p)
	opts.LiveCoverage = map[string]time.Time{"s1": cutover}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	actions := eventActions(readEvents(t, p.log))
	// The prompt predates the cutover, so it is still collected; the tool call and its result
	// postdate it and would double-count the hook's own records. Assistant text, reasoning and
	// usage always emit -- the hook payload carries none of them.
	for _, suppressed := range []string{"tool.invoked", "command.executed"} {
		for _, action := range actions {
			if action == suppressed {
				t.Fatalf("%s was emitted though live hooks cover it: %v", suppressed, actions)
			}
		}
	}
	for _, kept := range []string{"session.started", "prompt.submitted", "agent.message", "token.usage"} {
		found := false
		for _, action := range actions {
			if action == kept {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s missing though hooks cannot see it: %v", kept, actions)
		}
	}
	if summary.EventsSuppressedLive != 2 {
		t.Fatalf("suppressed = %d, want 2 (tool.invoked + command.executed)", summary.EventsSuppressedLive)
	}
}

func TestRejectedToolCallEmitsToolFailedAndApprovalDenied(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture("call-1", "exec", map[string]interface{}{"command": "rm -rf build"})},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)
	addNode(t, db, "s1", 2, toolMsg("t1", "call-1", "rejected by operator", "2023-11-14T22:13:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{
			"success":        false,
			"kind":           "execute",
			"failure_reason": map[string]interface{}{"Rejected": map[string]interface{}{"interactive": true}},
		},
	}), 1700000200, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	events := readEvents(t, p.log)
	// The call carried a command, so its result is command.executed -- failed, medium severity,
	// and typed by the rejection rather than an exit code (there is none: it never ran).
	failed := findAction(t, events, "command.executed")
	if failed.Error == nil || failed.Error.Type != "rejected" || failed.Severity != schema.SeverityMedium {
		t.Fatalf("rejected command = %#v / %#v", failed.Severity, failed.Error)
	}
	denied := findAction(t, events, "approval.denied")
	if denied.Approval == nil || denied.Approval.Decision != "deny" || denied.Approval.Reason != "operator rejected the tool call" {
		t.Fatalf("approval = %#v", denied.Approval)
	}
	if denied.GenAI == nil || denied.GenAI.Tool == nil || denied.GenAI.Tool.Call == nil || denied.GenAI.Tool.Call.ID != "call-1" {
		t.Fatalf("approval gen_ai.tool.call = %#v", denied.GenAI)
	}
}

func TestUserRowWithoutIsUserInputIsNotAPrompt(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	// Compactor inputs and keepalives arrive as user rows without the operator flag.
	addNode(t, db, "s1", 1, chatJSON(map[string]interface{}{
		"role": "user", "message_id": "u-internal", "content": "summarize the conversation so far",
		"metadata": map[string]interface{}{"created_at": "2023-11-14T22:13:20.000000Z"},
	}), 1700000100, nil)
	addNode(t, db, "s1", 2, userMsg("u-real", "real prompt", "2023-11-14T22:14:00.000000Z"), 1700000200, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	prompts := 0
	for _, ev := range readEvents(t, p.log) {
		if ev.Event.Action == "prompt.submitted" {
			prompts++
			if ev.Prompt.Text != "real prompt" {
				t.Fatalf("internal user row became a prompt: %q", ev.Prompt.Text)
			}
		}
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
}

// The compactor's own rows carry internal labels instead of a model: their content is the
// summary the following devin-rs/summary system row carries, so they must not surface as an
// agent reply -- only their token metrics, with no model and the label kept in raw.
func TestCompactorAssistantRowEmitsUsageWithoutAPersonaOrModel(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{Content: "real answer", Model: "swe-1-7", CreatedAt: "2023-11-14T22:13:40.000000Z"}), 1700000100, nil)
	addNode(t, db, "s1", 2, assistantMsg("comp-1", assistantFixture{
		Content:   "<summary>condensed history</summary>",
		Thinking:  "condensing",
		Model:     "compactor",
		Metrics:   map[string]interface{}{"input_tokens": 900, "output_tokens": 40},
		CreatedAt: "2023-11-14T22:14:00.000000Z",
	}), 1700000200, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	events := readEvents(t, p.log)
	agentMessages, reasoning := 0, 0
	var usage schema.Event
	foundUsage := false
	for _, ev := range events {
		switch ev.Event.Action {
		case "agent.message":
			agentMessages++
		case "agent.reasoning":
			reasoning++
		case "token.usage":
			usage = ev
			foundUsage = true
		}
	}
	if agentMessages != 1 || reasoning != 0 {
		t.Fatalf("compactor row surfaced as a reply: %d agent.message, %d agent.reasoning", agentMessages, reasoning)
	}
	if !foundUsage {
		t.Fatal("no token.usage for the compactor row")
	}
	if devinRaw(usage)["generation_model"] != "compactor" {
		t.Fatalf("usage raw generation_model = %v, want the label kept in raw", devinRaw(usage))
	}
	if usage.Model != "" || (usage.GenAI != nil && usage.GenAI.Request != nil && usage.GenAI.Request.Model != "") {
		t.Fatalf("compactor usage carried a model: %#v", usage)
	}
	if usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 900 {
		t.Fatalf("compactor usage = %#v", usage.GenAI.Usage)
	}
	// The compactor's label must never become the session's model for later events.
	addNode(t, db, "s1", 3, userMsg("u1", "next", "2023-11-14T22:15:00.000000Z"), 1700000300, nil)
	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	prompt := findAction(t, readEvents(t, p.log), "prompt.submitted")
	if prompt.Model != "swe-1-7" {
		t.Fatalf("prompt model = %q, want the last real model, not the compactor label", prompt.Model)
	}
}

func TestSinceLeavesOldSessionsForALaterSync(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "old", map[string]interface{}{"created_at": 1600000000, "last_activity_at": 1600000000})
	addNode(t, db, "old", 1, userMsg("u-old", "old prompt", "2020-09-13T12:26:40.000000Z"), 1600000000, nil)
	addSession(t, db, "new", nil)
	addNode(t, db, "new", 1, userMsg("u-new", "new prompt", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)

	opts := collectOptions(p)
	opts.Since = time.Unix(1650000000, 0)
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.Sessions != 1 {
		t.Fatalf("sessions = %d, want only the recent one", summary.Sessions)
	}
	state, err := LoadState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions["old"]; ok {
		t.Fatal("the skipped session got a cursor; a later full sync would read past its start")
	}
	if state.Sessions["new"] == nil || !state.Sessions["new"].Started {
		t.Fatalf("recent session was not collected: %#v", state.Sessions)
	}
}

func TestBudgetStopsTheSweepInsideAPageWithoutLosingRows(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	for i := 1; i <= 20; i++ {
		addNode(t, db, "s1", int64(i), userMsg(fmt.Sprintf("u%d", i), strings.Repeat("word ", 200), "2023-11-14T22:13:20.000000Z"), int64(1700000000+i), nil)
	}

	opts := collectOptions(p)
	// Enough room for the session.started event and a handful of prompts, not the rest.
	opts.Budget = writer.NewBudget(12000)
	_, err := CollectOnce(opts)
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("err = %v, want ErrBudgetSpent", err)
	}
	written := readEvents(t, p.log)
	if len(written) == 0 {
		t.Fatal("the budget allowed nothing at all; the fixture is off")
	}

	// The cursor may only sit past rows whose events are all written. With page-granularity
	// restore it lands on a page boundary at or before the last written row.
	state, err := LoadState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	cursor := state.Sessions["s1"]
	if cursor.LastRowID > int64(len(written)) {
		t.Fatalf("cursor at row %d with only %d events written", cursor.LastRowID, len(written))
	}

	// The follow-up unbudgeted sweep finishes the session; deterministic ids keep re-emitted
	// events from duplicating in the log.
	opts.Budget = nil
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("follow-up CollectOnce: %v", err)
	}
	all := readEvents(t, p.log)
	ids := map[string]bool{}
	for _, ev := range all {
		if ids[ev.Event.ID] {
			t.Fatalf("duplicate event id in the log: %s", ev.Event.ID)
		}
		ids[ev.Event.ID] = true
	}
	prompts := 0
	for _, ev := range all {
		if ev.Event.Action == "prompt.submitted" {
			prompts++
		}
	}
	if prompts != 20 {
		t.Fatalf("prompts = %d across both sweeps, want 20", prompts)
	}
}

func TestPrintModeWritesNothingAndLeavesNoState(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	seedBasicSession(t, db, "s1")

	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{
		DBPath: p.db, Print: true, Out: &out,
	})
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.EventsEmitted == 0 {
		t.Fatal("print mode mapped no events")
	}
	lines := 0
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("print line is not an event: %v", err)
		}
		lines++
	}
	if lines != summary.EventsEmitted {
		t.Fatalf("printed %d events, summary says %d", lines, summary.EventsEmitted)
	}
	if _, err := os.Stat(p.log); !os.IsNotExist(err) {
		t.Fatal("print mode wrote the runtime log")
	}
	if _, err := os.Stat(p.state); !os.IsNotExist(err) {
		t.Fatal("print mode wrote a cursor file")
	}
}

func TestMissingDatabaseIsNotAnError(t *testing.T) {
	p := newFixturePaths(t)
	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("CollectOnce on a missing db: %v", err)
	}
	if summary.Sessions != 0 || summary.EventsEmitted != 0 || summary.Errors != 0 {
		t.Fatalf("summary = %#v, want zero", summary)
	}
}

func TestCorruptDatabaseIsAnError(t *testing.T) {
	p := newFixturePaths(t)
	if err := os.WriteFile(p.db, []byte("this is not sqlite at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectOnce(collectOptions(p)); err == nil {
		t.Fatal("a corrupt database did not error")
	}
}

func TestRowsInAnUncheckpointedWALAreRead(t *testing.T) {
	p := newFixturePaths(t)
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;` + devinFixtureDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	// The writer stays open, so the rows live only in the WAL while the collector reads.
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, userMsg("u1", "wal prompt", "2023-11-14T22:13:20.000000Z"), 1700000100, nil)

	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("CollectOnce over an uncheckpointed WAL: %v", err)
	}
	if summary.EventsEmitted == 0 {
		t.Fatal("rows still in the WAL were not read")
	}
	findAction(t, readEvents(t, p.log), "prompt.submitted")
}

func TestUnknownColumnsAndJSONKeysAreTolerated(t *testing.T) {
	p := newFixturePaths(t)
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Newer Devin builds may add columns to either table and keys to the message JSON.
	_, err = db.Exec(`
CREATE TABLE sessions (id TEXT PRIMARY KEY, working_directory TEXT NOT NULL, backend_type TEXT NOT NULL, model TEXT NOT NULL, agent_mode TEXT NOT NULL, created_at INTEGER NOT NULL, last_activity_at INTEGER NOT NULL, title TEXT, hidden INTEGER NOT NULL DEFAULT 0, metadata TEXT, future_column TEXT);
CREATE TABLE message_nodes (row_id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, node_id INTEGER NOT NULL, parent_node_id INTEGER, chat_message TEXT NOT NULL, created_at INTEGER NOT NULL, metadata TEXT, another_future INTEGER);`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode, created_at, last_activity_at, future_column)
VALUES ('s1', '/repo', 'local', 'swe-1-7', 'standard', 1700000000, 1700000000, 'x')`); err != nil {
		t.Fatal(err)
	}
	msg := chatJSON(map[string]interface{}{
		"role": "user", "message_id": "u1", "content": "hi", "unknown_field": map[string]interface{}{"deep": true},
		"metadata": map[string]interface{}{"created_at": "2023-11-14T22:13:20.000000Z", "is_user_input": true, "unknown_meta": 1},
	})
	if _, err := db.Exec(`INSERT INTO message_nodes (session_id, node_id, chat_message, created_at) VALUES ('s1', 1, ?, 1700000100)`, msg); err != nil {
		t.Fatal(err)
	}

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce with unknown columns/keys: %v", err)
	}
	findAction(t, readEvents(t, p.log), "prompt.submitted")
}

func TestMissingOptionalColumnsAreTolerated(t *testing.T) {
	p := newFixturePaths(t)
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// An older store without the optional session columns still collects; the optional fields
	// simply read as NULL.
	_, err = db.Exec(`
CREATE TABLE sessions (id TEXT PRIMARY KEY, working_directory TEXT NOT NULL, backend_type TEXT NOT NULL, model TEXT NOT NULL, agent_mode TEXT NOT NULL, created_at INTEGER NOT NULL, last_activity_at INTEGER NOT NULL);
CREATE TABLE message_nodes (row_id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, node_id INTEGER NOT NULL, chat_message TEXT NOT NULL, created_at INTEGER NOT NULL);`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions VALUES ('s1', '/repo', 'local', 'swe-1-7', 'standard', 1700000000, 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message_nodes (session_id, node_id, chat_message, created_at) VALUES ('s1', 1, ?, 1700000100)`,
		userMsg("u1", "hi", "2023-11-14T22:13:20.000000Z")); err != nil {
		t.Fatal(err)
	}

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce without optional columns: %v", err)
	}
	findAction(t, readEvents(t, p.log), "prompt.submitted")
}

func TestMissingRequiredColumnIsAClearError(t *testing.T) {
	p := newFixturePaths(t)
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE sessions (id TEXT PRIMARY KEY);
CREATE TABLE message_nodes (row_id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, node_id INTEGER NOT NULL, created_at INTEGER NOT NULL);`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	_, err = CollectOnce(collectOptions(p))
	if err == nil || !strings.Contains(err.Error(), "message_nodes.chat_message") {
		t.Fatalf("err = %v, want one naming the missing column", err)
	}
}

// Rows from before message_id existed carry none; their identity is their content, so wholesale
// re-inserts of the same row collapse onto one event id while different legacy rows stay distinct.
func TestLegacyRowsWithoutMessageIDDedupeByContent(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	legacy := chatJSON(map[string]interface{}{
		"role": "user", "content": "legacy prompt", "metadata": map[string]interface{}{
			"created_at": "2023-11-14T22:13:20.000000Z", "is_user_input": true}})
	addNode(t, db, "s1", 1, legacy, 1700000100, nil)
	// An old-style resume re-inserts the same row as a new node with no prior ids.
	addNode(t, db, "s1", 2, legacy, 1700000200, nil)
	addNode(t, db, "s1", 3, chatJSON(map[string]interface{}{
		"role": "user", "content": "different legacy prompt", "metadata": map[string]interface{}{
			"created_at": "2023-11-14T22:13:30.000000Z", "is_user_input": true}}), 1700000300, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	var prompts []schema.Event
	for _, ev := range readEvents(t, p.log) {
		if ev.Event.Action == "prompt.submitted" {
			prompts = append(prompts, ev)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want the duplicate legacy row collapsed and the distinct one kept", len(prompts))
	}
	if prompts[0].Event.ID == prompts[1].Event.ID {
		t.Fatalf("different legacy rows share an event id: %s", prompts[0].Event.ID)
	}
}

// The third re-insert shape: the same assistant turn written again with a new message_id but the
// same request_id. On real data these groups carry identical token metrics, so the request_id
// joins the dedupe ring.
func TestReInsertedAssistantRowWithSameRequestIDIsSkipped(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Content: "hi", Model: "swe-1-7", RequestID: "req-shared",
		Metrics:   map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)
	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	before := len(readEvents(t, p.log))

	// New message_id, same request_id: skipped across sweeps because the request digest persists.
	addNode(t, db, "s1", 2, assistantMsg("a1-copy", assistantFixture{
		Content: "hi", Model: "swe-1-7", RequestID: "req-shared",
		Metrics:   map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000200, nil)
	// A different request_id is a genuinely different turn and still collects.
	addNode(t, db, "s1", 3, assistantMsg("a2", assistantFixture{
		Content: "next", Model: "swe-1-7", RequestID: "req-other",
		Metrics:   map[string]interface{}{"input_tokens": 20, "output_tokens": 7},
		CreatedAt: "2023-11-14T22:14:40.000000Z",
	}), 1700000300, nil)
	summary, err := CollectOnce(collectOptions(p))
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	after := readEvents(t, p.log)
	var usages int
	for _, ev := range after {
		if ev.Event.Action == "token.usage" {
			usages++
			if *ev.GenAI.Usage.InputTokens == 10 {
				continue
			}
		}
	}
	if usages != 2 {
		t.Fatalf("token.usage events = %d, want 2 (the shared-request copy must not recount)", usages)
	}
	if summary.EventsEmitted != 2 || len(after)-before != 2 {
		t.Fatalf("second sweep emitted %d, want only the new request's agent.message+token.usage", summary.EventsEmitted)
	}
}

// Call ids are not unique: some models reuse ids like "read_12" for different calls. Sequential
// resolution still attributes each result to its own invocation because a result consumes the
// pending entry.
func TestSequentialCallsSharingAnIDEachResolveTheirOwnResult(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	shared := "edit:4"
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture(shared, "exec", map[string]interface{}{"command": "make first"})},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)
	addNode(t, db, "s1", 2, toolMsg("t1", shared, "first output", "2023-11-14T22:13:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 0}},
	}), 1700000200, nil)
	// A later, different call reuses the same id; its result must map to its own command.
	addNode(t, db, "s1", 3, assistantMsg("a2", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture(shared, "exec", map[string]interface{}{"command": "make second"})},
		CreatedAt: "2023-11-14T22:14:40.000000Z",
	}), 1700000300, nil)
	addNode(t, db, "s1", 4, toolMsg("t2", shared, "second output", "2023-11-14T22:14:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 0}},
	}), 1700000400, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	var commands []string
	for _, ev := range readEvents(t, p.log) {
		if ev.Event.Action == "command.executed" {
			commands = append(commands, ev.Command.Command)
		}
	}
	if strings.Join(commands, ",") != "make first,make second" {
		t.Fatalf("commands = %v, want each result mapped to its own call", commands)
	}
}

// A tool result whose call the pending map never saw (invoked before the cursor, or an id
// collision) still names its tool: the row's own telemetry.operation carries it.
func TestUnknownToolResultFallsBackToTelemetryOperation(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	msg := chatJSON(map[string]interface{}{
		"role": "tool", "message_id": "t1", "tool_call_id": "call-never-seen", "content": "listing",
		"metadata": map[string]interface{}{
			"created_at": "2023-11-14T22:13:50.000000Z",
			"telemetry":  map[string]interface{}{"operation": "exec"},
			"extensions": map[string]interface{}{"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"}},
		}})
	addNode(t, db, "s1", 1, msg, 1700000100, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	// No command is recorded for this call, so it is a tool completion, not a command event:
	// the name still resolves from telemetry.operation.
	completed := findAction(t, readEvents(t, p.log), "tool.completed")
	if completed.Tool == nil || completed.Tool.Name != "exec" {
		t.Fatalf("tool name = %#v, want telemetry.operation", completed.Tool)
	}
}

// Devin's get_output takes a shell_id, not a command: it is an execute-kind call but has no
// command to put on the event, so it completes as a tool event rather than a command one.
func TestGetOutputCallCompletesAsAToolEvent(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Model:     "swe-1-7",
		ToolCalls: []map[string]interface{}{toolCallFixture("call-1", "get_output", map[string]interface{}{"shell_id": "abc"})},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)
	addNode(t, db, "s1", 2, toolMsg("t1", "call-1", "chunk of output", "2023-11-14T22:13:50.000000Z", map[string]interface{}{
		"chisel/tool_result_meta": map[string]interface{}{"success": true, "kind": "execute"},
		"chisel/terminal_output":  map[string]interface{}{"exit": map[string]interface{}{"exit_code": 0}},
	}), 1700000200, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	events := readEvents(t, p.log)
	for _, ev := range events {
		if ev.Event.Action == "command.executed" {
			t.Fatalf("get_output produced command.executed: %#v", ev)
		}
	}
	completed := findAction(t, events, "tool.completed")
	if completed.Tool == nil || completed.Tool.Name != "get_output" {
		t.Fatalf("tool = %#v, want get_output", completed.Tool)
	}
	if completed.GenAI == nil || completed.GenAI.Tool == nil || completed.GenAI.Tool.Call == nil || completed.GenAI.Tool.Call.Result != "chunk of output" {
		t.Fatalf("tool result did not carry the output: %#v", completed.GenAI)
	}
}

// A subagent's row contributes subagent provenance AND finish_reason to one event's raw.devin:
// a shallow merge would drop one for the other.
func TestSubagentMessageKeepsProvenanceAndFinishReason(t *testing.T) {
	p := newFixturePaths(t)
	db := openFixtureDB(t, p.db)
	addSession(t, db, "s1", nil)
	addNode(t, db, "s1", 1, assistantMsg("a1", assistantFixture{
		Content: "subagent reply", Finish: "stop",
		Extensions: map[string]interface{}{
			"subagent/agent_id":     "agent-9",
			"subagent/profile_name": "reviewer",
			"subagent/model":        "claude-haiku-5-5-low",
		},
		CreatedAt: "2023-11-14T22:13:40.000000Z",
	}), 1700000100, nil)

	if _, err := CollectOnce(collectOptions(p)); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	msg := findAction(t, readEvents(t, p.log), "agent.message")
	raw := devinRaw(msg)
	if raw["subagent_id"] != "agent-9" || raw["subagent_profile"] != "reviewer" || raw["finish_reason"] != "stop" {
		t.Fatalf("raw.devin = %v, want subagent provenance and finish_reason together", raw)
	}
	if msg.Model != "claude-haiku-5-5-low" {
		t.Fatalf("subagent row model = %q, want the subagent profile model", msg.Model)
	}
}
