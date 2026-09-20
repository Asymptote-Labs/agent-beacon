package groksession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreListsAndMapsGrokSession(t *testing.T) {
	root := t.TempDir()
	sessionDir := writeFixtureSession(t, root, "/tmp/grok project", "session-1")

	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	if refs[0].Workspace != "/tmp/grok project" {
		t.Fatalf("workspace = %q, want decoded path", refs[0].Workspace)
	}
	if refs[0].SourcePath != sessionDir {
		t.Fatalf("source path = %q, want %q", refs[0].SourcePath, sessionDir)
	}

	data, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if stats.Malformed != 0 {
		t.Fatalf("Malformed = %d, want 0", stats.Malformed)
	}
	events := MapSession(data)
	if len(events) == 0 {
		t.Fatal("MapSession returned no events")
	}
	byAction := map[string]jsonEvent{}
	for _, item := range events {
		byAction[item.Event.Event.Action] = jsonEventFromSchema(t, item.Event)
	}
	prompt := byAction["prompt.submitted"]
	if prompt.Harness.CollectionMethod != "poll" {
		t.Fatalf("collection method = %q, want poll", prompt.Harness.CollectionMethod)
	}
	if prompt.Prompt.Text != "run ls token=secret" {
		t.Fatalf("prompt = %q", prompt.Prompt.Text)
	}
	cmd := byAction["command.executed"]
	if cmd.Command.Command != "ls -la" {
		t.Fatalf("command = %q, want ls -la", cmd.Command.Command)
	}
	if cmd.Command.Output != "total 0\n" {
		t.Fatalf("command output = %q, want terminal log", cmd.Command.Output)
	}
	if byAction["approval.allowed"].Approval.Decision != "allowed" {
		t.Fatalf("approval event = %#v", byAction["approval.allowed"].Approval)
	}
}

func TestCollectOnceIsIdempotentAndPrintDoesNotAdvanceState(t *testing.T) {
	root := t.TempDir()
	writeFixtureSession(t, root, "/tmp/grok project", "session-1")
	statePath := filepath.Join(t.TempDir(), "state.json")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")

	var printed bytes.Buffer
	printSummary, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &printed})
	if err != nil {
		t.Fatalf("print CollectOnce returned error: %v", err)
	}
	if printSummary.EventsEmitted == 0 || printed.Len() == 0 {
		t.Fatalf("print emitted nothing: summary=%+v output=%q", printSummary, printed.String())
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("--print advanced state or unexpected stat error: %v", err)
	}

	first, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true})
	if err != nil {
		t.Fatalf("CollectOnce returned error: %v", err)
	}
	if first.EventsEmitted == 0 {
		t.Fatal("first CollectOnce emitted no events")
	}
	second, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true})
	if err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	if second.EventsEmitted != 0 || second.SessionsChanged != 0 {
		t.Fatalf("second summary = %+v, want no new work", second)
	}
	if lines := countLines(t, logPath); lines != first.EventsEmitted {
		t.Fatalf("runtime lines = %d, want %d", lines, first.EventsEmitted)
	}
}

func writeFixtureSession(t *testing.T, root, workspace, sessionID string) string {
	t.Helper()
	sessionDir := filepath.Join(root, urlPathEscape(workspace), sessionID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "terminal"), 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	writeFile(t, filepath.Join(sessionDir, "summary.json"), `{
  "created_at":"2026-05-21T16:49:22.269243Z",
  "updated_at":"2026-05-21T16:50:25.853796Z",
  "generated_title":"Fixture session",
  "current_model_id":"grok-build",
  "num_messages":6,
  "num_chat_messages":4
}`)
	writeFile(t, filepath.Join(sessionDir, "prompt_context.json"), `{"working_directory":"`+workspace+`","os_name":"macos","shell_path":"/bin/bash"}`)
	writeFile(t, filepath.Join(sessionDir, "events.jsonl"),
		`{"ts":"2026-05-21T16:49:25.601Z","type":"turn_started","session_id":"`+sessionID+`","turn_number":0,"model_id":"grok-build"}`+"\n"+
			`{"ts":"2026-05-21T16:49:27.545Z","type":"permission_requested","tool_name":"run_terminal_command"}`+"\n"+
			`{"ts":"2026-05-21T16:49:27.545Z","type":"permission_resolved","tool_name":"run_terminal_command","decision":"allow","wait_ms":0}`+"\n"+
			`{"ts":"2026-05-21T16:49:27.552Z","type":"tool_completed","tool_name":"run_terminal_command","duration_ms":6,"outcome":"success"}`+"\n"+
			`{"ts":"2026-05-21T16:50:25.814Z","type":"turn_ended","outcome":"success"}`+"\n")
	writeFile(t, filepath.Join(sessionDir, "chat_history.jsonl"),
		`{"type":"user","content":[{"type":"text","text":"run ls token=secret"}]}`+"\n"+
			`{"type":"assistant","content":"","reasoning":{"text":"Need to inspect files"},"tool_calls":[{"id":"call-abc","name":"run_terminal_command","arguments":"{\"command\":\"ls -la\",\"description\":\"list files\"}"}],"model_id":"grok-build"}`+"\n"+
			`{"type":"tool_result","tool_call_id":"call-abc","content":"fallback output"}`+"\n"+
			`{"type":"assistant","content":"done","model_id":"grok-build"}`+"\n")
	writeFile(t, filepath.Join(sessionDir, "terminal", "call-abc-1.log"), "total 0\n")
	return sessionDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	n := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		n++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return n
}

type jsonEvent struct {
	Harness struct {
		CollectionMethod string `json:"collection_method"`
	} `json:"harness"`
	Prompt struct {
		Text string `json:"text"`
	} `json:"prompt"`
	Command struct {
		Command string `json:"command"`
		Output  string `json:"output"`
	} `json:"command"`
	Approval struct {
		Decision string `json:"decision"`
	} `json:"approval"`
}

func jsonEventFromSchema(t *testing.T, event interface{}) jsonEvent {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var out jsonEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	return out
}

func urlPathEscape(path string) string {
	replacer := strings.NewReplacer("/", "%2F", " ", "%20")
	return replacer.Replace(path)
}
