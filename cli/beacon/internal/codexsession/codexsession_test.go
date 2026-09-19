package codexsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestListFindsCodexRolloutSessions(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, ".codex")
	sessionPath := filepath.Join(codexDir, "sessions", "2026", "09", "19", "rollout-2026-09-19T20-00-00-sess-1.jsonl")
	writeFile(t, sessionPath, sessionMetaLine("sess-1", "/tmp/repo")+"\n")
	writeFile(t, filepath.Join(codexDir, "session_index.jsonl"), `{"id":"sess-1","thread_name":"Add telemetry","updated_at":"2026-09-19T20:00:00Z"}`+"\n")

	store, err := NewStore(codexDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("List returned %d refs, want 1: %+v", len(refs), refs)
	}
	if refs[0].ID != "sess-1" || refs[0].Workspace != "/tmp/repo" {
		t.Fatalf("ref metadata = %+v", refs[0])
	}
	if refs[0].Index == nil || refs[0].Index.ThreadName != "Add telemetry" {
		t.Fatalf("index not attached: %+v", refs[0].Index)
	}
}

func TestMapCodexTranscriptProducesEndpointEvents(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/codex.jsonl", Workspace: "/tmp/repo"}
	records := decodeFixture(t, []string{
		sessionMetaLine("sess-1", "/tmp/repo"),
		turnContextLine("turn-1", "gpt-6-astra"),
		messageLine("user", "msg-user", "add a health endpoint"),
		messageLine("assistant", "msg-assistant", "I'll run tests."),
		toolCallLine("call-1", "exec", "npm test"),
		tokenUsageLine("sess-1", "turn-1", 120, 90, 10, 8, 2),
		toolOutputLine("call-1", "ok", "completed"),
		taskCompleteLine("turn-1"),
	})

	mapped := MapSession(ref, records, MapOptions{})
	got := actions(mapped)
	want := []string{"session.started", "prompt.submitted", "agent.message", "tool.invoked", "token.usage", "command.executed", "session.status"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for _, item := range mapped {
		ev := item.Event
		if ev.Harness.Name != Harness || ev.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Fatalf("%s harness = %+v, want Codex poll", ev.Event.Action, ev.Harness)
		}
		if ev.Origin != schema.OriginLocal {
			t.Fatalf("%s origin = %q, want local", ev.Event.Action, ev.Origin)
		}
		if ev.Event.ID == "" {
			t.Fatalf("%s missing deterministic event id", ev.Event.Action)
		}
		if err := ev.Validate(); err != nil {
			t.Fatalf("%s failed validation: %v", ev.Event.Action, err)
		}
	}
	command := findAction(t, mapped, "command.executed")
	if command.Command == nil || command.Command.Command != "npm test" || command.Command.Output != "ok" {
		t.Fatalf("command = %+v, want npm test with output", command.Command)
	}
	usage := findAction(t, mapped, "token.usage")
	if usage.GenAI == nil || usage.GenAI.Usage == nil || usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 20 {
		t.Fatalf("usage = %+v, want uncached input tokens", usage.GenAI)
	}
	if usage.GenAI.Usage.CacheRead == nil || usage.GenAI.Usage.CacheCreation == nil {
		t.Fatalf("usage cache fields missing: %+v", usage.GenAI.Usage)
	}
}

func TestTokenCountLastWinsForMultipleSnapshotsPerTurn(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/codex.jsonl", Workspace: "/tmp/repo"}
	records := decodeFixture(t, []string{
		sessionMetaLine("sess-1", "/tmp/repo"),
		turnContextLine("turn-1", "gpt-6-astra"),
		messageLine("user", "msg-user", "build it"),
		tokenCountLine("turn-1", 0, 0, 0, 0),
		tokenCountLine("turn-1", 50, 20, 50, 20),
		tokenCountLine("turn-1", 80, 30, 130, 50),
	})
	mapped := MapSession(ref, records, MapOptions{})
	var usageEvents []schema.Event
	for _, item := range mapped {
		if item.Event.Event.Action == "token.usage" {
			usageEvents = append(usageEvents, item.Event)
		}
	}
	if len(usageEvents) != 1 {
		t.Fatalf("expected 1 token.usage event, got %d", len(usageEvents))
	}
	ev := usageEvents[0]
	if ev.GenAI == nil || ev.GenAI.Usage == nil || ev.GenAI.Usage.OutputTokens == nil {
		t.Fatal("missing usage on token.usage event")
	}
	if *ev.GenAI.Usage.OutputTokens != 30 {
		t.Fatalf("output tokens = %d, want 30 (last snapshot)", *ev.GenAI.Usage.OutputTokens)
	}
}

func TestCollectOnceUsesCursorAndPrintDoesNotAdvance(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	sessionPath := filepath.Join(codexDir, "sessions", "2026", "09", "19", "rollout-2026-09-19T20-00-00-sess-1.jsonl")
	writeFile(t, sessionPath, sessionMetaLine("sess-1", "/tmp/repo")+"\n"+messageLine("user", "u1", "first")+"\n")
	statePath := filepath.Join(dir, "state", "codex.json")
	logPath := filepath.Join(dir, "runtime.jsonl")

	opts := CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
	first, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	if first.EventsEmitted == 0 {
		t.Fatal("first sweep emitted no events")
	}
	before := readLog(t, logPath)
	second, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	if second.EventsEmitted != 0 {
		t.Fatalf("second sweep emitted %d events, want 0", second.EventsEmitted)
	}
	if after := readLog(t, logPath); len(after) != len(before) {
		t.Fatalf("log grew from %d to %d events with unchanged cursor", len(before), len(after))
	}

	writeFile(t, sessionPath, sessionMetaLine("sess-1", "/tmp/repo")+"\n"+messageLine("user", "u1", "first")+"\n"+messageLine("user", "u2", "second")+"\n")
	var printed strings.Builder
	printSummary, err := CollectOnce(CollectOptions{CodexDir: codexDir, Print: true, Out: &printed})
	if err != nil {
		t.Fatalf("print CollectOnce: %v", err)
	}
	if printSummary.EventsEmitted == 0 || printed.Len() == 0 {
		t.Fatalf("print sweep emitted nothing: summary=%+v output=%q", printSummary, printed.String())
	}
	realSummary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("real CollectOnce after print: %v", err)
	}
	if realSummary.EventsEmitted == 0 {
		t.Fatal("--print advanced the cursor; real sweep had nothing left")
	}
}

func decodeFixture(t *testing.T, lines []string) []Record {
	t.Helper()
	records, stats, err := decodeRecords(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatalf("decodeRecords: %v", err)
	}
	if stats.Malformed != 0 {
		t.Fatalf("malformed fixture: %+v", stats)
	}
	return records
}

func actions(mapped []MappedEvent) []string {
	out := make([]string, 0, len(mapped))
	for _, item := range mapped {
		out = append(out, item.Event.Event.Action)
	}
	return out
}

func findAction(t *testing.T, mapped []MappedEvent, action string) schema.Event {
	t.Helper()
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			return item.Event
		}
	}
	t.Fatalf("no %s in %v", action, actions(mapped))
	return schema.Event{}
}

func readLog(t *testing.T, path string) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev schema.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sessionMetaLine(sessionID, cwd string) string {
	return `{"timestamp":"2026-09-19T22:00:00.000Z","type":"session_meta","payload":{"session_id":` + quote(sessionID) + `,"id":` + quote(sessionID) + `,"timestamp":"2026-09-19T22:00:00.000Z","cwd":` + quote(cwd) + `,"originator":"codex-tui","cli_version":"0.153.4","source":"cli"}}`
}

func turnContextLine(turnID, model string) string {
	return `{"timestamp":"2026-09-19T22:00:01.000Z","type":"turn_context","payload":{"turn_id":` + quote(turnID) + `,"cwd":"/tmp/repo","model":` + quote(model) + `}}`
}

func messageLine(role, id, text string) string {
	blockType := "input_text"
	if role == "assistant" {
		blockType = "output_text"
	}
	return `{"timestamp":"2026-09-19T22:00:02.000Z","type":"response_item","payload":{"type":"message","id":` + quote(id) + `,"role":` + quote(role) + `,"content":[{"type":` + quote(blockType) + `,"text":` + quote(text) + `}]}}`
}

func toolCallLine(callID, name, input string) string {
	return `{"timestamp":"2026-09-19T22:00:03.000Z","type":"response_item","payload":{"type":"custom_tool_call","id":"ctc_1","status":"completed","call_id":` + quote(callID) + `,"name":` + quote(name) + `,"input":` + quote(input) + `}}`
}

func toolOutputLine(callID, output, status string) string {
	return `{"timestamp":"2026-09-19T22:00:04.000Z","type":"response_item","payload":{"type":"custom_tool_call_output","id":"ctco_1","call_id":` + quote(callID) + `,"output":[{"type":"output_text","text":` + quote(output) + `}],"status":` + quote(status) + `}}`
}

func tokenUsageLine(sessionID, turnID string, input, cached, cacheWrite, output, reasoning int64) string {
	return `{"timestamp":"2026-09-19T22:00:04.000Z","type":"token_usage_record","payload":{"thread_id":` + quote(sessionID) + `,"turn_id":` + quote(turnID) + `,"session_id":` + quote(sessionID) + `,"response_id":"resp_1","turn_token_usage":{"input_tokens":` + i64(input) + `,"cached_input_tokens":` + i64(cached) + `,"cache_write_input_tokens":` + i64(cacheWrite) + `,"output_tokens":` + i64(output) + `,"reasoning_output_tokens":` + i64(reasoning) + `,"total_tokens":130}}}`
}

func taskCompleteLine(turnID string) string {
	return `{"timestamp":"2026-09-19T22:00:05.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":` + quote(turnID) + `,"duration_ms":1234,"time_to_first_token_ms":99}}`
}

func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func tokenCountLine(turnID string, lastInput, lastOutput, totalInput, totalOutput int64) string {
	return `{"timestamp":"2026-09-19T22:00:04.000Z","type":"event_msg","payload":{"type":"token_count","turn_id":` + quote(turnID) + `,"info":{"last_token_usage":{"input_tokens":` + i64(lastInput) + `,"output_tokens":` + i64(lastOutput) + `},"total_token_usage":{"input_tokens":` + i64(totalInput) + `,"output_tokens":` + i64(totalOutput) + `}}}}`
}

func i64(v int64) string {
	return strconv.FormatInt(v, 10)
}
