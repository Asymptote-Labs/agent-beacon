package claudesession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestListFindsClaudeSessionsAndSubagents(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-tmp-repo")
	mainPath := filepath.Join(project, "sess-1.jsonl")
	writeFile(t, mainPath, userLine("sess-1", "u1", "ship it")+"\n")
	writeFile(t, filepath.Join(project, "sessions-index.json"), `{"version":1,"entries":[{"sessionId":"sess-1","fullPath":`+quote(mainPath)+`,"projectPath":"/tmp/repo","gitBranch":"main"}]}`)

	subPath := filepath.Join(project, "sess-1", "subagents", "agent-a.jsonl")
	writeFile(t, subPath, userLine("sess-1", "su1", "inspect")+"\n")
	writeFile(t, strings.TrimSuffix(subPath, ".jsonl")+".meta.json", `{"agentType":"Explore","description":"inspect files","toolUseId":"toolu_sub"}`)

	store := &Store{ProjectsDir: root}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("List returned %d refs, want 2: %+v", len(refs), refs)
	}
	if refs[0].ProjectPath != "/tmp/repo" {
		t.Fatalf("project path = %q, want index value", refs[0].ProjectPath)
	}
	if !refs[1].IsSidechain || refs[1].ParentSessionID != "sess-1" || refs[1].Meta == nil || refs[1].Meta.AgentType != "Explore" {
		t.Fatalf("subagent ref not populated: %+v", refs[1])
	}
}

func TestMapClaudeTranscriptProducesEndpointEvents(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	records := decodeFixture(t, []string{
		userLine("sess-1", "u1", "add a health endpoint"),
		assistantLine("sess-1", "a1"),
		toolResultLine("sess-1", "r1", "toolu_bash", "ok", false),
	})

	mapped := MapSession(ref, records, MapOptions{})
	got := actions(mapped)
	want := []string{"session.started", "prompt.submitted", "tool.invoked", "agent.message", "token.usage", "command.executed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for _, item := range mapped {
		ev := item.Event
		if ev.Harness.Name != Harness || ev.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Fatalf("%s harness = %+v, want Claude poll", ev.Event.Action, ev.Harness)
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
	if usage.GenAI == nil || usage.GenAI.Usage == nil || usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 10 {
		t.Fatalf("usage = %+v, want input tokens", usage.GenAI)
	}
}

func TestCollectOnceUsesCursorAndPrintDoesNotAdvance(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	sessionPath := filepath.Join(projects, "-tmp-repo", "sess-1.jsonl")
	writeFile(t, sessionPath, userLine("sess-1", "u1", "first")+"\n")
	statePath := filepath.Join(dir, "state", "claude.json")
	logPath := filepath.Join(dir, "runtime.jsonl")

	opts := CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
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

	writeFile(t, sessionPath, userLine("sess-1", "u1", "first")+"\n"+userLine("sess-1", "u2", "second")+"\n")
	var printed strings.Builder
	printSummary, err := CollectOnce(CollectOptions{ProjectsDir: projects, Print: true, Out: &printed})
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

func userLine(sessionID, uuid, text string) string {
	return `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":` + quote(text) + `},"uuid":` + quote(uuid) + `,"timestamp":"2026-09-19T22:00:00.000Z","cwd":"/tmp/repo","sessionId":` + quote(sessionID) + `,"version":"2.1.154","gitBranch":"main"}`
}

func assistantLine(sessionID, uuid string) string {
	return `{"parentUuid":"u1","isSidechain":false,"type":"assistant","message":{"model":"claude-sonnet-4-5","id":"msg_1","role":"assistant","content":[{"type":"text","text":"I'll run tests."},{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"npm test"}}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2}},"uuid":` + quote(uuid) + `,"timestamp":"2026-09-19T22:00:01.000Z","cwd":"/tmp/repo","sessionId":` + quote(sessionID) + `,"version":"2.1.154","gitBranch":"main"}`
}

func toolResultLine(sessionID, uuid, toolUseID, output string, isError bool) string {
	return `{"parentUuid":"a1","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":` + quote(toolUseID) + `,"content":` + quote(output) + `,"is_error":` + boolString(isError) + `}]},"uuid":` + quote(uuid) + `,"timestamp":"2026-09-19T22:00:02.000Z","cwd":"/tmp/repo","sessionId":` + quote(sessionID) + `,"version":"2.1.154","gitBranch":"main"}`
}

func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
