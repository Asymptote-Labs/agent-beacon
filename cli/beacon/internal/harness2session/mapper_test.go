package harness2session

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func mapFixture(t *testing.T, rel string, opts MapOptions) []MappedEvent {
	t.Helper()
	ref, ok := listFixtures(t, copyRuns(t))[rel]
	if !ok {
		t.Fatalf("%s not listed", rel)
	}
	store := &Store{Roots: []string{ref.Root}}
	trajectory, _, err := store.Read(ref)
	if err != nil {
		t.Fatal(err)
	}
	return MapTrajectory(trajectory, opts)
}

func actions(events []MappedEvent) []string {
	out := make([]string, len(events))
	for i, item := range events {
		out[i] = item.Event.Event.Action
	}
	return out
}

// Every event a collector writes must survive the trip through JSON as the release-contract shape.
func roundTrip(t *testing.T, ev schema.Event) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMapOpenCodeStream(t *testing.T) {
	events := mapFixture(t, jbRel, MapOptions{})
	want := []string{"session.started", "agent.reasoning", "file.read", "command.executed", "file.modified", "token.usage", "agent.message", "token.usage"}
	if got := actions(events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v\nwant      %v", got, want)
	}
	for _, item := range events {
		ev := item.Event
		if ev.Harness.Name != Harness || ev.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Fatalf("%s harness = %+v, want harness2 by poll", ev.Event.Action, ev.Harness)
		}
		// The collector writes the canonical name, so a normalizing reader leaves it alone.
		if got := asymptoteobserve.NormalizeHarnessName(ev.Harness.Name); got != Harness {
			t.Fatalf("NormalizeHarnessName(%q) = %q", ev.Harness.Name, got)
		}
		if ev.Session == nil || ev.Session.ID != "ses_7a1f00c0ffee" {
			t.Fatalf("%s session = %+v, want OpenCode's session id", ev.Event.Action, ev.Session)
		}
		ctx, _ := ev.Raw["harness2"].(map[string]interface{})
		if ctx["executor"] != "opencode" || ctx["benchmark"] != "jb" || ctx["task_id"] != "accountant/task3" ||
			ctx["label"] != "h2-final-1" || ctx["attempt"] != 2 || ctx["trajectory"] != jbRel {
			t.Fatalf("%s harness2 context = %v", ev.Event.Action, ctx)
		}
		if ev.Event.ID == "" || ev.Timestamp == "" {
			t.Fatalf("%s has no id or timestamp: %+v", ev.Event.Action, ev.Event)
		}
	}
	if events[0].Event.Event.Fidelity != schema.FidelityInferred {
		t.Fatalf("session.started fidelity = %q; the stream never states a start, so it is inferred", events[0].Event.Event.Fidelity)
	}

	read := events[2].Event
	if read.File == nil || read.File.Operation != "read" || !strings.HasSuffix(read.File.Path, "TASK_INSTRUCTIONS.txt") || read.File.Language != "txt" {
		t.Fatalf("file.read = %+v", read.File)
	}
	// A tool event is stamped when the tool finished, not when the line was printed.
	if read.Timestamp != schema.FormatTimestamp(timeMS(1793004902250)) {
		t.Fatalf("file.read timestamp = %s", read.Timestamp)
	}
	if read.GenAI == nil || read.GenAI.Tool == nil || read.GenAI.Tool.Call.ID != "call_read_1" || read.GenAI.Tool.Call.Result != "Prepare the Q3 reconciliation." {
		t.Fatalf("file.read gen_ai = %+v", read.GenAI)
	}

	cmd := events[3].Event
	if cmd.Command == nil || cmd.Command.Command != "python3 scripts/reconcile.py" || cmd.Command.Output != "wrote output/q3.xlsx\n" ||
		cmd.Command.ExitCode == nil || *cmd.Command.ExitCode != 0 {
		t.Fatalf("command = %+v", cmd.Command)
	}
	if cmd.Content == nil || !cmd.Content.Included {
		t.Fatalf("command content = %+v, want the output retained", cmd.Content)
	}

	edit := events[4].Event
	if edit.Error == nil || edit.Error.Type != "tool_error" || edit.Raw["error_message"] != "oldString not found in content" {
		t.Fatalf("failed edit error = %+v raw=%v", edit.Error, edit.Raw["error_message"])
	}
	if edit.File == nil || edit.File.Operation != "modify" || edit.File.Diff != "y" {
		t.Fatalf("failed edit file = %+v", edit.File)
	}

	usage := events[5].Event.GenAI.Usage
	// OpenCode keeps reasoning out of output when the provider total says so (1200+200+400+40 =
	// 1840); gen_ai counts it inside.
	if *usage.InputTokens != 1200 || *usage.OutputTokens != 440 || *usage.Reasoning.OutputTokens != 40 ||
		*usage.CacheRead.InputTokens != 200 || usage.CacheCreation != nil || *usage.CostUSD != 0.0123 {
		t.Fatalf("step usage = %s", mustJSON(t, usage))
	}
	if second := events[7].Event.GenAI.Usage; *second.OutputTokens != 100 || second.Reasoning != nil {
		t.Fatalf("second step usage = %s", mustJSON(t, second))
	}
	if msg := events[6].Event; msg.GenAI == nil || msg.GenAI.Output == nil || !strings.Contains(mustJSON(t, msg.GenAI.Output), "output/q3.xlsx") {
		t.Fatalf("agent.message = %+v", msg.GenAI)
	}
	for _, item := range events {
		roundTrip(t, item.Event)
	}
}

// WorkBuddy captures OpenCode's stdout whole, banner lines included, and a session error is the one
// OpenCode event with no part.
func TestMapOpenCodeCapturedStdout(t *testing.T) {
	events := mapFixture(t, wbRel, MapOptions{})
	if got := strings.Join(actions(events), ","); got != "session.started,agent.message,session.error" {
		t.Fatalf("actions = %s", got)
	}
	errEv := events[2].Event
	if errEv.Error == nil || errEv.Error.Type != "APIError" || errEv.Raw["error_message"] != "rate limited" || errEv.Severity != schema.SeverityHigh {
		t.Fatalf("session.error = %+v raw=%v", errEv.Error, errEv.Raw)
	}
	ctx := errEv.Raw["harness2"].(map[string]interface{})
	if ctx["subset"] != "office" || ctx["trial"] != "xlsx-budget__Ab3dE" || ctx["harness_fingerprint"] != "base" || ctx["run_id"] != "wb-office-xlsx-budget-base" {
		t.Fatalf("context = %v", ctx)
	}
}

func TestMapCodexExecStream(t *testing.T) {
	events := mapFixture(t, labRel, MapOptions{})
	want := []string{"session.started", "agent.reasoning", "command.executed", "command.executed",
		"file.created", "file.modified", "file.modified", "mcp.tool_invoked", "agent.message", "token.usage"}
	if got := actions(events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v\nwant      %v", got, want)
	}
	for _, item := range events {
		if item.Event.Session.ID != "0199f1c2-7d1e-7c41-9a3b-5e2b8d0f6a11" {
			t.Fatalf("session id = %q, want the thread id", item.Event.Session.ID)
		}
		ctx := item.Event.Raw["harness2"].(map[string]interface{})
		if ctx["executor"] != "codex" || ctx["run_id"] != "antitrust-competition__t07__h2-cand-dom-1" || ctx["harness_fingerprint"] != "9f2c4e1a7b3d5f60" {
			t.Fatalf("context = %v", ctx)
		}
	}
	// item.started for the same command is not a second event.
	ok, failed := events[2].Event, events[3].Event
	if ok.Command.Command != "bash -lc 'ls documents'" || *ok.Command.ExitCode != 0 || ok.Error != nil {
		t.Fatalf("completed command = %+v err=%+v", ok.Command, ok.Error)
	}
	if *failed.Command.ExitCode != 1 || failed.Error == nil || failed.Error.Type != "command_failed" {
		t.Fatalf("failed command = %+v err=%+v", failed.Command, failed.Error)
	}
	for i, want := range []struct{ path, op string }{{"/work/memo.md", "create"}, {"/work/draft.md", "modify"}, {"/work/scratch.txt", "delete"}} {
		file := events[4+i].Event.File
		if file.Path != want.path || file.Operation != want.op {
			t.Errorf("file change %d = %+v, want %+v", i, file, want)
		}
	}
	mcp := events[7].Event
	if mcp.MCP == nil || mcp.MCP.Server != "docs" || mcp.MCP.Tool != "search" || mcp.Error != nil {
		t.Fatalf("mcp = %+v err=%+v", mcp.MCP, mcp.Error)
	}
	usage := events[9].Event.GenAI.Usage
	// Codex's input includes its cached input; gen_ai counts them apart.
	if *usage.InputTokens != 2000 || *usage.CacheRead.InputTokens != 3000 || *usage.OutputTokens != 700 || *usage.Reasoning.OutputTokens != 300 {
		t.Fatalf("usage = %s", mustJSON(t, usage))
	}
	// The exec stream has no times; every event takes the file's modification time.
	for _, item := range events {
		if item.Event.Timestamp != events[0].Event.Timestamp {
			t.Fatalf("timestamps differ: %s vs %s", item.Event.Timestamp, events[0].Event.Timestamp)
		}
	}
}

// turn.completed reports the thread's running total, so a two-turn stream must not count the first
// turn twice.
func TestCodexUsageIsDifferencedAcrossTurns(t *testing.T) {
	lines := []Line{
		{Number: 1, Object: obj(t, `{"type":"thread.started","thread_id":"th"}`)},
		{Number: 2, Object: obj(t, `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"reasoning_output_tokens":0}}`)},
		{Number: 3, Object: obj(t, `{"type":"turn.completed","usage":{"input_tokens":250,"cached_input_tokens":140,"output_tokens":30,"reasoning_output_tokens":5}}`)},
		// A total that went down is a restarted count, taken whole.
		{Number: 4, Object: obj(t, `{"type":"turn.completed","usage":{"input_tokens":20,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`)},
	}
	events := MapTrajectory(Trajectory{Ref: TrajectoryRef{Format: FormatCodexExec, Rel: "x.jsonl"}, Lines: lines}, MapOptions{})
	var got []string
	for _, item := range events {
		if u := item.Event.GenAI; u != nil && u.Usage != nil {
			cache := int64(0)
			if u.Usage.CacheRead != nil {
				cache = *u.Usage.CacheRead.InputTokens
			}
			got = append(got, strings.Join([]string{itoa64(*u.Usage.InputTokens), itoa64(cache), itoa64(*u.Usage.OutputTokens)}, "/"))
		}
	}
	if strings.Join(got, " ") != "60/40/10 50/100/20 20/0/2" {
		t.Fatalf("per-turn usage (input/cache/output) = %v", got)
	}
	// Resuming after the first turn still differences against it.
	resumed := MapTrajectory(Trajectory{Ref: TrajectoryRef{Format: FormatCodexExec, Rel: "x.jsonl"}, Lines: lines[:3]}, MapOptions{MinLine: 2, SkipStarted: true})
	if len(resumed) != 1 || *resumed[0].Event.GenAI.Usage.InputTokens != 50 {
		t.Fatalf("resumed = %v", actions(resumed))
	}
}

// Ids are coordinates in the session, so a second read names every event the same way and a resumed
// read names the events it skips over the same way too.
func TestEventIDsAreStableAndResumeSkipsWrittenLines(t *testing.T) {
	first := mapFixture(t, jbRel, MapOptions{})
	second := mapFixture(t, jbRel, MapOptions{})
	seen := map[string]bool{}
	for i := range first {
		if first[i].Event.Event.ID != second[i].Event.Event.ID {
			t.Fatalf("event %d id changed between reads", i)
		}
		if seen[first[i].Event.Event.ID] {
			t.Fatalf("duplicate id %s", first[i].Event.Event.ID)
		}
		seen[first[i].Event.Event.ID] = true
	}
	resumed := mapFixture(t, jbRel, MapOptions{MinLine: 4, SkipStarted: true})
	if got := strings.Join(actions(resumed), ","); got != "file.modified,token.usage,agent.message,token.usage" {
		t.Fatalf("resumed actions = %s", got)
	}
	if resumed[0].Event.Event.ID != first[4].Event.Event.ID {
		t.Fatal("a resumed read named an event differently")
	}
}

// A Codex run that fails before its thread starts names no session; its events still group under
// one id derived from where the file is, the same on every read.
func TestStreamWithoutASessionIDGetsAStableOne(t *testing.T) {
	lines := []Line{{Number: 1, Object: obj(t, `{"type":"error","message":"401 Unauthorized"}`)}}
	ref := TrajectoryRef{Format: FormatCodexExec, Rel: "lab/results/cx/r1/codex_session.jsonl"}
	a := MapTrajectory(Trajectory{Ref: ref, Lines: lines}, MapOptions{})
	b := MapTrajectory(Trajectory{Ref: ref, Lines: lines}, MapOptions{})
	if len(a) != 1 || a[0].Event.Event.Action != "session.error" || a[0].Event.Error.Type != "stream_error" {
		t.Fatalf("events = %v", actions(a))
	}
	if id := a[0].Event.Session.ID; !strings.HasPrefix(id, "harness2-") || id != b[0].Event.Session.ID {
		t.Fatalf("session id = %q / %q", id, b[0].Event.Session.ID)
	}
}

func TestCodexItemsWithoutAnOutcomeAreSkipped(t *testing.T) {
	lines := []Line{
		{Number: 1, Object: obj(t, `{"type":"thread.started","thread_id":"th"}`)},
		{Number: 2, Object: obj(t, `{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"sleep 999","aggregated_output":"","exit_code":null,"status":"in_progress"}}`)},
		{Number: 3, Object: obj(t, `{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"   "}}`)},
		{Number: 4, Object: obj(t, `{"type":"item.completed","item":{"id":"i3","type":"todo_list","items":[]}}`)},
		{Number: 5, Object: obj(t, `{"type":"item.completed","item":{"id":"i4","type":"some_future_item"}}`)},
		{Number: 6, Object: obj(t, `{"type":"turn.completed","usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0}}`)},
		{Number: 7, Object: obj(t, `{"type":"item.completed","item":{"id":"i5","type":"error","message":"model fell back"}}`)},
		{Number: 8, Object: obj(t, `{"type":"turn.failed","error":{"message":"stream disconnected"}}`)},
	}
	events := MapTrajectory(Trajectory{Ref: TrajectoryRef{Format: FormatCodexExec, Rel: "x"}, Lines: lines}, MapOptions{})
	if got := strings.Join(actions(events), ","); got != "session.started,session.error,session.error" {
		t.Fatalf("actions = %s", got)
	}
	if events[1].Event.Severity != schema.SeverityMedium || events[2].Event.Severity != schema.SeverityHigh {
		t.Fatalf("severities = %s, %s; an item warning is not a failed turn", events[1].Event.Severity, events[2].Event.Severity)
	}
}

func obj(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	m, ok := decodeObject([]byte(s))
	if !ok {
		t.Fatalf("bad fixture %s", s)
	}
	return m
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func timeMS(ms int64) time.Time { return time.UnixMilli(ms) }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
