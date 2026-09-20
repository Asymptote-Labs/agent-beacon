package openclawsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestStoreReadsIndexedAndFlatScannedSessions(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "agents", "main", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "indexed.jsonl"), openClawLines("indexed", "/repo/indexed")...)
	writeFile(t, filepath.Join(sessions, "loose.jsonl"), openClawLines("loose", "/repo/loose")...)
	writeFile(t, filepath.Join(sessions, "sessions.json"), `{
  "indexed": {
    "sessionId": "indexed",
    "sessionFile": "indexed.jsonl",
    "updatedAt": "2026-09-19T23:00:00Z",
    "workspaceDir": "/repo/indexed"
  }
}`)

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %d, want 2: %+v", len(refs), refs)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		seen[ref.ID] = true
		if ref.Profile != "main" {
			t.Errorf("profile = %q, want main", ref.Profile)
		}
	}
	if !seen["indexed"] || !seen["loose"] {
		t.Fatalf("missing refs: %+v", seen)
	}
}

func TestCollectOnceWritesOpenClawSessionEventsAndCursor(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, ".openclaw", "agents", "main", "sessions")
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "s1.jsonl"), openClawLines("s1", "/repo")...)

	opts := CollectOptions{
		OpenClawDir: filepath.Join(dir, ".openclaw"),
		StatePath:   statePath,
		Write:       true,
		LogPath:     logPath,
		UserMode:    true,
	}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.Traces != 1 || summary.TracesChanged != 1 || summary.EventsEmitted == 0 {
		t.Fatalf("summary = %+v", summary)
	}
	events := readEvents(t, logPath)
	if len(events) != summary.EventsEmitted {
		t.Fatalf("events = %d, summary emitted %d", len(events), summary.EventsEmitted)
	}
	var sawPrompt, sawCommand, sawUsage bool
	for _, event := range events {
		if event.Harness.Name != Harness || event.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Fatalf("bad provenance on %s: %+v", event.Event.Action, event.Harness)
		}
		if event.Event.ID == "" {
			t.Fatalf("%s has no deterministic event id", event.Event.Action)
		}
		switch event.Event.Action {
		case "prompt.submitted":
			sawPrompt = event.Prompt != nil && strings.Contains(event.Prompt.Text, "summarize")
		case "command.executed":
			sawCommand = event.Command != nil && event.Command.Command == "echo hi" && event.Command.ExitCode != nil && *event.Command.ExitCode == 0
		case "token.usage":
			sawUsage = event.GenAI != nil && event.GenAI.Usage != nil && event.GenAI.Usage.InputTokens != nil && *event.GenAI.Usage.InputTokens == 12
		}
	}
	if !sawPrompt || !sawCommand || !sawUsage {
		t.Fatalf("saw prompt=%t command=%t usage=%t in %+v", sawPrompt, sawCommand, sawUsage, events)
	}

	second, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	if second.EventsEmitted != 0 {
		t.Fatalf("second emitted %d events, want 0", second.EventsEmitted)
	}
}

func openClawLines(id, cwd string) []string {
	return []string{
		`{"type":"session","id":"` + id + `","timestamp":"2026-09-19T23:00:00Z","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"u1","timestamp":"2026-09-19T23:00:01Z","message":{"role":"user","content":[{"type":"text","text":"summarize the repo"}]}}`,
		`{"type":"message","id":"a1","timestamp":"2026-09-19T23:00:02Z","message":{"role":"assistant","modelId":"openai/gpt-5","content":[{"type":"tool_call","id":"call_1","tool_name":"exec","input":{"command":"echo hi"}},{"type":"tool_result","call_id":"call_1","tool_name":"exec","output":{"output":"hi\n","exitCode":0}},{"type":"text","text":"Done."}],"usage":{"input":12,"output":5,"cacheRead":2}}}`,
	}
}

func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := strings.Join(lines, "\n")
	if len(lines) > 1 {
		data += "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readEvents(t *testing.T, path string) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event: %v\n%s", err, line)
		}
		events = append(events, event)
	}
	return events
}
