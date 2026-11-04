package dshsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// v4Session builds a session record file in the shape DSH session format v4 writes
// (session.v4.jsonl.zstd): a header line, then {type, seq, time, data} events, with `surfaceOp` on
// the message events. Every value here is synthetic.
type v4Session struct {
	lines []string
	seq   int
	time  int64
}

func newV4Session(header map[string]interface{}) *v4Session {
	s := &v4Session{time: 1791000000000}
	full := map[string]interface{}{"type": "session", "version": 4, "createdAt": s.time, "delegationDepth": 0, "isSeeded": false}
	for k, v := range header {
		full[k] = v
	}
	s.lines = append(s.lines, mustJSON(full))
	return s
}

func (s *v4Session) event(kind string, data map[string]interface{}, surfaceOp interface{}) *v4Session {
	s.time += 1000
	line := map[string]interface{}{"type": kind, "seq": s.seq, "time": s.time, "data": data}
	if surfaceOp != nil {
		line["surfaceOp"] = surfaceOp
	}
	s.seq++
	s.lines = append(s.lines, mustJSON(line))
	return s
}

func (s *v4Session) prompt(kind, text string) *v4Session {
	source := map[string]interface{}{"kind": kind}
	return s.event("user/message", map[string]interface{}{
		"id": "m" + itoa(s.seq), "role": "user", "source": source,
		"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
	}, "append")
}

// call records the assistant message that requests a tool, then the tool/call event, with the
// arguments as the raw JSON string DSH stores.
func (s *v4Session) call(id, name string, args map[string]interface{}) *v4Session {
	raw := mustJSON(args)
	s.event("assistant/message", map[string]interface{}{
		"turn": 1, "step": 1,
		"message": map[string]interface{}{
			"id": "a" + id, "role": "assistant",
			"source":  map[string]interface{}{"kind": "model", "provider": "synthetic", "model": "model-a"},
			"content": []interface{}{map[string]interface{}{"type": "tool-call", "id": id, "name": name, "arguments": raw}},
		},
		"stream": []interface{}{},
		"usage":  map[string]interface{}{"inputTokens": 10, "outputTokens": 2, "totalTokens": 12},
	}, "append")
	return s.event("tool/call", map[string]interface{}{"turn": 1, "step": 1, "callId": id, "name": name, "arguments": raw}, nil)
}

func (s *v4Session) result(id, text string, isError bool, extra map[string]interface{}) *v4Session {
	data := map[string]interface{}{
		"turn": 1, "step": 1,
		"message": map[string]interface{}{
			"id": "r" + id, "role": "tool", "toolCallId": id, "isError": isError,
			"source":  map[string]interface{}{"kind": "tool", "callId": id},
			"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
		},
	}
	for k, v := range extra {
		data[k] = v
	}
	return s.event("tool/result", data, "append")
}

func (s *v4Session) records(t *testing.T) []Record {
	t.Helper()
	out := make([]Record, 0, len(s.lines))
	for i, line := range s.lines {
		out = append(out, mustRecord(t, i+1, line))
	}
	return out
}

func (s *v4Session) bytes() []byte { return []byte(strings.Join(s.lines, "\n") + "\n") }

func mustJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func itoa(n int) string { return strings.TrimSpace(mustJSON(n)) }

func mappedByCallID(mapped []MappedEvent) map[string][]schema.Event {
	out := map[string][]schema.Event{}
	for _, item := range mapped {
		ev := item.Event
		if ev.GenAI != nil && ev.GenAI.Tool != nil && ev.GenAI.Tool.Call != nil {
			out[ev.GenAI.Tool.Call.ID] = append(out[ev.GenAI.Tool.Call.ID], ev)
		}
	}
	return out
}

func countAction(mapped []MappedEvent, action string) int {
	n := 0
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			n++
		}
	}
	return n
}

func TestMapV4ToolCallsAndResults(t *testing.T) {
	s := newV4Session(map[string]interface{}{"id": "session-v4", "cwd": "/work/repo"}).
		event("request/header", map[string]interface{}{"reason": "initial", "header": map[string]interface{}{"config": map[string]interface{}{"provider": "synthetic", "model": "model-a"}}}, nil).
		prompt("user", "please fix the build").
		call("c-bash-fail", "bash", map[string]interface{}{"command": "make test", "description": "run tests"}).
		result("c-bash-fail", "2 failed\n[exit code: 2]", false, nil).
		call("c-bash-ok", "bash", map[string]interface{}{"command": "echo ok"}).
		result("c-bash-ok", "ok", false, map[string]interface{}{"meta": map[string]interface{}{"cwd": "/work/repo"}}).
		call("c-read", "read", map[string]interface{}{"file_path": "/work/repo/a.go", "offset": 1, "limit": 20}).
		result("c-read", "1\tpackage a", false, nil).
		call("c-grep", "grep", map[string]interface{}{"pattern": "TODO", "path": "/work/repo"}).
		result("c-grep", "a.go:3", false, nil).
		call("c-glob", "glob", map[string]interface{}{"pattern": "**/*.go", "path": "/work/repo"}).
		result("c-glob", "a.go", false, nil).
		call("c-edit", "edit", map[string]interface{}{"file_path": "/work/repo/a.go", "old_string": "old()", "new_string": "new()"}).
		result("c-edit", "edited", false, nil).
		call("c-write", "write", map[string]interface{}{"file_path": "/work/repo/b.go", "content": "package b\n"}).
		result("c-write", "written", false, nil).
		call("c-term", "terminal_send", map[string]interface{}{"terminal_id": "t1", "text": "go vet ./...", "submit": true}).
		result("c-term", "sent", false, nil).
		call("c-mcp", "mcp__synthetic-server__lookup", map[string]interface{}{"query": "x"}).
		result("c-mcp", "found", false, nil).
		call("c-fail", "read", map[string]interface{}{"file_path": "/work/repo/missing.go"}).
		result("c-fail", "no such file", true, map[string]interface{}{"error": map[string]interface{}{"name": "FsError", "code": "FS_NOT_FOUND"}})

	mapped := MapSession(SessionRef{ID: "session-v4", Path: "/store/session-v4/session.v4.jsonl.zstd"}, s.records(t), MapOptions{})
	byCall := mappedByCallID(mapped)

	want := map[string]string{
		"c-bash-fail": "command.executed",
		"c-bash-ok":   "command.executed",
		"c-read":      "file.read",
		"c-grep":      "file.read",
		"c-glob":      "file.read",
		"c-edit":      "file.modified",
		"c-write":     "file.modified",
		"c-term":      "command.executed",
		"c-mcp":       "mcp.tool_invoked",
		"c-fail":      "tool.failed",
	}
	for id, action := range want {
		events := byCall[id]
		if len(events) != 2 || events[0].Event.Action != "tool.invoked" || events[1].Event.Action != action {
			got := []string{}
			for _, ev := range events {
				got = append(got, ev.Event.Action)
			}
			t.Fatalf("call %s mapped to %v, want [tool.invoked %s]", id, got, action)
		}
		if args, _ := events[0].GenAI.Tool.Call.Arguments.(map[string]interface{}); len(args) == 0 || args["value"] != nil {
			t.Fatalf("call %s: string-encoded arguments were not decoded", id)
		}
	}

	failed := byCall["c-bash-fail"][1]
	if failed.Command == nil || failed.Command.Command != "make test" || failed.Command.ExitCode == nil || *failed.Command.ExitCode != 2 {
		t.Fatalf("non-zero bash exit = %+v, want command make test with exit code 2", failed.Command)
	}
	if !strings.Contains(failed.Command.Output, "2 failed") || failed.Error != nil {
		t.Fatalf("non-zero exit must be a command with output, not a tool failure: %+v / %+v", failed.Command, failed.Error)
	}
	if ok := byCall["c-bash-ok"][1]; ok.Command.ExitCode != nil {
		t.Fatalf("exit code invented for an output with no marker: %v", *ok.Command.ExitCode)
	}
	if term := byCall["c-term"][1]; term.Command == nil || term.Command.Command != "go vet ./..." {
		t.Fatalf("terminal_send command = %+v, want its text", term.Command)
	}
	if read := byCall["c-read"][1]; read.File == nil || read.File.Path != "/work/repo/a.go" || read.File.Operation != "read" {
		t.Fatalf("read file = %+v", read.File)
	}
	edit := byCall["c-edit"][1]
	if edit.File == nil || edit.File.Operation != "modify" || !strings.Contains(edit.File.Diff, "-old()") || !strings.Contains(edit.File.Diff, "+new()") {
		t.Fatalf("edit file = %+v", edit.File)
	}
	if write := byCall["c-write"][1]; write.File == nil || write.File.Operation != "create" || !strings.Contains(write.File.Diff, "+package b") {
		t.Fatalf("write file = %+v", write.File)
	}
	if mcp := byCall["c-mcp"][1]; mcp.MCP == nil || mcp.MCP.Server != "synthetic-server" || mcp.MCP.Tool != "lookup" {
		t.Fatalf("mcp = %+v", mcp.MCP)
	}
	if fail := byCall["c-fail"][1]; fail.Error == nil || fail.Error.Type != "tool_execution_failed" {
		t.Fatalf("failed tool error = %+v", fail.Error)
	}

	usage := findAction(t, mapped, "token.usage")
	if usage.Model != "model-a" || usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 10 {
		t.Fatalf("usage = %+v model %q", usage.GenAI.Usage, usage.Model)
	}
}

func TestMapV4RunCodeNestedDispatches(t *testing.T) {
	s := newV4Session(map[string]interface{}{"id": "session-ptc", "cwd": "/work/repo"}).
		prompt("user", "count the files").
		call("c-run", "run_code", map[string]interface{}{"code": "await tools.bash({command: 'ls'})", "description": "list"}).
		event("tool/ptc-dispatch-start", map[string]interface{}{"name": "bash", "parentCallId": "c-run", "rootCallId": "c-run", "subCallId": "c-run.1", "arguments": map[string]interface{}{"command": "ls -1", "description": "list"}}, nil).
		event("tool/ptc-dispatch", map[string]interface{}{"name": "bash", "parentCallId": "c-run", "rootCallId": "c-run", "subCallId": "c-run.1", "arguments": map[string]interface{}{"command": "ls -1", "description": "list"}, "content": []interface{}{map[string]interface{}{"type": "text", "text": "a\nb\n[exit code: 1]"}}, "isError": false}, nil).
		result("c-run", "2 files", false, nil)

	byCall := mappedByCallID(MapSession(SessionRef{ID: "session-ptc"}, s.records(t), MapOptions{}))
	run := byCall["c-run"]
	if len(run) != 2 || run[1].Event.Action != "tool.completed" {
		t.Fatalf("run_code = %v, want tool.invoked then tool.completed", run)
	}
	if run[1].Command != nil {
		t.Fatalf("run_code recorded as a command: %+v", run[1].Command)
	}
	nested := byCall["c-run.1"]
	if len(nested) != 2 || nested[1].Event.Action != "command.executed" || nested[1].Command.Command != "ls -1" {
		t.Fatalf("nested dispatch = %v, want a command.executed for ls -1", nested)
	}
	if nested[1].Command.ExitCode == nil || *nested[1].Command.ExitCode != 1 {
		t.Fatalf("nested exit code = %v, want 1", nested[1].Command.ExitCode)
	}
}

func TestMapV4CountsOnlyPromptsAPersonSent(t *testing.T) {
	s := newV4Session(map[string]interface{}{"id": "session-prompts", "cwd": "/work/repo"}).
		prompt("agent-instructions", "synthetic instructions").
		prompt("time-context", "synthetic clock").
		prompt("user", "first real prompt").
		prompt("agent-message", "relayed from another agent").
		prompt("subagent-settled", "a subagent finished").
		prompt("user", "second real prompt")
	ref := SessionRef{ID: "session-prompts"}
	mapped := MapSession(ref, s.records(t), MapOptions{})
	if n := countAction(mapped, "prompt.submitted"); n != 2 {
		t.Fatalf("prompts = %d, want 2 (injected context is not a prompt)", n)
	}
	if got := findAction(t, mapped, "prompt.submitted").Prompt.Text; got != "first real prompt" {
		t.Fatalf("first prompt = %q", got)
	}

	root := t.TempDir()
	writeBytes(t, filepath.Join(root, "sessions", "--work-repo--", "session-prompts", "session.v4.jsonl"), s.bytes())
	store, _ := NewStore(root)
	refs, err := store.List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs = %v, %v", refs, err)
	}
	if refs[0].Meta.FirstPrompt != "first real prompt" {
		t.Fatalf("FirstPrompt = %q, want the first prompt a person sent", refs[0].Meta.FirstPrompt)
	}
}

// The v4 session header keeps its fields at the top level of the line, not under `data`.
func TestV4SessionHeaderFields(t *testing.T) {
	s := newV4Session(map[string]interface{}{"id": "session-sub", "cwd": "/work/repo", "parentSession": "session-parent", "origin": "subagent", "delegationDepth": 1}).
		prompt("user", "delegated task")
	root := t.TempDir()
	writeBytes(t, filepath.Join(root, "sessions", "--work-repo--", "session-sub", "session.v4.jsonl.zstd"), zstdFrame(t, s.bytes()))
	store, _ := NewStore(root)
	refs, err := store.List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs = %v, %v", refs, err)
	}
	meta := refs[0].Meta
	if meta == nil || meta.ID != "session-sub" || meta.CWD != "/work/repo" || meta.ParentSessionID != "session-parent" || meta.Origin != "subagent" {
		t.Fatalf("meta = %+v", meta)
	}
	records, _, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	started := findAction(t, MapSession(refs[0], records, MapOptions{}), "session.started")
	if started.Session == nil || started.Session.WorkingDirectory != "/work/repo" {
		t.Fatalf("session = %+v, want the header's cwd", started.Session)
	}
	if got := filterRefs(refs, CollectOptions{Workspace: "/work/repo"}); len(got) != 1 {
		t.Fatalf("--workspace matched %d sessions, want 1", len(got))
	}
}

func TestMapV4SkipsCompactionRewrites(t *testing.T) {
	s := newV4Session(map[string]interface{}{"id": "session-compact", "cwd": "/work/repo"}).
		prompt("user", "go").
		call("c1", "bash", map[string]interface{}{"command": "echo one"}).
		result("c1", "one", false, nil).
		event("compaction/prune", map[string]interface{}{"shadowedRange": map[string]interface{}{"start": 3, "end": 3}, "shadowedSeqs": []int{3}, "shadowedTokenCount": 5}, nil).
		event("tool/result", map[string]interface{}{"turn": 1, "step": 1, "message": map[string]interface{}{
			"id": "pruned", "role": "tool", "toolCallId": "c1", "isError": false,
			"source":  map[string]interface{}{"kind": "tool", "callId": "c1"},
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "[pruned]"}},
		}}, map[string]interface{}{"op": "replace", "startSeq": 3, "endSeq": 3}).
		event("compaction/summary", map[string]interface{}{"compactionId": "k1", "model": "model-small", "provider": "synthetic",
			"usage": map[string]interface{}{"inputTokens": 400, "outputTokens": 40, "totalTokens": 440}}, nil).
		event("user/message", map[string]interface{}{"id": "ck", "role": "user", "source": map[string]interface{}{"kind": "compact-checkpoint"},
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "summary"}}}, map[string]interface{}{"op": "replace", "startSeq": 0, "endSeq": 4})

	mapped := MapSession(SessionRef{ID: "session-compact"}, s.records(t), MapOptions{})
	if n := countAction(mapped, "command.executed"); n != 1 {
		t.Fatalf("command.executed = %d, want 1: a pruned result is the same call again", n)
	}
	if n := countAction(mapped, "prompt.submitted"); n != 1 {
		t.Fatalf("prompts = %d, want 1", n)
	}
	var compaction *schema.Event
	for _, item := range mapped {
		if item.Event.Event.Action == "token.usage" && item.Event.Model == "model-small" {
			ev := item.Event
			compaction = &ev
		}
	}
	if compaction == nil || *compaction.GenAI.Usage.InputTokens != 400 || *compaction.GenAI.Usage.OutputTokens != 40 {
		t.Fatalf("compaction usage = %+v", compaction)
	}
}

func TestMapV4ForkDoesNotReemitInheritedHistory(t *testing.T) {
	s := newV4Session(map[string]interface{}{"id": "session-child", "cwd": "/work/repo", "parentSession": "session-parent", "isSeeded": true, "origin": "subagent", "delegationDepth": 1}).
		// The parent's history, copied verbatim into the fork's seed.
		prompt("user", "parent prompt").
		call("p1", "bash", map[string]interface{}{"command": "echo parent"}).
		result("p1", "parent", false, nil).
		event("session/end-seed", map[string]interface{}{"inherited": true}, nil).
		// The child's own work.
		prompt("user", "child prompt").
		call("k1", "bash", map[string]interface{}{"command": "echo child"}).
		result("k1", "child", false, nil)

	mapped := MapSession(SessionRef{ID: "session-child"}, s.records(t), MapOptions{})
	if n := countAction(mapped, "session.started"); n != 1 {
		t.Fatalf("session.started = %d, want 1", n)
	}
	if got := findAction(t, mapped, "prompt.submitted").Prompt.Text; got != "child prompt" || countAction(mapped, "prompt.submitted") != 1 {
		t.Fatalf("prompts = %d first %q, want only the child's", countAction(mapped, "prompt.submitted"), got)
	}
	if cmd := findAction(t, mapped, "command.executed"); cmd.Command.Command != "echo child" || countAction(mapped, "command.executed") != 1 {
		t.Fatalf("commands = %d, want only the child's", countAction(mapped, "command.executed"))
	}
	if n := countAction(mapped, "token.usage"); n != 1 {
		t.Fatalf("token.usage = %d, want 1: inherited usage was already counted for the parent", n)
	}
	for _, item := range mapped {
		if got := item.Event.Raw["dsh"].(map[string]interface{})["parent_session"]; got != "session-parent" {
			t.Fatalf("raw.dsh.parent_session = %v on %s", got, item.Event.Event.Action)
		}
	}

	// An untagged end-seed marker separates restored settings from live work and cuts nothing.
	plain := newV4Session(map[string]interface{}{"id": "session-restored", "cwd": "/work/repo", "isSeeded": true}).
		event("permission/preset", map[string]interface{}{"preset": "workspace-write"}, nil).
		event("session/end-seed", map[string]interface{}{}, nil).
		prompt("user", "hello")
	if n := countAction(MapSession(SessionRef{ID: "session-restored"}, plain.records(t), MapOptions{}), "prompt.submitted"); n != 1 {
		t.Fatalf("prompts after an untagged seed marker = %d, want 1", n)
	}
}

func TestCollectV4StoreIsIdempotentAndResumesAPartialFrame(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "--work-repo--", "session-z")
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	base := newV4Session(map[string]interface{}{"id": "session-z", "cwd": "/work/repo"}).
		prompt("user", "go").
		call("z1", "bash", map[string]interface{}{"command": "echo z"}).
		result("z1", "z", false, nil)
	first := zstdFrame(t, base.bytes())
	next := newV4Session(nil)
	next.seq, next.time = base.seq, base.time
	next.lines = nil
	next.call("z2", "write", map[string]interface{}{"file_path": "/work/repo/z.txt", "content": "z\n"}).result("z2", "ok", false, nil)
	second := zstdFrame(t, next.bytes())
	// DSH is still writing the second frame.
	writeBytes(t, path, append(append([]byte{}, first...), second[:len(second)/2]...))

	state := filepath.Join(root, "state.json")
	log := filepath.Join(root, "runtime.jsonl")
	opts := CollectOptions{DSHHome: root, StatePath: state, Write: true, LogPath: log, UserMode: true}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PartialSessions != 1 || summary.EventsEmitted == 0 {
		t.Fatalf("first sweep = %+v", summary)
	}
	firstEvents := summary.EventsEmitted

	again, err := CollectOnce(opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.EventsEmitted != 0 {
		t.Fatalf("re-sweep of an unchanged partial file emitted %d events", again.EventsEmitted)
	}

	writeBytes(t, path, append(append([]byte{}, first...), second...))
	done, err := CollectOnce(opts)
	if err != nil {
		t.Fatal(err)
	}
	if done.EventsEmitted == 0 || done.PartialSessions != 0 {
		t.Fatalf("completed frame sweep = %+v", done)
	}
	if idle, _ := CollectOnce(opts); idle.EventsEmitted != 0 {
		t.Fatalf("idle sweep emitted %d events", idle.EventsEmitted)
	}

	ids := map[string]bool{}
	actionsSeen := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(readFile(t, log))), "\n") {
		var ev schema.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ids[ev.Event.ID] {
			t.Fatalf("duplicate event id %s (%s)", ev.Event.ID, ev.Event.Action)
		}
		ids[ev.Event.ID] = true
		actionsSeen[ev.Event.Action]++
	}
	if len(ids) != firstEvents+done.EventsEmitted || actionsSeen["command.executed"] != 1 || actionsSeen["file.modified"] != 1 {
		t.Fatalf("log = %v (%d ids)", actionsSeen, len(ids))
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
