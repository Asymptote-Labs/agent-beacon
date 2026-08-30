package fxsession

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// mapFixture decodes lines and maps them the way a sweep does.
func mapFixture(t *testing.T, lines []string, opts MapOptions) []MappedEvent {
	t.Helper()
	events, stats := decodeAll(t, strings.Join(lines, "\n")+"\n")
	if stats.Malformed != 0 {
		t.Fatalf("fixture produced %d malformed lines: %v", stats.Malformed, stats.FirstError)
	}
	return MapSession(SessionRef{ID: testSessionID}, events, opts)
}

func actions(mapped []MappedEvent) []string {
	out := make([]string, 0, len(mapped))
	for _, item := range mapped {
		out = append(out, item.Event.Event.Action)
	}
	return out
}

func find(t *testing.T, mapped []MappedEvent, action string) schema.Event {
	t.Helper()
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			return item.Event
		}
	}
	t.Fatalf("no %q event in %v", action, actions(mapped))
	return schema.Event{}
}

func findAll(mapped []MappedEvent, action string) []schema.Event {
	var out []schema.Event
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			out = append(out, item.Event)
		}
	}
	return out
}

// One turn is one prompt, three tool calls, one reply, and the turn's tokens. This asserts the
// whole shape at once, because the failure that matters here is not a wrong field on one event --
// it is an action that silently stops being produced.
func TestOneTurnProducesTheEventsItJustifies(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})

	want := []string{
		"session.started",
		"prompt.submitted",
		"file.read",
		"file.modified",
		"command.executed",
		"agent.message",
		"token.usage",
	}
	got := actions(mapped)
	if len(got) != len(want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("actions = %v, want %v", got, want)
		}
	}
}

// Every event has to say how Beacon came to know it. fx has no hook and no OTLP export, so the
// answer is always poll -- and a consumer that reads these as hook-grade coverage would believe
// Beacon could have blocked something it never saw.
func TestEveryEventCarriesPollProvenanceAndLocalOrigin(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2), usageCheckpointLine(3, 4200, 880, 0.5)}, MapOptions{})
	if len(mapped) == 0 {
		t.Fatal("no events mapped")
	}
	for _, item := range mapped {
		ev := item.Event
		if ev.Harness.Name != Harness {
			t.Errorf("%s: harness = %q, want %q", ev.Event.Action, ev.Harness.Name, Harness)
		}
		if ev.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Errorf("%s: collection_method = %q, want poll", ev.Event.Action, ev.Harness.CollectionMethod)
		}
		if ev.Origin != schema.OriginLocal {
			t.Errorf("%s: origin = %q, want local", ev.Event.Action, ev.Origin)
		}
		if ev.Event.Fidelity == "" {
			t.Errorf("%s: no fidelity marker", ev.Event.Action)
		}
		if ev.Session == nil || ev.Session.ID != testSessionID {
			t.Errorf("%s: session = %+v, want %q", ev.Event.Action, ev.Session, testSessionID)
		}
		if ev.Session != nil && ev.Session.WorkingDirectory != "/repo" {
			t.Errorf("%s: working directory = %q, want /repo", ev.Event.Action, ev.Session.WorkingDirectory)
		}
	}
}

// The harness name has to survive normalization unchanged, or one fx session is recorded under two
// names by the collector and by anything that re-normalizes on read.
func TestHarnessNameIsTheCanonicalOne(t *testing.T) {
	if got := asymptoteobserve.NormalizeHarnessName(Harness); got != Harness {
		t.Fatalf("NormalizeHarnessName(%q) = %q; the mapper's harness name is not canonical", Harness, got)
	}
	if got := asymptoteobserve.NormalizeHarnessName("fx"); got != Harness {
		t.Fatalf("NormalizeHarnessName(\"fx\") = %q, want %q", got, Harness)
	}
}

// Every Beacon writer runs Validate before a line reaches the log. An event that fails it is
// dropped with an error, so a mapper that produces one produces silence.
func TestEveryMappedEventPassesSchemaValidation(t *testing.T) {
	mapped := mapFixture(t, []string{
		sessionStartedLine(1, "/repo"),
		assistantTurnLine(2),
		usageCheckpointLine(3, 4200, 880, 0.5),
	}, MapOptions{})
	for _, item := range mapped {
		if err := item.Event.Validate(); err != nil {
			t.Errorf("%s failed validation: %v", item.Event.Event.Action, err)
		}
	}
}

func TestPromptCarriesTextRetentionAndSemconvMessages(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	prompt := find(t, mapped, "prompt.submitted")

	if prompt.Prompt == nil || prompt.Prompt.Text != "add a health endpoint and run the tests" {
		t.Fatalf("prompt text = %+v", prompt.Prompt)
	}
	if prompt.Content == nil || !prompt.Content.Included || prompt.Content.Retention != schema.ContentRetentionFull {
		t.Fatalf("content marker = %+v", prompt.Content)
	}
	if prompt.Content.Bytes != len("add a health endpoint and run the tests") {
		t.Errorf("content bytes = %d", prompt.Content.Bytes)
	}
	if prompt.GenAI == nil || prompt.GenAI.Input == nil {
		t.Fatal("prompt has no gen_ai.input.messages, so the semconv readers see nothing")
	}
}

// The prompt happened when the turn started; the envelope is stamped when fx committed the turn,
// after the model finished. Using the commit time for the prompt would put every prompt after the
// tool calls it caused, which is exactly backwards for anyone reading a session in order.
func TestPromptIsTimestampedAtTurnStartNotTurnCommit(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	prompt := find(t, mapped, "prompt.submitted")
	command := find(t, mapped, "command.executed")

	if prompt.Timestamp >= command.Timestamp {
		t.Fatalf("prompt at %s is not before the command it caused at %s", prompt.Timestamp, command.Timestamp)
	}
	want := schema.FormatTimestamp(millis(1770000000500))
	if prompt.Timestamp != want {
		t.Errorf("prompt timestamp = %s, want the turn's start %s", prompt.Timestamp, want)
	}
}

// A command that exits non-zero is still a command that ran. fx marks it status=failure, and a
// mapper that classified on status would file it as tool.failed -- invisible to every rule that
// matches command.executed, which is most of the rule corpus.
func TestFailingCommandStaysACommandEvent(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	command := find(t, mapped, "command.executed")

	if command.Command == nil {
		t.Fatal("command block missing")
	}
	if command.Command.Command != "npm test" {
		t.Errorf("command = %q, want %q", command.Command.Command, "npm test")
	}
	if command.Command.ExitCode == nil || *command.Command.ExitCode != 1 {
		t.Fatalf("exit code = %+v, want 1", command.Command.ExitCode)
	}
	if command.Command.Output != "1 test failed" {
		t.Errorf("output = %q", command.Command.Output)
	}
	if command.Error == nil || command.Error.Type == "" {
		t.Error("a failed tool call must still be marked as an error on the event")
	}
	if len(findAll(mapped, "tool.failed")) != 0 {
		t.Error("the failing command was also reported as tool.failed, double-counting one action")
	}
}

// fx records four different process outcomes and only one of them is an exit code. Writing a signal
// or a timeout as an exit code would put a number in a field that means something else -- a rule
// matching exit_code == 0 would match a command that was killed.
func TestOnlyAnExitCodeIsWrittenAsAnExitCode(t *testing.T) {
	for _, outcome := range []struct {
		name string
		json string
	}{
		{"signal", `{"kind":"signal","value":9}`},
		{"timed_out", `{"kind":"timed_out","value":null}`},
		{"output_capture_failed", `{"kind":"output_capture_failed","value":null}`},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			line := strings.Replace(assistantTurnLine(2), `{"kind":"exit_code","value":1}`, outcome.json, 1)
			mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), line}, MapOptions{})
			command := find(t, mapped, "command.executed")
			if command.Command.ExitCode != nil {
				t.Errorf("exit code = %d for a %s outcome, want none", *command.Command.ExitCode, outcome.name)
			}
			raw := rawFx(t, command)
			if raw["process_outcome"] != outcome.name {
				t.Errorf("raw.fx.process_outcome = %v, want %q", raw["process_outcome"], outcome.name)
			}
		})
	}
}

func TestEditCarriesPathOperationAndRenderedDiff(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	edit := find(t, mapped, "file.modified")

	if edit.File == nil || edit.File.Path != "src/server.ts" {
		t.Fatalf("file = %+v", edit.File)
	}
	if edit.File.Operation != "modify" {
		t.Errorf("operation = %q, want modify", edit.File.Operation)
	}
	if edit.File.Language != "ts" {
		t.Errorf("language = %q, want ts", edit.File.Language)
	}
	if !strings.Contains(edit.File.Diff, "-export function serve() {}") ||
		!strings.Contains(edit.File.Diff, "+export function serveHealth() {}") {
		t.Fatalf("diff does not read as a unified diff:\n%s", edit.File.Diff)
	}
	if edit.File.DiffBytes != len(edit.File.Diff) || edit.File.DiffHash == "" {
		t.Errorf("diff accounting = %d bytes hash %q", edit.File.DiffBytes, edit.File.DiffHash)
	}
	if edit.Content == nil || !edit.Content.Included {
		t.Error("an edit that retained a diff has no content marker")
	}
	raw := rawFx(t, edit)
	if raw["diff_additions"] == nil || raw["diff_deletions"] == nil {
		t.Errorf("raw.fx lost fx's own line counts: %v", raw)
	}
}

// fx says which call touched which file, keyed by call id. Reading that rather than guessing from
// the tool's name is what keeps a turn that both reads and edits the same file from reporting two
// events of the same kind.
func TestFileActionsComeFromFxEvidenceNotToolNames(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	if len(findAll(mapped, "file.read")) != 1 {
		t.Fatalf("file.read events = %d, want 1", len(findAll(mapped, "file.read")))
	}
	read := find(t, mapped, "file.read")
	if read.File == nil || read.File.Path != "src/server.ts" || read.File.Operation != "read" {
		t.Fatalf("read event file block = %+v", read.File)
	}
	if read.File.Diff != "" {
		t.Error("a read carries a diff, which means the edit's evidence leaked onto it")
	}
}

// The call id is the only thing that links a call to its result, an approval to what it approved,
// and one runtime's record of an action to another's. Every tool event carries fx's own id.
func TestToolCallIDIsPromotedOnEveryToolEvent(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	want := map[string]string{
		"file.read":        "call_read_1",
		"file.modified":    "call_edit_1",
		"command.executed": "call_cmd_1",
	}
	for action, callID := range want {
		ev := find(t, mapped, action)
		if ev.GenAI == nil || ev.GenAI.Tool == nil || ev.GenAI.Tool.Call == nil {
			t.Fatalf("%s has no gen_ai.tool.call block", action)
		}
		if ev.GenAI.Tool.Call.ID != callID {
			t.Errorf("%s call id = %q, want %q", action, ev.GenAI.Tool.Call.ID, callID)
		}
		if ev.GenAI.Tool.Call.Arguments == nil {
			t.Errorf("%s carries no tool arguments", action)
		}
	}
}

// The provider fx authenticated against is part of what a session cost and where its data went, and
// it must survive every later edit to the gen_ai block.
func TestProviderSurvivesTheRestOfTheGenAIBlock(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	for _, item := range mapped {
		ev := item.Event
		if ev.GenAI == nil || ev.GenAI.Provider == nil || ev.GenAI.Provider.Name != "gateway" {
			t.Errorf("%s: gen_ai.provider = %+v, want gateway", ev.Event.Action, ev.GenAI)
		}
		if ev.Model != "anthropic/claude-opus-4" {
			t.Errorf("%s: model = %q", ev.Event.Action, ev.Model)
		}
	}
}

// A tool imported from an MCP server is not the same event as a local file edit, and fx's own
// naming (`mcp_<server>_<tool>`) is what says which is which.
func TestMCPServerToolsAreReportedAsMCPCalls(t *testing.T) {
	line := strings.NewReplacer(
		`"name":"read_file","arguments_json":"{\"path\":\"src/server.ts\"}"`,
		`"name":"mcp_github_get_issue","arguments_json":"{\"issue\":42}"`,
		`"tool_call_id":"call_read_1","tool_name":"read_file"`,
		`"tool_call_id":"call_read_1","tool_name":"mcp_github_get_issue"`,
		`{"path":"src/server.ts","new_path":null,"tool_call_id":"call_read_1","tool_name":"read_file","action":"read"`,
		`{"path":"src/server.ts","new_path":null,"tool_call_id":"unrelated","tool_name":"read_file","action":"read"`,
	).Replace(assistantTurnLine(2))

	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), line}, MapOptions{})
	mcp := find(t, mapped, "mcp.tool_invoked")
	if mcp.MCP == nil || mcp.MCP.Tool != "mcp_github_get_issue" {
		t.Fatalf("mcp block = %+v", mcp.MCP)
	}
	// fx sanitizes both halves of the name into the same underscore-separated string, so the server
	// cannot be recovered from it. Asserting a server here would be a guess presented as a fact.
	if mcp.MCP.Server != "" {
		t.Errorf("mcp.server = %q; fx's naming cannot be split back into server and tool", mcp.MCP.Server)
	}
}

// fx's own MCP control tools share the `mcp_` prefix and are not calls to a server. Reporting them
// as MCP activity would put "the agent searched its tool catalog" in the same bucket as "the agent
// called an external server".
func TestFxOwnMCPControlToolsAreNotReportedAsServerCalls(t *testing.T) {
	for _, name := range []string{"mcp_search_tools", "mcp_select_tool", "mcp_features"} {
		t.Run(name, func(t *testing.T) {
			line := strings.NewReplacer(
				`"name":"read_file"`, `"name":"`+name+`"`,
				`"tool_name":"read_file","status":"success"`, `"tool_name":"`+name+`","status":"success"`,
			).Replace(assistantTurnLine(2))
			mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), line}, MapOptions{})
			if len(findAll(mapped, "mcp.tool_invoked")) != 0 {
				t.Fatalf("%s was reported as an MCP server call", name)
			}
		})
	}
}

// A call fx never recorded a result for is the one thing a poll-based reader could plausibly drop,
// and it is the one least worth dropping: an interrupted call is more interesting than a completed
// one, not less.
func TestInterruptedTurnStillReportsTheCallInFlight(t *testing.T) {
	payload := `{"conversation_language":"en","total_input_tokens":10,"total_output_tokens":2,"turn":{` +
		`"kind":"interrupted","user":{"text":"delete the build cache","images":[]},"assistant":null,` +
		`"tool_call":{"id":"call_rm_1","name":"run_command","arguments_json":"{\"command\":\"rm -rf build\"}","provider_result":null},` +
		`"completed_tool_names":["read_file"],"terminal_reason":"user_interrupt"}}`
	line := frame(2, 1770000006000, KindHistoryTurnCommitted, payload)

	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), line}, MapOptions{})
	invoked := find(t, mapped, "tool.invoked")
	if invoked.Tool == nil || invoked.Tool.Name != "run_command" {
		t.Fatalf("tool block = %+v", invoked.Tool)
	}
	if invoked.Tool.Command != "rm -rf build" {
		t.Errorf("command = %q, want the interrupted command", invoked.Tool.Command)
	}
	if invoked.GenAI == nil || invoked.GenAI.Tool == nil || invoked.GenAI.Tool.Call.ID != "call_rm_1" {
		t.Error("the interrupted call lost its call id")
	}
	if raw := rawFx(t, invoked); raw["result_missing"] == nil {
		t.Errorf("raw.fx does not say why there is no result: %v", raw)
	}
}

// Per-turn tokens, not the session totals sitting next to them in the same record. Beacon's rollups
// sum usage across events, so emitting the cumulative totals per turn would report a session's
// whole history once per turn.
func TestTurnUsageReportsTheTurnNotTheSessionTotal(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	usage := find(t, mapped, "token.usage")

	if usage.GenAI == nil || usage.GenAI.Usage == nil {
		t.Fatal("no gen_ai.usage on the usage event")
	}
	if usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 1200 {
		t.Errorf("input tokens = %+v, want the turn's 1200 rather than the session's 4200", usage.GenAI.Usage.InputTokens)
	}
	if usage.GenAI.Usage.OutputTokens == nil || *usage.GenAI.Usage.OutputTokens != 340 {
		t.Errorf("output tokens = %+v, want the turn's 340", usage.GenAI.Usage.OutputTokens)
	}
	raw := rawFx(t, usage)
	if raw["input_exact"] != true || raw["output_exact"] != false {
		t.Errorf("raw.fx exactness = %v/%v, want fx's own flags carried through", raw["input_exact"], raw["output_exact"])
	}
}

// fx's usage snapshots are cumulative replacements. Emitting one directly would re-count everything
// before it on every checkpoint, so the event carries the difference from the previous snapshot.
func TestUsageCheckpointsAreEmittedAsDeltas(t *testing.T) {
	lines := []string{
		sessionStartedLine(1, "/repo"),
		usageCheckpointLine(2, 1000, 200, 0.10),
		usageCheckpointLine(3, 3000, 700, 0.35),
	}
	mapped := mapFixture(t, lines, MapOptions{})
	usage := findAll(mapped, "token.usage")
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want 2", len(usage))
	}
	if usage[0].GenAI.Usage.CostUSD == nil || *usage[0].GenAI.Usage.CostUSD != 0.10 {
		t.Fatalf("first checkpoint cost = %+v, want 0.10", usage[0].GenAI.Usage.CostUSD)
	}
	second := usage[1].GenAI.Usage
	if second.CostUSD == nil {
		t.Fatal("second checkpoint has no cost")
	}
	if got := *second.CostUSD; got < 0.2499 || got > 0.2501 {
		t.Errorf("second checkpoint cost = %v, want the 0.25 difference rather than the 0.35 total", got)
	}
	// The cumulative numbers are still readable, just not summed as if they were increments.
	if raw := rawFx(t, usage[1]); raw["cumulative_cost_usd"] == nil {
		t.Errorf("raw.fx dropped fx's cumulative figures: %v", raw)
	}
}

// Turn usage and checkpoint usage both write gen_ai.usage, and Beacon's rollups sum every field
// across every event. They must therefore fill disjoint fields: input/output from the turn, cache
// and cost from the checkpoint. If they ever overlap, every fx session's totals double.
func TestTurnAndCheckpointUsageDoNotOverlap(t *testing.T) {
	lines := []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2), usageCheckpointLine(3, 4200, 880, 0.5)}
	mapped := mapFixture(t, lines, MapOptions{})
	usage := findAll(mapped, "token.usage")
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want 2", len(usage))
	}
	turn, checkpoint := usage[0].GenAI.Usage, usage[1].GenAI.Usage

	if turn.InputTokens == nil || turn.OutputTokens == nil {
		t.Fatal("the turn event carries no token counts")
	}
	if turn.CostUSD != nil || turn.CacheRead != nil || turn.CacheCreation != nil {
		t.Error("the turn event claims cost or cache tokens, which the checkpoint also reports")
	}
	if checkpoint.InputTokens != nil || checkpoint.OutputTokens != nil {
		t.Error("the checkpoint event repeats input/output tokens already counted per turn")
	}
	if checkpoint.CacheRead == nil || checkpoint.CacheCreation == nil || checkpoint.CostUSD == nil {
		t.Errorf("the checkpoint event dropped the fields only it can report: %+v", checkpoint)
	}
}

// A snapshot that goes backwards is a session restored from an earlier state, not negative usage.
// A negative delta would subtract from a total that describes something else entirely.
func TestUsageThatGoesBackwardsIsNotReportedAsNegative(t *testing.T) {
	lines := []string{
		sessionStartedLine(1, "/repo"),
		usageCheckpointLine(2, 3000, 700, 0.35),
		usageCheckpointLine(3, 1000, 200, 0.10),
	}
	mapped := mapFixture(t, lines, MapOptions{})
	usage := findAll(mapped, "token.usage")
	if len(usage) != 1 {
		t.Fatalf("usage events = %d, want only the first checkpoint", len(usage))
	}
}

// A session resumed from earlier work starts with the usage it already had. Without seeding the
// baseline from it, the first checkpoint after a resume reports the whole prior session as if it
// had just happened.
func TestResumedSessionDoesNotReplayPriorUsage(t *testing.T) {
	started := strings.Replace(sessionStartedLine(1, "/repo"), `"usage":null`,
		`"usage":{"billing":"complete","total_cost":5.0,"input_tokens":100000,"output_tokens":20000,`+
			`"cache_read_tokens":800,"cache_write_tokens":100,"billable_web_search_calls":0,`+
			`"lines_added":0,"lines_removed":0,"models":[]}`, 1)
	lines := []string{started, usageCheckpointLine(2, 100500, 20100, 5.10)}

	mapped := mapFixture(t, lines, MapOptions{})
	usage := find(t, mapped, "token.usage")
	if usage.GenAI.Usage.CostUSD == nil {
		t.Fatal("no cost on the checkpoint")
	}
	if got := *usage.GenAI.Usage.CostUSD; got < 0.09 || got > 0.11 {
		t.Errorf("cost = %v, want the 0.10 spent since the resume rather than the 5.10 total", got)
	}
	if usage.GenAI.Usage.CacheRead == nil || *usage.GenAI.Usage.CacheRead.InputTokens != 100 {
		t.Errorf("cache read delta = %+v, want 100", usage.GenAI.Usage.CacheRead)
	}
}

// MinSeq is what makes a second sweep cheap and quiet. Events at or below it still build the
// mapper's state -- the model in force, the usage baseline -- but produce nothing.
func TestAlreadyCollectedEventsBuildStateWithoutBeingEmittedAgain(t *testing.T) {
	lines := []string{
		sessionStartedLine(1, "/repo"),
		usageCheckpointLine(2, 1000, 200, 0.10),
		usageCheckpointLine(3, 3000, 700, 0.35),
	}
	mapped := mapFixture(t, lines, MapOptions{MinSeq: 2})
	if len(mapped) != 1 {
		t.Fatalf("mapped %d events, want only the one past the cursor: %v", len(mapped), actions(mapped))
	}
	usage := mapped[0].Event
	if usage.GenAI.Usage.CostUSD == nil {
		t.Fatal("no cost")
	}
	// The baseline came from the checkpoint before the cursor, which was never re-emitted.
	if got := *usage.GenAI.Usage.CostUSD; got < 0.2499 || got > 0.2501 {
		t.Errorf("cost = %v, want the 0.25 delta from the checkpoint the sweep skipped", got)
	}
	// The model was set by the session_started the sweep also skipped.
	if usage.Model != "anthropic/claude-opus-4" {
		t.Errorf("model = %q, want the one the skipped session_started set", usage.Model)
	}
}

// fx writes a fresh session_started at sequence 1 of every log generation, including the one it
// creates when it compacts a log. A second start for a session that never restarted would double
// every session count on the dashboard.
func TestSessionStartCanBeSuppressedForAnAlreadyStartedSession(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{SkipSessionStarted: true})
	if len(findAll(mapped, "session.started")) != 0 {
		t.Fatal("session.started was emitted for a session already known to have started")
	}
	// Suppressing the event must not suppress the state it carries.
	prompt := find(t, mapped, "prompt.submitted")
	if prompt.Model != "anthropic/claude-opus-4" || prompt.Session.WorkingDirectory != "/repo" {
		t.Errorf("state from the suppressed session_started was lost: model %q dir %q",
			prompt.Model, prompt.Session.WorkingDirectory)
	}
}

// Dedup ids are what make re-reading fx's log idempotent, so the same record has to produce the
// same id on every sweep, and two records must never share one.
func TestDedupIDsAreStableAndUnique(t *testing.T) {
	lines := []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2), usageCheckpointLine(3, 1, 1, 0.01)}
	first := mapFixture(t, lines, MapOptions{})
	second := mapFixture(t, lines, MapOptions{})

	if len(first) != len(second) {
		t.Fatalf("two passes produced %d and %d events", len(first), len(second))
	}
	seen := map[string]bool{}
	for i := range first {
		if first[i].DedupID != second[i].DedupID {
			t.Fatalf("dedup id changed between passes: %q then %q", first[i].DedupID, second[i].DedupID)
		}
		if first[i].DedupID == "" {
			t.Fatalf("event %s has no dedup id", first[i].Event.Event.Action)
		}
		if seen[first[i].DedupID] {
			t.Fatalf("dedup id %q used twice", first[i].DedupID)
		}
		seen[first[i].DedupID] = true
	}
}

// fx does not persist an approve/deny record per call -- what it keeps is the feedback a person
// typed at a prompt. That is evidence about a decision, not the decision, and synthesizing an
// approval event from it would put a fabricated decision in the field auditors count. Same posture
// the Cline and Pi integrations already take.
func TestPermissionFeedbackIsRecordedWithoutSynthesizingAnApproval(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	for _, item := range mapped {
		if strings.HasPrefix(item.Event.Event.Action, "approval.") {
			t.Fatalf("mapper synthesized %q from fx records that contain no decision", item.Event.Event.Action)
		}
		if item.Event.Approval != nil {
			t.Fatalf("%s carries an approval block fx never recorded", item.Event.Event.Action)
		}
	}
	command := find(t, mapped, "command.executed")
	raw := rawFx(t, command)
	feedback, ok := raw["permission_feedback"].([]interface{})
	if !ok || len(feedback) != 1 {
		t.Fatalf("raw.fx.permission_feedback = %v, want fx's feedback preserved", raw["permission_feedback"])
	}
}

// A session's workspace can change: fx sessions are global and portable, and a resume in another
// directory rebinds them. Later events have to follow the session to its new root, or a file path
// gets read against the wrong tree.
func TestWorkspaceRebindMovesLaterEventsToTheNewRoot(t *testing.T) {
	rebound := frame(2, 1770000002000, KindWorkspaceRebound,
		`{"previous_workspace_root":"/repo","workspace_root":"/other/checkout"}`)
	lines := []string{sessionStartedLine(1, "/repo"), rebound, assistantTurnLine(3)}

	mapped := mapFixture(t, lines, MapOptions{})
	context := find(t, mapped, "session.context")
	if raw := rawFx(t, context); raw["change"] != "workspace_rebound" {
		t.Errorf("raw.fx.change = %v", raw["change"])
	}
	prompt := find(t, mapped, "prompt.submitted")
	if prompt.Session.WorkingDirectory != "/other/checkout" {
		t.Errorf("working directory = %q, want the rebound root", prompt.Session.WorkingDirectory)
	}
}

// Switching model or provider mid-session changes what a session costs and where its prompts went.
// It is recorded under session.context, the action the Codex integration already uses for session
// metadata, rather than under a verb only fx produces.
func TestPreferencesChangeIsRecordedAndAppliesToLaterEvents(t *testing.T) {
	changed := frame(2, 1770000002000, KindPreferencesChanged,
		`{"provider":"grok","model":"grok-4","effort":"high","fast_mode":true}`)
	lines := []string{sessionStartedLine(1, "/repo"), changed, assistantTurnLine(3)}

	mapped := mapFixture(t, lines, MapOptions{})
	context := find(t, mapped, "session.context")
	raw := rawFx(t, context)
	if raw["change"] != "preferences_changed" || raw["model"] != "grok-4" {
		t.Errorf("raw.fx = %v", raw)
	}
	prompt := find(t, mapped, "prompt.submitted")
	if prompt.Model != "grok-4" {
		t.Errorf("model on a later event = %q, want grok-4", prompt.Model)
	}
	if prompt.GenAI.Provider.Name != "grok" {
		t.Errorf("provider on a later event = %q, want grok", prompt.GenAI.Provider.Name)
	}
}

// fx compacting its own history means the record thins out from that point back, and a reader
// reconstructing a session needs to see where.
func TestHistoryCompactionIsRecorded(t *testing.T) {
	payload := `{"conversation_language":"en","total_input_tokens":10,"total_output_tokens":2,"turn":{` +
		`"kind":"compacted_summary","summary":"Earlier: set up the API and its tests.","removed_turn_count":12,"compaction_count":1}}`
	lines := []string{sessionStartedLine(1, "/repo"), frame(2, 1770000007000, KindHistoryTurnCommitted, payload)}

	mapped := mapFixture(t, lines, MapOptions{})
	compaction := find(t, mapped, "session.compacting")
	raw := rawFx(t, compaction)
	if raw["removed_turn_count"] == nil {
		t.Fatalf("raw.fx = %v, want the dropped-turn count", raw)
	}
}

// Text that is not valid UTF-8 reaches Beacon as fx's base64 form, and it must survive into an
// event rather than emptying the field or failing the line. An event that says a tool returned
// nothing when it returned bytes is worse than one carrying a replacement character.
func TestNonUTF8CommandOutputSurvivesIntoTheEvent(t *testing.T) {
	line := strings.Replace(assistantTurnLine(2),
		`"status":"failure","output":"1 test failed"`,
		`"status":"failure","output":{"encoding":"base64","data":"/w=="}`, 1)
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), line}, MapOptions{})
	command := find(t, mapped, "command.executed")
	if command.Command.Output == "" {
		t.Fatal("non-UTF-8 output was dropped, which reads as a command that printed nothing")
	}
	if command.Content == nil || command.Content.Bytes != 1 {
		t.Errorf("content marker = %+v, want one byte retained", command.Content)
	}
}

func rawFx(t *testing.T, ev schema.Event) map[string]interface{} {
	t.Helper()
	if ev.Raw == nil {
		t.Fatalf("%s has no raw block", ev.Event.Action)
	}
	// Round-tripped so the assertions read the same shape a consumer of the log would.
	data, err := json.Marshal(ev.Raw["fx"])
	if err != nil {
		t.Fatalf("marshal raw.fx: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode raw.fx: %v", err)
	}
	return out
}

// turnWithoutSummaryLine is the shape fx 0.0.7 commits: an execution block with no turn summary,
// and the session's cumulative token totals on the record. See testdata/fx-0.0.7-session for the
// captured original.
func turnWithoutSummaryLine(seq int, totalInput, totalOutput int64) string {
	payload := fmt.Sprintf(
		`{"conversation_language":"und-Latn","total_input_tokens":%d,"total_output_tokens":%d,"turn":{`+
			`"kind":"assistant","user":{"text":"turn %d","images":[]},"assistant":"done",`+
			`"execution":{"schema_version":4,"tool_steps":[],"files":[]}}}`,
		totalInput, totalOutput, seq)
	return frame(seq, 1770000000000+int64(seq)*1000, KindHistoryTurnCommitted, payload)
}

// On a build that writes no turn summary, each turn's usage is the increase in the session's
// cumulative totals -- not the totals themselves, which would report the whole history once per
// turn in any rollup that sums events.
func TestTurnsWithoutASummaryReportTheIncreaseNotTheRunningTotal(t *testing.T) {
	lines := []string{
		sessionStartedLine(1, "/repo"),
		turnWithoutSummaryLine(2, 1000, 200),
		turnWithoutSummaryLine(3, 2500, 450),
	}
	mapped := mapFixture(t, lines, MapOptions{})
	usage := findAll(mapped, "token.usage")
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want one per turn", len(usage))
	}
	if got := *usage[0].GenAI.Usage.InputTokens; got != 1000 {
		t.Errorf("first turn input tokens = %d, want 1000", got)
	}
	if got := *usage[1].GenAI.Usage.InputTokens; got != 1500 {
		t.Errorf("second turn input tokens = %d, want the 1500 increase rather than the 2500 total", got)
	}
	if got := *usage[1].GenAI.Usage.OutputTokens; got != 250 {
		t.Errorf("second turn output tokens = %d, want 250", got)
	}
	if source := rawFx(t, usage[1])["token_source"]; source != "session_totals_delta" {
		t.Errorf("raw.fx.token_source = %v, want session_totals_delta", source)
	}
}

// The baseline has to carry across turns a sweep is not re-emitting. Without that, the first turn a
// resumed sweep emits reports the session's entire history as its own usage -- the exact
// double-count the delta exists to prevent.
func TestResumedSweepMeasuresAgainstTheTurnBeforeTheCursor(t *testing.T) {
	lines := []string{
		sessionStartedLine(1, "/repo"),
		turnWithoutSummaryLine(2, 1000, 200),
		turnWithoutSummaryLine(3, 2500, 450),
	}
	mapped := mapFixture(t, lines, MapOptions{MinSeq: 2})
	usage := findAll(mapped, "token.usage")
	if len(usage) != 1 {
		t.Fatalf("usage events = %d, want only the turn past the cursor", len(usage))
	}
	if got := *usage[0].GenAI.Usage.InputTokens; got != 1500 {
		t.Fatalf("input tokens = %d, want the 1500 increase over the skipped turn, not the 2500 total", got)
	}
}

// A turn summary, when fx writes one, still wins: it is the runtime's own per-turn count with its
// exactness flags, and the cumulative difference is only a fallback.
func TestATurnSummaryIsPreferredOverTheCumulativeFallback(t *testing.T) {
	mapped := mapFixture(t, []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}, MapOptions{})
	usage := find(t, mapped, "token.usage")
	if source := rawFx(t, usage)["token_source"]; source != "turn_summary" {
		t.Fatalf("raw.fx.token_source = %v, want turn_summary", source)
	}
	if got := *usage.GenAI.Usage.InputTokens; got != 1200 {
		t.Errorf("input tokens = %d, want the summary's 1200 rather than the record's 4200 total", got)
	}
}
