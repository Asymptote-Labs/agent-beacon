package claudesession

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// Claude Code writes a streamed API response as one transcript line per content block: a thinking
// line, a text line, a tool_use line, each its own record with its own uuid, all sharing the
// response's message.id and requestId, and each carrying a copy of message.usage. A line written
// before the stream's final message_delta carries the message_start snapshot, whose output_tokens
// is a placeholder. These fixtures reproduce that layout field for field.

type streamed struct {
	session   string
	uuid      string
	parent    string
	msgID     string
	requestID string
	block     map[string]interface{}
	output    int64
	sidechain bool
	agentID   string
}

func (s streamed) line() string {
	usage := map[string]interface{}{
		"input_tokens":                4,
		"cache_creation_input_tokens": 1200,
		"cache_read_input_tokens":     30000,
		"cache_creation": map[string]interface{}{
			"ephemeral_5m_input_tokens": 0,
			"ephemeral_1h_input_tokens": 1200,
		},
		"output_tokens": s.output,
		"service_tier":  "standard",
	}
	message := map[string]interface{}{
		"model":         "claude-sonnet-4-5",
		"type":          "message",
		"role":          "assistant",
		"content":       []interface{}{s.block},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         usage,
	}
	if s.msgID != "" {
		message["id"] = s.msgID
	}
	record := map[string]interface{}{
		"parentUuid":  nullable(s.parent),
		"isSidechain": s.sidechain,
		"userType":    "external",
		"cwd":         "/tmp/repo",
		"sessionId":   s.session,
		"version":     "2.1.154",
		"gitBranch":   "main",
		"message":     message,
		"type":        "assistant",
		"uuid":        s.uuid,
		"timestamp":   "2026-09-19T22:00:01.000Z",
	}
	if s.requestID != "" {
		record["requestId"] = s.requestID
	}
	if s.agentID != "" {
		record["agentId"] = s.agentID
	}
	data, _ := json.Marshal(record)
	return string(data)
}

func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func thinkingBlock(text string) map[string]interface{} {
	return map[string]interface{}{"type": "thinking", "thinking": text, "signature": "EqoBCkYIBxgCKkA"}
}

func textBlock(text string) map[string]interface{} {
	return map[string]interface{}{"type": "text", "text": text}
}

func bashBlock(id, command string) map[string]interface{} {
	return map[string]interface{}{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]interface{}{"command": command, "description": "run"}}
}

// splitResponse is one API response written the way Claude Code writes it: thinking, text and a
// Bash call on three lines, the first two carrying the message_start placeholder of 8 output tokens
// and the last the final count, followed by the tool's result.
func splitResponse(session, prefix, msgID, requestID string, final int64) []string {
	return []string{
		streamed{session: session, uuid: prefix + "-think", parent: prefix + "-prompt", msgID: msgID, requestID: requestID, block: thinkingBlock("Check the tests first."), output: 8}.line(),
		streamed{session: session, uuid: prefix + "-text", parent: prefix + "-think", msgID: msgID, requestID: requestID, block: textBlock("I'll run the tests."), output: 8}.line(),
		streamed{session: session, uuid: prefix + "-tool", parent: prefix + "-text", msgID: msgID, requestID: requestID, block: bashBlock("toolu_"+prefix, "npm test"), output: final}.line(),
		toolResultLine(session, prefix+"-result", "toolu_"+prefix, "ok", false),
	}
}

func usageEvents(mapped []MappedEvent) []MappedEvent {
	var out []MappedEvent
	for _, item := range mapped {
		if item.Event.Event.Action == "token.usage" {
			out = append(out, item)
		}
	}
	return out
}

func outputOf(t *testing.T, ev schema.Event) int64 {
	t.Helper()
	if ev.GenAI == nil || ev.GenAI.Usage == nil || ev.GenAI.Usage.OutputTokens == nil {
		t.Fatalf("usage event without output tokens: %+v", ev.GenAI)
	}
	return *ev.GenAI.Usage.OutputTokens
}

func sumOutput(t *testing.T, mapped []MappedEvent) int64 {
	t.Helper()
	var total int64
	for _, item := range usageEvents(mapped) {
		total += outputOf(t, item.Event)
	}
	return total
}

func countActions(mapped []MappedEvent) map[string]int {
	out := map[string]int{}
	for _, item := range mapped {
		out[item.Event.Event.Action]++
	}
	return out
}

func TestSplitResponseUsageIsCountedOnceWithTheFinalSnapshot(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	lines := append([]string{userLine("sess-1", "r1-prompt", "run the tests")}, splitResponse("sess-1", "r1", "msg_01A", "req_01A", 312)...)
	mapped := MapSession(ref, decodeFixture(t, lines), MapOptions{})

	usage := usageEvents(mapped)
	if len(usage) != 1 {
		t.Fatalf("token.usage events = %d, want 1 for one API response written on three lines: %v", len(usage), actions(mapped))
	}
	ev := usage[0].Event
	if got := outputOf(t, ev); got != 312 {
		t.Fatalf("output_tokens = %d, want the final snapshot's 312", got)
	}
	u := ev.GenAI.Usage
	if *u.InputTokens != 4 || *u.CacheRead.InputTokens != 30000 || *u.CacheCreation.InputTokens != 1200 {
		t.Fatalf("usage = %+v, want the response's input, cache read and cache creation counted once", u)
	}
	if ev.GenAI.Response == nil || ev.GenAI.Response.ID != "msg_01A" {
		t.Fatalf("response = %+v, want msg_01A", ev.GenAI.Response)
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("usage event invalid: %v", err)
	}

	// The other per-block emissions are per record and stay that way: each block line is its own
	// reasoning, message or tool call.
	counts := countActions(mapped)
	for action, want := range map[string]int{"agent.reasoning": 1, "agent.message": 1, "tool.invoked": 1, "command.executed": 1, "prompt.submitted": 1} {
		if counts[action] != want {
			t.Errorf("%s = %d, want %d (%v)", action, counts[action], want, actions(mapped))
		}
	}
	command := findAction(t, mapped, "command.executed")
	if command.Command == nil || command.Command.Command != "npm test" {
		t.Errorf("command = %+v, want the split tool_use line's command joined to its result", command.Command)
	}
}

func TestSplitResponseUsageNeverTakesAStaleSnapshot(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl"}
	// The largest count wins wherever it sits, including ahead of a smaller one.
	lines := []string{
		streamed{session: "sess-1", uuid: "a", msgID: "msg_01B", requestID: "req_01B", block: thinkingBlock("x"), output: 7}.line(),
		streamed{session: "sess-1", uuid: "b", parent: "a", msgID: "msg_01B", requestID: "req_01B", block: textBlock("y"), output: 120}.line(),
		streamed{session: "sess-1", uuid: "c", parent: "b", msgID: "msg_01B", requestID: "req_01B", block: bashBlock("toolu_b", "ls"), output: 40}.line(),
	}
	usage := usageEvents(MapSession(ref, decodeFixture(t, lines), MapOptions{}))
	if len(usage) != 1 || outputOf(t, usage[0].Event) != 120 {
		t.Fatalf("usage = %d events, want one carrying 120", len(usage))
	}
}

func TestDistinctResponsesAreEachCounted(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl"}
	lines := []string{userLine("sess-1", "r1-prompt", "go")}
	lines = append(lines, splitResponse("sess-1", "r1", "msg_01C", "req_01C", 50)...)
	lines = append(lines, splitResponse("sess-1", "r2", "msg_01D", "req_01D", 80)...)
	mapped := MapSession(ref, decodeFixture(t, lines), MapOptions{})
	usage := usageEvents(mapped)
	if len(usage) != 2 {
		t.Fatalf("token.usage events = %d, want 2", len(usage))
	}
	if usage[0].Event.Event.ID == usage[1].Event.Event.ID {
		t.Fatal("two responses share an event id")
	}
	if got := sumOutput(t, mapped); got != 130 {
		t.Fatalf("output total = %d, want 50+80", got)
	}
}

func TestUsageWithoutAResponseIDIsCountedPerRecord(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl"}
	lines := []string{
		// No message.id: nothing ties these together, so each record is its own response, under
		// the event id it always had.
		streamed{session: "sess-1", uuid: "n1", block: textBlock("one"), output: 11}.line(),
		streamed{session: "sess-1", uuid: "n2", parent: "n1", block: textBlock("two"), output: 13}.line(),
		// message.id without requestId still identifies the response.
		streamed{session: "sess-1", uuid: "m1", parent: "n2", msgID: "msg_01E", block: thinkingBlock("x"), output: 2}.line(),
		streamed{session: "sess-1", uuid: "m2", parent: "m1", msgID: "msg_01E", block: textBlock("z"), output: 21}.line(),
	}
	usage := usageEvents(MapSession(ref, decodeFixture(t, lines), MapOptions{}))
	if len(usage) != 3 {
		t.Fatalf("token.usage events = %d, want 3", len(usage))
	}
	if usage[0].Event.Event.ID != claudeEventID("n1:usage") || usage[1].Event.Event.ID != claudeEventID("n2:usage") {
		t.Fatal("records without a message id changed event id")
	}
	if got := outputOf(t, usage[2].Event); got != 21 {
		t.Fatalf("message-id-only response output = %d, want 21", got)
	}
}

// A sweep can land between two block lines of one response. The cursor advances past what it read,
// and the next sweep must count only what the earlier one did not -- so the total is the final
// snapshot's, once, however the response is split across sweeps.
func TestIncrementalMapCountsASplitResponseOnce(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl"}
	lines := []string{userLine("sess-1", "p", "go")}
	lines = append(lines, splitResponse("sess-1", "r1", "msg_01F", "req_01F", 300)...)
	records := decodeFixture(t, lines)
	// Every split point: after the prompt, after each block line, after the result.
	for cut := 1; cut <= len(records); cut++ {
		first := MapSession(ref, records[:cut], MapOptions{})
		second := MapSession(ref, records, MapOptions{MinLine: records[cut-1].Line, SkipSessionStarted: true})
		total := sumOutput(t, first) + sumOutput(t, second)
		if total != 300 {
			t.Errorf("cut after line %d: output counted %d, want 300", records[cut-1].Line, total)
		}
		var input int64
		for _, item := range append(usageEvents(first), usageEvents(second)...) {
			if in := item.Event.GenAI.Usage.InputTokens; in != nil {
				input += *in
			}
		}
		if input != 4 {
			t.Errorf("cut after line %d: input counted %d, want 4", records[cut-1].Line, input)
		}
	}
}

func TestSplitResponseAcrossSweepsOnDiskTotalsOnceAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	sessionPath := filepath.Join(projects, "-tmp-repo", "sess-1.jsonl")
	statePath := filepath.Join(dir, "state", "claude.json")
	logPath := filepath.Join(dir, "runtime.jsonl")
	opts := CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}

	all := []string{userLine("sess-1", "r1-prompt", "run the tests")}
	all = append(all, splitResponse("sess-1", "r1", "msg_01G", "req_01G", 312)...)
	all = append(all, splitResponse("sess-1", "r2", "msg_01H", "req_01H", 95)...)

	// Sweep one sees the first two block lines of the first response: the placeholder snapshot.
	writeFile(t, sessionPath, strings.Join(all[:3], "\n")+"\n")
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	// Sweep two sees the rest, including the line that carries the final count.
	writeFile(t, sessionPath, strings.Join(all, "\n")+"\n")
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	// Sweep three has nothing new.
	again, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if again.EventsEmitted != 0 {
		t.Fatalf("idempotent re-run emitted %d events", again.EventsEmitted)
	}

	report := tokens.Aggregate(readLog(t, logPath), tokens.Options{})
	want := tokens.Usage{InputTokens: 8, OutputTokens: 312 + 95, CacheReadInputTokens: 60000, CacheCreationInputTokens: 2400}
	if got := report.Totals; got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens ||
		got.CacheReadInputTokens != want.CacheReadInputTokens || got.CacheCreationInputTokens != want.CacheCreationInputTokens {
		t.Fatalf("report totals = %+v, want %+v", got, want)
	}

	// A from-scratch sweep over the finished file names the same response the same way and
	// reports the same totals.
	freshLog := filepath.Join(dir, "fresh.jsonl")
	if _, err := CollectOnce(CollectOptions{ProjectsDir: projects, StatePath: filepath.Join(dir, "fresh-state.json"), LogPath: freshLog, Write: true, UserMode: true}); err != nil {
		t.Fatalf("fresh sweep: %v", err)
	}
	fresh := readLog(t, freshLog)
	freshReport := tokens.Aggregate(fresh, tokens.Options{})
	if freshReport.Totals.OutputTokens != want.OutputTokens || freshReport.Totals.InputTokens != want.InputTokens || freshReport.EventsWithUsage != 2 {
		t.Fatalf("fresh totals = %+v (events %d), want %+v over 2 events", freshReport.Totals, freshReport.EventsWithUsage, want)
	}
	// Response-derived ids: the from-scratch event for each response is the one the incremental
	// sweeps wrote first, so the two logs agree on what to call it.
	incrementalIDs := map[string]bool{}
	for _, ev := range readLog(t, logPath) {
		if ev.Event.Action == "token.usage" {
			incrementalIDs[ev.Event.ID] = true
		}
	}
	for _, ev := range fresh {
		if ev.Event.Action == "token.usage" && !incrementalIDs[ev.Event.ID] {
			t.Errorf("from-scratch usage event %s (response %+v) was not written by the incremental sweeps", ev.Event.ID, ev.GenAI.Response)
		}
	}
}

func TestSidechainAndForkedTranscriptsCountEachResponseOnce(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	project := filepath.Join(projects, "-tmp-repo")
	logPath := filepath.Join(dir, "runtime.jsonl")

	main := append([]string{userLine("sess-1", "r1-prompt", "explore")}, splitResponse("sess-1", "r1", "msg_01M", "req_01M", 200)...)
	writeFile(t, filepath.Join(project, "sess-1.jsonl"), strings.Join(main, "\n")+"\n")

	// A subagent's own API calls live in its own file, split the same way.
	sub := []string{
		streamed{session: "sess-1", uuid: "s-think", msgID: "msg_01S", requestID: "req_01S", block: thinkingBlock("look"), output: 3, sidechain: true, agentID: "a1"}.line(),
		streamed{session: "sess-1", uuid: "s-tool", parent: "s-think", msgID: "msg_01S", requestID: "req_01S", block: bashBlock("toolu_s", "ls"), output: 40, sidechain: true, agentID: "a1"}.line(),
	}
	subPath := filepath.Join(project, "sess-1", "subagents", "agent-a1.jsonl")
	writeFile(t, subPath, strings.Join(sub, "\n")+"\n")

	// A forked session replays the original's lines -- same message.id and requestId under a new
	// session id and new uuids -- and then continues with a response of its own.
	fork := append(splitResponse("sess-2", "f1", "msg_01M", "req_01M", 200), splitResponse("sess-2", "f2", "msg_01N", "req_01N", 60)...)
	forkPath := filepath.Join(project, "sess-2.jsonl")
	writeFile(t, forkPath, strings.Join(fork, "\n")+"\n")

	if _, err := CollectOnce(CollectOptions{ProjectsDir: projects, StatePath: filepath.Join(dir, "state.json"), LogPath: logPath, Write: true, UserMode: true}); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	events := readLog(t, logPath)
	report := tokens.Aggregate(events, tokens.Options{})
	if report.Totals.OutputTokens != 200+40+60 || report.EventsWithUsage != 3 {
		t.Fatalf("totals = %+v over %d events, want 300 output over 3 responses", report.Totals, report.EventsWithUsage)
	}
	var sawSubagent bool
	for _, ev := range events {
		if ev.Event.Action == "token.usage" && ev.GenAI != nil && ev.GenAI.Response != nil && ev.GenAI.Response.ID == "msg_01S" {
			sawSubagent = ev.GenAI.Agent != nil && ev.GenAI.Agent.ID == "a1" && outputOf(t, ev) == 40
		}
	}
	if !sawSubagent {
		t.Fatal("subagent response usage missing or not attributed to its agent")
	}
}

// When a sweep stops partway through a session (the retention window), the cursor must not land
// between a response's first counted line and the line its usage event sits on, or the next sweep
// would treat the response as already counted.
func TestPartialSweepDoesNotStrandAResponsesUsage(t *testing.T) {
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl"}
	lines := []string{
		userLine("sess-1", "p", "go"),
		streamed{session: "sess-1", uuid: "a", msgID: "msg_01P", requestID: "req_01P", block: bashBlock("toolu_a", "ls"), output: 8}.line(),
		// Claude Code runs a tool while the response is still streaming, so its result can land
		// between two block lines of the same response.
		toolResultLine("sess-1", "ra", "toolu_a", "ok", false),
		streamed{session: "sess-1", uuid: "b", parent: "ra", msgID: "msg_01P", requestID: "req_01P", block: bashBlock("toolu_b", "pwd"), output: 300}.line(),
	}
	records := decodeFixture(t, lines)
	mapped := MapSession(ref, records, MapOptions{})
	failed := -1
	for i, item := range mapped {
		if item.Event.Event.Action == "token.usage" {
			failed = i
		}
	}
	if failed < 0 {
		t.Fatalf("no usage event: %v", actions(mapped))
	}
	cursor := &Cursor{}
	advanceCursorPartial(cursor, mapped, failed)
	retry := MapSession(ref, records, MapOptions{MinLine: cursor.LastLine, SkipSessionStarted: true})
	if got := sumOutput(t, retry); got != 300 {
		t.Fatalf("after a stop at the usage event (cursor %d), the retry counted %d output tokens, want 300", cursor.LastLine, got)
	}
	var input int64
	for _, item := range usageEvents(retry) {
		input += *item.Event.GenAI.Usage.InputTokens
	}
	if input != 4 {
		t.Fatalf("retry counted %d input tokens, want 4", input)
	}
}
