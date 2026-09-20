package groksession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	events := MapSession(data, MapOptions{})
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
	// A turn is not a session. Grok keeps writing to the same session after turn_ended, so the
	// only session-lifecycle boundary this store can honestly report is the start.
	if _, ok := byAction["session.ended"]; ok {
		t.Fatal("turn_ended produced a session.ended event")
	}
	// Grok numbers turns from zero, so the first turn's number has to survive the mapping. Dropping
	// it as a falsy value would report the one turn every session has as having no number.
	turnEnd := rawGrokDetail(t, events, "session.context", "turn_ended")
	if turnEnd == nil {
		t.Fatal("no session.context event for turn_ended")
	}
	if got, ok := turnEnd["turn_number"]; !ok || got != float64(0) {
		t.Fatalf("turn_number = %v (present=%t), want 0", got, ok)
	}
	if _, ok := byAction["session.started"]; !ok {
		t.Fatal("no session.started event")
	}
}

// A Grok session directory is appended to while the session runs, so the same session is read
// again on the next sweep. Only the records that were added may be emitted: re-mapping the whole
// session would append a duplicate copy of everything already in the runtime log.
func TestCollectOnceEmitsOnlyNewRecordsWhenSessionGrows(t *testing.T) {
	root := t.TempDir()
	sessionDir := writeFixtureSession(t, root, "/tmp/grok project", "session-1")
	statePath := filepath.Join(t.TempDir(), "state.json")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")

	opts := CollectOptions{SessionsDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
	first, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("first CollectOnce returned error: %v", err)
	}
	if first.EventsEmitted == 0 {
		t.Fatal("first CollectOnce emitted no events")
	}

	appendLine(t, filepath.Join(sessionDir, "chat_history.jsonl"),
		`{"type":"user","content":[{"type":"text","text":"second turn"}]}`)
	appendLine(t, filepath.Join(sessionDir, "events.jsonl"),
		`{"ts":"2026-05-21T16:52:10.000Z","type":"turn_ended","outcome":"success","turn_number":1}`)
	touchNewer(t, sessionDir)

	second, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	if second.EventsEmitted != 2 {
		t.Fatalf("second sweep emitted %d events, want the 2 appended records", second.EventsEmitted)
	}
	if lines := countLines(t, logPath); lines != first.EventsEmitted+2 {
		t.Fatalf("runtime lines = %d, want %d", lines, first.EventsEmitted+2)
	}
	actions := logActions(t, logPath)
	if actions["session.started"] != 1 {
		t.Fatalf("session.started written %d times, want 1", actions["session.started"])
	}
	if actions["prompt.submitted"] != 2 {
		t.Fatalf("prompt.submitted written %d times, want 2", actions["prompt.submitted"])
	}
	if actions["command.executed"] != 1 {
		t.Fatalf("command.executed written %d times, want 1 (the first sweep's only command)", actions["command.executed"])
	}

	third, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("third CollectOnce returned error: %v", err)
	}
	if third.EventsEmitted != 0 {
		t.Fatalf("third sweep emitted %d events, want none", third.EventsEmitted)
	}
}

// A sweep can catch the tail of a live session mid-write: the last line is half flushed, counts as
// malformed, and completes before the next sweep. Numbering records by their position among the
// lines that parsed would renumber every later line once it landed and the cursor would start
// skipping records it had never read, so records are numbered by physical line instead.
func TestCollectOnceCollectsALineThatWasTornOnTheFirstSweep(t *testing.T) {
	root := t.TempDir()
	sessionDir := writeFixtureSession(t, root, "/tmp/grok project", "session-1")
	chatPath := filepath.Join(sessionDir, "chat_history.jsonl")
	statePath := filepath.Join(t.TempDir(), "state.json")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")

	// Grok is midway through flushing the next chat line when the sweep reads the file.
	appendLine(t, chatPath, `{"type":"user","content":[{"type":"te`)

	opts := CollectOptions{SessionsDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
	first, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("first CollectOnce returned error: %v", err)
	}
	if first.MalformedLines != 1 {
		t.Fatalf("MalformedLines = %d, want 1", first.MalformedLines)
	}

	// The write lands, and the session continues past it.
	rewriteLine(t, chatPath, 5, `{"type":"user","content":[{"type":"text","text":"the completed line"}]}`)
	appendLine(t, chatPath, `{"type":"user","content":[{"type":"text","text":"and the next one"}]}`)
	touchNewer(t, sessionDir)

	second, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	if second.MalformedLines != 0 {
		t.Fatalf("MalformedLines = %d on the second sweep, want 0", second.MalformedLines)
	}
	if second.EventsEmitted != 2 {
		t.Fatalf("second sweep emitted %d events, want the completed line and the one after it", second.EventsEmitted)
	}
	if actions := logActions(t, logPath); actions["prompt.submitted"] != 3 {
		t.Fatalf("prompt.submitted written %d times, want 3", actions["prompt.submitted"])
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
			`{"ts":"2026-05-21T16:50:25.814Z","type":"turn_ended","outcome":"success","turn_number":0}`+"\n")
	writeFile(t, filepath.Join(sessionDir, "chat_history.jsonl"),
		`{"type":"user","content":[{"type":"text","text":"run ls token=secret"}]}`+"\n"+
			`{"type":"assistant","content":"","reasoning":{"text":"Need to inspect files"},"tool_calls":[{"id":"call-abc","name":"run_terminal_command","arguments":"{\"command\":\"ls -la\",\"description\":\"list files\"}"}],"model_id":"grok-build"}`+"\n"+
			`{"type":"tool_result","tool_call_id":"call-abc","content":"fallback output"}`+"\n"+
			`{"type":"assistant","content":"done","model_id":"grok-build"}`+"\n")
	writeFile(t, filepath.Join(sessionDir, "terminal", "call-abc-1.log"), "total 0\n")
	return sessionDir
}

// rawGrokDetail pulls the grok detail map off the first event with the given action whose
// "lifecycle" field matches, going through JSON so it reads the event exactly as the log does.
func rawGrokDetail(t *testing.T, events []MappedEvent, action, lifecycle string) map[string]interface{} {
	t.Helper()
	for _, item := range events {
		if item.Event.Event.Action != action {
			continue
		}
		data, err := json.Marshal(item.Event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		var record struct {
			Raw struct {
				Grok map[string]interface{} `json:"grok"`
			} `json:"raw"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if record.Raw.Grok["lifecycle"] == lifecycle {
			return record.Raw.Grok
		}
	}
	return nil
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s for append: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("append to %s: %v", path, err)
	}
}

func rewriteLine(t *testing.T, path string, lineNo int, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if lineNo < 1 || lineNo > len(lines) {
		t.Fatalf("line %d out of range for %s (%d lines)", lineNo, path, len(lines))
	}
	lines[lineNo-1] = content
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
}

// touchNewer moves the session directory's modification time forward. The collector uses it as a
// cheap "could this have changed" filter, and a test that writes twice inside one filesystem
// timestamp tick would otherwise be skipped for reasons that have nothing to do with the cursor.
func touchNewer(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	next := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(dir, next, next); err != nil {
		t.Fatalf("chtimes %s: %v", dir, err)
	}
}

func logActions(t *testing.T, path string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record struct {
			Event struct {
				Action string `json:"action"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("unmarshal runtime line: %v", err)
		}
		counts[record.Event.Action]++
	}
	return counts
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
