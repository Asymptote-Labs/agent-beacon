package copilotsession

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func writeCopilotFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	const sessionID = "caab1e17-509c-43b4-9ff2-13d1427c36a1"
	dir := filepath.Join(root, "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := strings.Join([]string{
		"id: " + sessionID,
		"cwd: /repo",
		"git_root: /repo",
		"repository: Asymptote-Labs/agent-beacon",
		"branch: main",
		"name: Fixture Session",
		"created_at: 2026-05-24T23:00:09.657Z",
		"updated_at: 2026-05-24T23:00:39.908Z",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, WorkspaceFile), []byte(workspace), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"id":"s1","timestamp":1770000000000,"type":"session.start","data":{"sessionId":"caab1e17-509c-43b4-9ff2-13d1427c36a1","copilotVersion":"1.2.3","selectedModel":"gpt-5.3-codex","context":{"cwd":"/repo","repository":"Asymptote-Labs/agent-beacon","branch":"main"}}}`,
		`{"id":"u1","timestamp":1770000000100,"type":"user.message","data":{"content":"run tests"}}`,
		`{"id":"t1","timestamp":1770000000200,"type":"tool.execution_start","data":{"toolCallId":"call_bash","toolName":"bash","turnId":"turn1","arguments":{"command":"go test ./...","description":"Run tests"}}}`,
		`{"id":"t2","timestamp":1770000000500,"type":"tool.execution_complete","data":{"toolCallId":"call_bash","turnId":"turn1","success":true,"result":{"content":"ok","detailedContent":"ok"},"toolTelemetry":{"properties":{"command":"go test ./..."}}}}`,
		`{"id":"t3","timestamp":1770000000600,"type":"tool.execution_start","data":{"toolCallId":"call_view","toolName":"view","turnId":"turn1","arguments":{"path":"README.md"}}}`,
		`{"id":"t4","timestamp":1770000000700,"type":"tool.execution_complete","data":{"toolCallId":"call_view","turnId":"turn1","success":true,"result":{"content":"# Beacon"}}}`,
		`{"id":"a1","timestamp":1770000000800,"type":"assistant.message","data":{"content":"Done.","messageId":"msg1","model":"gpt-5.3-codex","outputTokens":20,"turnId":"turn1"}}`,
		`{"id":"x1","timestamp":1770000000900,"type":"session.shutdown","data":{"currentModel":"gpt-5.3-codex","modelMetrics":{"gpt-5.3-codex":{"requests":{"count":1,"cost":0.01},"totalNanoAiu":1000,"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":2,"cacheWriteTokens":3,"reasoningTokens":5}}},"totalPremiumRequests":1,"totalNanoAiu":1000}}`,
	}
	path := filepath.Join(dir, EventsFile)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return sessionID, path
}

func TestMapSessionReconstructsCommandsFilesAndUsage(t *testing.T) {
	root := t.TempDir()
	_, _ = writeCopilotFixture(t, root)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	events := MapSession(refs[0], records, MapOptions{})
	actions := map[string]int{}
	for _, item := range events {
		ev := item.Event
		actions[ev.Event.Action]++
		if ev.Harness.Name != Harness || ev.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Fatalf("bad provenance: %+v", ev.Harness)
		}
		if ev.Origin != schema.OriginLocal {
			t.Fatalf("bad origin: %q", ev.Origin)
		}
	}
	for _, action := range []string{"session.started", "prompt.submitted", "tool.invoked", "command.executed", "file.read", "agent.message", "token.usage"} {
		if actions[action] == 0 {
			t.Fatalf("missing action %q in %#v", action, actions)
		}
	}
	if actions["token.usage"] != 2 {
		t.Fatalf("token usage should be split into assistant output and shutdown input/cache/reasoning, got %d", actions["token.usage"])
	}

	var command schema.Event
	var fileRead schema.Event
	var shutdownUsage schema.Event
	for _, item := range events {
		switch item.Event.Event.Action {
		case "command.executed":
			command = item.Event
		case "file.read":
			fileRead = item.Event
		case "token.usage":
			if item.Event.GenAI != nil && item.Event.GenAI.Usage != nil && item.Event.GenAI.Usage.InputTokens != nil {
				shutdownUsage = item.Event
			}
		}
	}
	if command.Command == nil || command.Command.Command != "go test ./..." || command.Command.Output != "ok" {
		t.Fatalf("command event = %+v", command.Command)
	}
	if fileRead.File == nil || fileRead.File.Path != "README.md" || fileRead.File.Operation != "read" {
		t.Fatalf("file event = %+v", fileRead.File)
	}
	if shutdownUsage.GenAI == nil || shutdownUsage.GenAI.Usage == nil || shutdownUsage.GenAI.Usage.OutputTokens != nil {
		t.Fatalf("shutdown usage double-counted output tokens: %+v", shutdownUsage.GenAI)
	}
	if got := *shutdownUsage.GenAI.Usage.InputTokens; got != 100 {
		t.Fatalf("shutdown input tokens = %d, want 100", got)
	}
	if got := *shutdownUsage.GenAI.Usage.CacheRead.InputTokens; got != 2 {
		t.Fatalf("cache read tokens = %d, want 2", got)
	}
	if got := *shutdownUsage.GenAI.Usage.CacheCreation.InputTokens; got != 3 {
		t.Fatalf("cache write tokens = %d, want 3", got)
	}
	if got := *shutdownUsage.GenAI.Usage.Reasoning.OutputTokens; got != 5 {
		t.Fatalf("reasoning tokens = %d, want 5", got)
	}
}

func TestCollectOnceIsIdempotentAndPrintDoesNotAdvanceState(t *testing.T) {
	root := t.TempDir()
	_, _ = writeCopilotFixture(t, root)
	statePath := filepath.Join(root, "state.json")
	logPath := filepath.Join(root, "runtime.jsonl")
	var printed bytes.Buffer
	printSummary, err := CollectOnce(CollectOptions{CopilotDir: root, StatePath: statePath, Print: true, Out: &printed})
	if err != nil {
		t.Fatalf("print CollectOnce: %v", err)
	}
	if printSummary.EventsEmitted == 0 || !strings.Contains(printed.String(), `"copilot_cli"`) {
		t.Fatalf("--print emitted nothing useful: %+v\n%s", printSummary, printed.String())
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("--print wrote collector state")
	}

	opts := CollectOptions{CopilotDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
	first, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	if first.EventsEmitted == 0 {
		t.Fatal("first sweep emitted no events")
	}
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	second, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read runtime log again: %v", err)
	}
	if second.EventsEmitted != 0 || !bytes.Equal(before, after) {
		t.Fatalf("second sweep was not idempotent: %+v", second)
	}
}

func TestDecoderReportsPartialTailAndCollectsItLater(t *testing.T) {
	root := t.TempDir()
	_, path := writeCopilotFixture(t, root)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"partial","type":"user.message","data":{"content":"later"}`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := store.List()
	_, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !stats.PartialTail {
		t.Fatal("partial tail was not reported")
	}

	if err := os.WriteFile(path, []byte(strings.TrimRight(readFile(t, path), "\n")+`}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if stats.PartialTail {
		t.Fatal("completed tail still reported partial")
	}
	if records[len(records)-1].ID != "partial" {
		t.Fatalf("last record = %+v", records[len(records)-1])
	}
}

func TestEventIDsAreDeterministic(t *testing.T) {
	root := t.TempDir()
	_, _ = writeCopilotFixture(t, root)
	store, _ := NewStore(root)
	refs, _ := store.List()
	records, _, _ := store.Read(refs[0])
	first := MapSession(refs[0], records, MapOptions{})
	second := MapSession(refs[0], records, MapOptions{})
	if len(first) != len(second) {
		t.Fatalf("event counts differ: %d != %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Event.Event.ID != second[i].Event.Event.ID {
			t.Fatalf("event id %d changed: %s != %s", i, first[i].Event.Event.ID, second[i].Event.Event.ID)
		}
	}
}

func TestAdvanceCursorPartialDoesNotSkipSiblingEventsFromFailedLine(t *testing.T) {
	root := t.TempDir()
	_, _ = writeCopilotFixture(t, root)
	store, _ := NewStore(root)
	refs, _ := store.List()
	records, _, _ := store.Read(refs[0])
	mapped := MapSession(refs[0], records, MapOptions{})

	failedIdx := -1
	for i := 1; i < len(mapped); i++ {
		if mapped[i].SourceLine == mapped[i-1].SourceLine {
			failedIdx = i
			break
		}
	}
	if failedIdx < 0 {
		t.Fatal("fixture did not produce sibling events from one source line")
	}

	cursor := &Cursor{}
	advanceCursorPartial(cursor, mapped, failedIdx)
	if cursor.LastLine >= mapped[failedIdx].SourceLine {
		t.Fatalf("cursor advanced to line %d after failing sibling on line %d", cursor.LastLine, mapped[failedIdx].SourceLine)
	}
}

func TestMapSessionAppliesLiveContextChangesToLaterEvents(t *testing.T) {
	ref := SessionRef{ID: "session-1", Path: "/tmp/copilot/events.jsonl", ModTimeUnixMS: 1770000000000}
	records := []Record{
		{Line: 1, Type: "session.start", Time: float64(1770000000000), Data: map[string]interface{}{
			"sessionId":      "session-1",
			"copilotVersion": "1.0.0",
			"selectedModel":  "old-model",
			"context":        map[string]interface{}{"cwd": "/old"},
		}},
		{Line: 2, Type: "session.resume", Time: float64(1770000000100), Data: map[string]interface{}{
			"selectedModel": "new-model",
			"context":       map[string]interface{}{"cwd": "/new", "repository": "owner/repo", "branch": "feature"},
		}},
		{Line: 3, Type: "session.model_change", Time: float64(1770000000200), Data: map[string]interface{}{
			"newModel": "newer-model",
		}},
		{Line: 4, Type: "user.message", Time: float64(1770000000300), Data: map[string]interface{}{
			"content": "what changed?",
		}},
	}
	events := MapSession(ref, records, MapOptions{})
	var prompt schema.Event
	for _, item := range events {
		if item.Event.Event.Action == "prompt.submitted" {
			prompt = item.Event
			break
		}
	}
	if prompt.Session == nil || prompt.Session.WorkingDirectory != "/new" {
		t.Fatalf("prompt session context = %+v, want cwd /new", prompt.Session)
	}
	if prompt.Model != "newer-model" {
		t.Fatalf("prompt model = %q, want newer-model", prompt.Model)
	}
}

func TestStatusStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	_, path := writeCopilotFixture(t, root)
	statePath := filepath.Join(root, "state.json")
	if _, err := CollectOnce(CollectOptions{CopilotDir: root, StatePath: statePath, Print: true, Out: bytes.NewBuffer(nil)}); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Files) != 0 {
		t.Fatal("--print persisted state")
	}
	if _, err := CollectOnce(CollectOptions{CopilotDir: root, StatePath: statePath, Write: false}); err != nil {
		t.Fatal(err)
	}
	state, err = LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Files[path] == nil || state.Files[path].LastLine == 0 {
		data, _ := json.MarshalIndent(state, "", "  ")
		t.Fatalf("state did not record progress:\n%s", data)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
