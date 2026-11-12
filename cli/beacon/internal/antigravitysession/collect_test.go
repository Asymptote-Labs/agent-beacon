package antigravitysession

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const fixtureConversation = "11111111-2222-4333-8444-555555555555"

// fixtureSteps is a two-turn conversation shaped like the one agy 1.5.3 commits: a prompt wrapped
// in USER_REQUEST/ADDITIONAL_METADATA/USER_SETTINGS_CHANGE, a planner response with reasoning and a
// run_command call, the call's GENERIC result, the reply, then a resumed turn with a system notice
// before its reply.
var fixtureSteps = []string{
	`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-05-01T10:00:00Z","content":"<USER_REQUEST>\nRun: cat notes.txt\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-05-01T06:00:00-04:00.\n</ADDITIONAL_METADATA>\n<USER_SETTINGS_CHANGE>\nThe user changed setting ` + "`Model Selection`" + `.\n</USER_SETTINGS_CHANGE>"}`,
	`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-01T10:00:01Z","input_tokens":5000,"cache_read_tokens":1200,"output_tokens":150,"thinking":"Run the command.","tool_calls":[{"name":"run_command","args":{"CommandLine":"cat notes.txt","Cwd":"/work/probe","WaitMsBeforeAsync":5000}}]}`,
	`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-05-01T10:00:03Z","content":"Created At: 2026-05-01T06:00:03-04:00\nCompleted At: 2026-05-01T06:00:04-04:00\nCommand exited with code 0.\n\nCommand output:\nhello\r\n"}`,
	`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-01T10:00:04Z","input_tokens":5300,"cache_read_tokens":0,"output_tokens":20,"content":"hello"}`,
	`{"step_index":4,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-05-01T10:05:00Z","content":"<USER_REQUEST>\nNow reply with: done\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-05-01T06:05:00-04:00.\n</ADDITIONAL_METADATA>"}`,
	`{"step_index":5,"source":"SYSTEM","type":"SYSTEM_MESSAGE","status":"DONE","created_at":"2026-05-01T10:05:00Z","content":"<SYSTEM_MESSAGE>notice</SYSTEM_MESSAGE>"}`,
	`{"step_index":6,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-01T10:05:01Z","input_tokens":6000,"cache_read_tokens":0,"output_tokens":5,"content":"done"}`,
}

// fixtureEvents is what fixtureSteps maps to, in order.
var fixtureEvents = []string{
	"session.started",
	"prompt.submitted",
	"agent.reasoning", "tool.invoked", "token.usage",
	"command.executed",
	"agent.message", "token.usage",
	"prompt.submitted",
	"agent.message", "token.usage",
}

type fixture struct {
	brainDir  string
	statePath string
	logPath   string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	return fixture{
		brainDir:  filepath.Join(root, "antigravity-cli", "brain"),
		statePath: filepath.Join(root, "state", "antigravity.json"),
		logPath:   filepath.Join(root, "logs", "runtime.jsonl"),
	}
}

func (f fixture) options() CollectOptions {
	return CollectOptions{BrainDir: f.brainDir, StatePath: f.statePath, LogPath: f.logPath, Write: true, UserMode: true}
}

func (f fixture) transcript(id, name string) string {
	return filepath.Join(f.brainDir, id, ".system_generated", "logs", name)
}

// writeConversation writes a conversation's transcript under the given file name.
func (f fixture) writeConversation(t *testing.T, id, name string, steps []string) string {
	t.Helper()
	path := f.transcript(id, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Join(steps, "\n")+"\n")
	return path
}

// writeSummaries creates conversation_summaries.db beside the brain directory with the columns
// Beacon reads, one row per id, each with the given workspace URIs.
func (f fixture) writeSummaries(t *testing.T, rows map[string]string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(f.brainDir), "conversation_summaries.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE `conversation_summaries` (`conversation_id` text,`title` text NOT NULL DEFAULT \"\",`workspace_uris` text NOT NULL,`project_id` text NOT NULL DEFAULT \"\",`parent_conversation_id` text NOT NULL DEFAULT \"\",`nesting_depth` integer NOT NULL DEFAULT 0,`blob` blob)"); err != nil {
		t.Fatal(err)
	}
	for id, uris := range rows {
		if _, err := db.Exec("INSERT INTO conversation_summaries (conversation_id, title, workspace_uris, project_id, parent_conversation_id, nesting_depth, blob) VALUES (?, ?, ?, ?, ?, ?, ?)",
			id, "Probe", uris, "default-cli-project", "99999999-0000-4000-8000-000000000000", 1, []byte{0x0a, 0x01}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreListsAndMapsAConversation(t *testing.T) {
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	f.writeSummaries(t, map[string]string{fixtureConversation: `["file:///work/agy%20probe","file:///work/other"]`})

	refs, err := NewStore(f.brainDir).List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("List() = %d refs, %v", len(refs), err)
	}
	ref := refs[0]
	if ref.ID != fixtureConversation || !ref.Full || ref.Summary == nil || ref.Summary.WorkingDirectory != "/work/agy probe" {
		t.Fatalf("ref = %+v summary=%+v", ref, ref.Summary)
	}
	data, stats, err := NewStore(f.brainDir).Read(ref)
	if err != nil || stats.Malformed != 0 {
		t.Fatalf("Read() = %v, %d malformed", err, stats.Malformed)
	}
	mapped := MapSession(data, MapOptions{})
	if got := actionsOf(mapped); strings.Join(got, ",") != strings.Join(fixtureEvents, ",") {
		t.Fatalf("actions = %v\nwant      %v", got, fixtureEvents)
	}
	for _, item := range mapped {
		ev := item.Event
		if ev.Harness.Name != "antigravity_cli" || ev.Harness.CollectionMethod != "poll" || ev.Event.Fidelity != "observed" {
			t.Fatalf("%s provenance = %+v %+v", ev.Event.Action, ev.Harness, ev.Event)
		}
		if ev.Session == nil || ev.Session.ID != fixtureConversation || ev.Session.WorkingDirectory != "/work/agy probe" {
			t.Fatalf("%s session = %+v", ev.Event.Action, ev.Session)
		}
		if ev.Event.ID == "" {
			t.Fatalf("%s has no id", ev.Event.Action)
		}
	}
	// The harness name the collector writes is the one the hooks' platform name normalizes to.
	if asymptoteobserve.NormalizeHarnessName("antigravity") != Harness {
		t.Fatalf("NormalizeHarnessName(antigravity) = %q, want %q", asymptoteobserve.NormalizeHarnessName("antigravity"), Harness)
	}

	prompts := byAction(mapped, "prompt.submitted")
	if prompts[0].Prompt.Text != "Run: cat notes.txt" || prompts[1].Prompt.Text != "Now reply with: done" {
		t.Fatalf("prompts = %q, %q", prompts[0].Prompt.Text, prompts[1].Prompt.Text)
	}
	if prompts[0].Timestamp != "2026-05-01T10:00:00.000000000Z" {
		t.Fatalf("prompt timestamp = %q, want the step's created_at", prompts[0].Timestamp)
	}

	invoked := byAction(mapped, "tool.invoked")[0]
	if invoked.Tool.Name != "run_command" || invoked.Command == nil || invoked.Command.Command != "cat notes.txt" ||
		callArgs(invoked)["Cwd"] != "/work/probe" {
		t.Fatalf("tool.invoked = %+v %+v", invoked.Tool, invoked.GenAI.Tool)
	}
	command := byAction(mapped, "command.executed")[0]
	if command.Command == nil || command.Command.Command != "cat notes.txt" || command.Command.Output != "hello" ||
		command.Command.ExitCode == nil || *command.Command.ExitCode != 0 {
		t.Fatalf("command = %+v", command.Command)
	}
	if command.Timestamp != "2026-05-01T10:00:03.000000000Z" {
		t.Fatalf("command timestamp = %q, want the GENERIC step's created_at", command.Timestamp)
	}

	usage := byAction(mapped, "token.usage")[0].GenAI.Usage
	if usage == nil || *usage.InputTokens != 3800 || *usage.CacheRead.InputTokens != 1200 || *usage.OutputTokens != 150 {
		t.Fatalf("usage = %+v, want uncached input 3800, cache read 1200, output 150", usage)
	}
	reasoning := byAction(mapped, "agent.reasoning")[0]
	if reasoning.Content == nil || reasoning.GenAI == nil || reasoning.GenAI.Output == nil {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if messages := byAction(mapped, "agent.message"); messageContent(messages[0]) != "hello" || messageContent(messages[1]) != "done" {
		t.Fatalf("messages = %+v", messages)
	}

	started := byAction(mapped, "session.started")[0]
	detail, _ := started.Raw["antigravity"].(map[string]interface{})
	if detail["title"] != "Probe" || detail["project_id"] != "default-cli-project" ||
		detail["parent_conversation_id"] != "99999999-0000-4000-8000-000000000000" || detail["nesting_depth"] != int64(1) {
		t.Fatalf("session.started raw = %v", started.Raw)
	}
	for _, action := range []string{"session.ended", "approval.requested", "approval.allowed"} {
		if len(byAction(mapped, action)) != 0 {
			t.Fatalf("%s was invented", action)
		}
	}
}

// The full transcript is preferred, and transcript.jsonl -- whose argument values are JSON-encoded
// strings -- is read only without it. The two hold the same steps, so they map to the same events
// under the same ids.
func TestTheShortTranscriptIsReadWhenTheFullOneIsAbsentAndMapsTheSame(t *testing.T) {
	full := newFixture(t)
	full.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	short := newFixture(t)
	encoded := append([]string{}, fixtureSteps...)
	encoded[1] = `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-01T10:00:01Z","input_tokens":5000,"cache_read_tokens":1200,"output_tokens":150,"thinking":"Run the command.","tool_calls":[{"name":"run_command","args":{"CommandLine":"\"cat notes.txt\"","Cwd":"\"/work/probe\"","WaitMsBeforeAsync":"5000"}}]}`
	short.writeConversation(t, fixtureConversation, transcriptName, encoded)

	fullEvents := mapAll(t, full.brainDir)
	shortRefs, _ := NewStore(short.brainDir).List()
	if len(shortRefs) != 1 || shortRefs[0].Full {
		t.Fatalf("refs = %+v, want the short transcript", shortRefs)
	}
	shortEvents := mapAll(t, short.brainDir)
	if len(fullEvents) != len(shortEvents) {
		t.Fatalf("%d events from the full transcript, %d from the short one", len(fullEvents), len(shortEvents))
	}
	for i := range fullEvents {
		if fullEvents[i].Event.Event.ID != shortEvents[i].Event.Event.ID {
			t.Fatalf("event %d (%s) has a different id read from each transcript", i, fullEvents[i].Event.Event.Action)
		}
	}
	invoked := byAction(shortEvents, "tool.invoked")[0]
	args := callArgs(invoked)
	if invoked.Command.Command != "cat notes.txt" || args["Cwd"] != "/work/probe" || args["WaitMsBeforeAsync"] != float64(5000) {
		t.Fatalf("decoded arguments = %v, command %q", args, invoked.Command.Command)
	}
}

func callArgs(ev schema.Event) map[string]interface{} {
	args, _ := ev.GenAI.Tool.Call.Arguments.(map[string]interface{})
	return args
}

// messageContent is the text of an event's first output message part, in the gen_ai.output.messages
// shape the dashboard and trace readers expect.
func messageContent(ev schema.Event) interface{} {
	messages, _ := ev.GenAI.Output.Messages.([]interface{})
	if len(messages) == 0 {
		return nil
	}
	message, _ := messages[0].(map[string]interface{})
	parts, _ := message["parts"].([]interface{})
	if len(parts) == 0 {
		return nil
	}
	part, _ := parts[0].(map[string]interface{})
	return part["content"]
}

// Without the summaries database, or without the conversation's row in it, there is no working
// directory to report -- except the one a command names -- and the conversation is still collected.
func TestAMissingSummaryMeansNoWorkingDirectoryNotAnError(t *testing.T) {
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	check := func(label string) {
		t.Helper()
		events := mapAll(t, f.brainDir)
		if len(events) != len(fixtureEvents) {
			t.Fatalf("%s: %d events, want %d", label, len(events), len(fixtureEvents))
		}
		for _, item := range events {
			want := ""
			if item.Event.Command != nil {
				want = "/work/probe"
			}
			if item.Event.Session.WorkingDirectory != want {
				t.Fatalf("%s: %s working directory = %q, want %q", label, item.Event.Event.Action, item.Event.Session.WorkingDirectory, want)
			}
		}
	}
	check("no database")
	f.writeSummaries(t, map[string]string{"another-conversation": `["file:///elsewhere"]`})
	check("no row")
}

// Antigravity on Windows records workspaces as file:///C:/... URIs, and a share as file://host/...;
// the working directory is the drive or UNC path, not the URI's path with its leading slash.
func TestWorkspaceURIsBecomeLocalPaths(t *testing.T) {
	for raw, want := range map[string]string{
		`["file:///C:/Users/dev/proj"]`:                filepath.FromSlash("C:/Users/dev/proj"),
		`["file://server/share/proj"]`:                 filepath.FromSlash("//server/share/proj"),
		`["file:///tmp/agy%20probe", "file:///other"]`: filepath.FromSlash("/tmp/agy probe"),
		`["https://example.com/repo"]`:                 "",
		`not json`:                                     "",
	} {
		if got := firstWorkspacePath(raw); got != want {
			t.Errorf("firstWorkspacePath(%s) = %q, want %q", raw, got, want)
		}
	}
}

// A summaries database that exists but cannot be read costs the working directories, not the
// conversations: the sweep collects every transcript and reports the database error.
func TestAnUnreadableSummariesDatabaseStillCollectsTheTranscripts(t *testing.T) {
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	writeFile(t, filepath.Join(filepath.Dir(f.brainDir), summariesDBName), "this is not a SQLite database, but it is long enough to have a header")

	summary, err := CollectOnce(f.options())
	if err == nil || summary.Errors != 1 {
		t.Fatalf("CollectOnce = %+v, %v; want the database error reported", summary, err)
	}
	if summary.EventsEmitted != len(fixtureEvents) {
		t.Fatalf("events emitted = %d, want every transcript event (%d)", summary.EventsEmitted, len(fixtureEvents))
	}
}

// A transcript is appended to while the conversation runs and again when it is resumed. Only the
// steps that were added may be emitted; a planner line written before its GENERIC result is
// completed by the result alone on the next sweep.
func TestCollectOnceEmitsOnlyNewStepsWhenTheConversationGrows(t *testing.T) {
	f := newFixture(t)
	path := f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps[:2])

	first, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	if got := logActions(t, f.logPath); strings.Join(got, ",") != strings.Join(fixtureEvents[:5], ",") {
		t.Fatalf("first sweep wrote %v", got)
	}

	for _, step := range fixtureSteps[2:] {
		appendLine(t, path, step)
	}
	touchNewer(t, path)
	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	if first.EventsEmitted+second.EventsEmitted != len(fixtureEvents) {
		t.Fatalf("sweeps emitted %d + %d events, want %d in all", first.EventsEmitted, second.EventsEmitted, len(fixtureEvents))
	}
	if got := logActions(t, f.logPath); strings.Join(got, ",") != strings.Join(fixtureEvents, ",") {
		t.Fatalf("log holds %v\nwant      %v", got, fixtureEvents)
	}

	touchNewer(t, path)
	third, err := CollectOnce(f.options())
	if err != nil || third.EventsEmitted != 0 {
		t.Fatalf("third sweep emitted %d events (%v), want none", third.EventsEmitted, err)
	}
}

// A sweep can catch the tail of a live transcript mid-write. The torn line counts as malformed,
// and once it lands it is collected rather than skipped.
func TestCollectOnceCollectsALineThatWasTornOnTheFirstSweep(t *testing.T) {
	f := newFixture(t)
	path := f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps[:4])
	appendLine(t, path, `{"step_index":4,"source":"USER_EXPLICIT","type":"USER_IN`)

	first, err := CollectOnce(f.options())
	if err != nil || first.MalformedLines != 1 {
		t.Fatalf("first sweep: %v, %d malformed", err, first.MalformedLines)
	}
	rewriteLine(t, path, 5, fixtureSteps[4])
	for _, step := range fixtureSteps[5:] {
		appendLine(t, path, step)
	}
	touchNewer(t, path)
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatal(err)
	}
	if got := logActions(t, f.logPath); strings.Join(got, ",") != strings.Join(fixtureEvents, ",") {
		t.Fatalf("log holds %v\nwant      %v", got, fixtureEvents)
	}
}

// A transcript that comes back shorter than the cursor was rewritten, and its content is read
// again rather than skipped forever; session.started is not written a second time.
func TestARewrittenShorterTranscriptIsNotSkipped(t *testing.T) {
	f := newFixture(t)
	path := f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Join([]string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-05-02T09:00:00Z","content":"<USER_REQUEST>\nrewritten\n</USER_REQUEST>"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-05-02T09:00:01Z","content":"rewritten reply"}`,
	}, "\n")+"\n")
	touchNewer(t, path)

	second, err := CollectOnce(f.options())
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsEmitted != 2 {
		t.Fatalf("sweep after the rewrite emitted %d events, want the rewritten prompt and reply", second.EventsEmitted)
	}
	actions := countActions(logActions(t, f.logPath))
	if actions["session.started"] != 1 {
		t.Fatalf("session.started written %d times, want 1", actions["session.started"])
	}
	touchNewer(t, path)
	if third, err := CollectOnce(f.options()); err != nil || third.EventsEmitted != 0 {
		t.Fatalf("third sweep emitted %d events (%v), want none", third.EventsEmitted, err)
	}
}

// One malformed line in the middle leaves the step count below the line cursor; that is not a
// rewrite, and the conversation must not be appended to the log again.
func TestAMalformedLineIsNotMistakenForARewrite(t *testing.T) {
	f := newFixture(t)
	steps := append(append(append([]string{}, fixtureSteps[:2]...), `{"step_index":`), fixtureSteps[2:]...)
	path := f.writeConversation(t, fixtureConversation, fullTranscriptName, steps)
	first, err := CollectOnce(f.options())
	if err != nil || first.MalformedLines != 1 {
		t.Fatalf("first sweep: %v, %d malformed", err, first.MalformedLines)
	}
	appendLine(t, path, `{"step_index":7,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-05-01T10:10:00Z","content":"<USER_REQUEST>\nthird\n</USER_REQUEST>"}`)
	touchNewer(t, path)
	second, err := CollectOnce(f.options())
	if err != nil || second.EventsEmitted != 1 {
		t.Fatalf("second sweep emitted %d events (%v), want only the appended prompt", second.EventsEmitted, err)
	}
}

// Since keeps the sweep to recently modified conversations and leaves the cursors of the rest
// untouched, so a later sync still collects them.
func TestSinceLeavesOlderConversationsForALaterSync(t *testing.T) {
	f := newFixture(t)
	oldPath := f.writeConversation(t, "old-conversation", fullTranscriptName, fixtureSteps)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	past := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatal(err)
	}

	opts := f.options()
	opts.Since = time.Now().Add(-30 * 24 * time.Hour)
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sessions != 1 || summary.EventsEmitted != len(fixtureEvents) {
		t.Fatalf("windowed sweep = %+v, want only the recent conversation", summary)
	}
	state, err := LoadState(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions["old-conversation"]; ok {
		t.Fatal("the windowed sweep moved the cursor of a conversation it did not read")
	}

	summary, err = CollectOnce(f.options())
	if err != nil || summary.EventsEmitted != len(fixtureEvents) {
		t.Fatalf("unwindowed sweep emitted %d events (%v), want the older conversation", summary.EventsEmitted, err)
	}
}

// A spent budget stops the sweep with the cursor past only the lines whose events were all
// written; the next sweep writes the rest, and nothing twice.
func TestABudgetStopsTheSweepWithTheCursorPastOnlyWhatWasWritten(t *testing.T) {
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)

	probe := newFixture(t)
	probe.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps[:1])
	probeBudget := writer.NewBudget(1 << 30)
	probeOpts := probe.options()
	probeOpts.Budget = probeBudget
	if _, err := CollectOnce(probeOpts); err != nil {
		t.Fatal(err)
	}

	// Room for session.started and the first prompt, and not for the planner response's events.
	opts := f.options()
	opts.Budget = writer.NewBudget(probeBudget.Used() + 64)
	summary, err := CollectOnce(opts)
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("err = %v, want ErrBudgetSpent", err)
	}
	if summary.Errors != 0 || summary.EventsEmitted == 0 || summary.EventsEmitted >= len(fixtureEvents) {
		t.Fatalf("budgeted sweep = %+v", summary)
	}

	rest, err := CollectOnce(f.options())
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted+rest.EventsEmitted < len(fixtureEvents) {
		t.Fatalf("the two sweeps wrote %d + %d events, want at least %d", summary.EventsEmitted, rest.EventsEmitted, len(fixtureEvents))
	}
	ids := map[string]int{}
	for _, ev := range readLog(t, f.logPath) {
		ids[ev.Event.ID]++
	}
	if len(ids) != len(fixtureEvents) {
		t.Fatalf("log holds %d distinct events, want %d", len(ids), len(fixtureEvents))
	}
}

// A conversation the hooks covered from its start, whose hooks recorded only its first prompt --
// what Beacon v1.3.60's hooks do, having no PreInvocation handler. The sweep suppresses the session
// start, the recorded prompt and the tool events, keeps what hooks cannot carry (agent messages,
// reasoning, token usage), and backfills the second prompt the hooks never saw. The cursor moves
// past the suppressed events, so they are not retried.
func TestLiveCoverageBackfillsThePromptsTheHooksDidNotRecord(t *testing.T) {
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	opts := f.options()
	opts.LiveCoverage = sessionsync.LiveCoverage{
		From:    map[string]time.Time{fixtureConversation: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)},
		Prompts: map[string]map[string]bool{fixtureConversation: {asymptoteobserve.RetainedContent("Run: cat notes.txt", 0).Hash: true}},
	}

	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		// session.started, the first prompt, tool.invoked and command.executed are the hooks'.
		"agent.reasoning", "token.usage",
		"agent.message", "token.usage",
		"prompt.submitted",
		"agent.message", "token.usage",
	}
	if got := logActions(t, f.logPath); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("log holds %v\nwant      %v", got, want)
	}
	if summary.EventsEmitted != len(want) || summary.EventsSuppressedLive != 4 {
		t.Fatalf("summary = %+v, want %d emitted and 4 suppressed", summary, len(want))
	}

	touchNewer(t, f.transcript(fixtureConversation, fullTranscriptName))
	again, err := CollectOnce(opts)
	if err != nil || again.EventsEmitted != 0 || again.EventsSuppressedLive != 0 {
		t.Fatalf("second sweep = %+v (%v), want nothing re-read", again, err)
	}

	// Coverage for another conversation changes nothing here.
	other := newFixture(t)
	other.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	otherOpts := other.options()
	otherOpts.LiveCoverage = sessionsync.LiveCoverage{From: map[string]time.Time{"another-conversation": time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}}
	if s, err := CollectOnce(otherOpts); err != nil || s.EventsEmitted != len(fixtureEvents) || s.EventsSuppressedLive != 0 {
		t.Fatalf("uncovered conversation = %+v (%v)", s, err)
	}
}

// `beacon endpoint antigravity sync --print` maps and prints every event without writing or
// advancing a cursor, so a real sync afterwards writes the same events.
func TestPrintShowsEveryEventAndLeavesTheConversationForSync(t *testing.T) {
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, fixtureSteps)
	var printed strings.Builder
	summary, err := CollectOnce(CollectOptions{BrainDir: f.brainDir, Print: true, Out: &printed})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(printed.String()), "\n")
	if summary.EventsEmitted != len(fixtureEvents) || len(lines) != len(fixtureEvents) {
		t.Fatalf("printed %d lines for %d events", len(lines), summary.EventsEmitted)
	}
	real, err := CollectOnce(f.options())
	if err != nil || real.EventsEmitted != len(fixtureEvents) {
		t.Fatalf("sync after --print wrote %d events (%v), want %d", real.EventsEmitted, err, len(fixtureEvents))
	}
}

func TestMapSessionRecordsAHandoffLink(t *testing.T) {
	marker := asymptoteobserve.HandoffMarker("claude_code", "src-1")
	content, _ := json.Marshal("<USER_REQUEST>\nContinue the work.\n\n" + marker + "\n</USER_REQUEST>")
	f := newFixture(t)
	f.writeConversation(t, fixtureConversation, fullTranscriptName, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-05-01T10:00:00Z","content":` + string(content) + `}`,
	})
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatal(err)
	}
	var links []schema.Event
	for _, ev := range readLog(t, f.logPath) {
		if ev.Event.Action == "session.handoff" {
			links = append(links, ev)
		}
	}
	if len(links) != 1 || links[0].Handoff == nil || links[0].Handoff.SourceHarness != "claude_code" || links[0].Handoff.SourceSessionID != "src-1" {
		t.Fatalf("links = %+v", links)
	}
	if links[0].Prompt != nil || links[0].Session.ID != fixtureConversation {
		t.Fatalf("link = %+v", links[0])
	}

	// Collected again from scratch, the link carries the same id.
	if err := os.Remove(f.statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectOnce(f.options()); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, ev := range readLog(t, f.logPath) {
		if ev.Event.Action == "session.handoff" {
			ids = append(ids, ev.Event.ID)
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("link ids = %q, want the same id both times", ids)
	}
}

// Results pair with calls by order: two calls in one planner response take the next two GENERIC
// steps, and a call whose result never came does not take the next turn's step.
func TestGenericStepsPairWithCallsInOrder(t *testing.T) {
	data := SessionData{Ref: SessionRef{ID: "c"}, Steps: []Step{
		{Type: stepPlannerResponse, Line: 1, ToolCalls: []ToolCall{
			{Name: "view_file", Arguments: map[string]interface{}{"AbsolutePath": "/w/a.go"}},
			{Name: "write_to_file", Arguments: map[string]interface{}{"TargetFile": "/w/b.go"}},
		}},
		{Type: stepGeneric, Line: 2, Content: "package a"},
		{Type: stepGeneric, Line: 3, Content: "Created file"},
		{Type: stepPlannerResponse, Line: 4, ToolCalls: []ToolCall{{Name: "replace_file_content", Arguments: map[string]interface{}{"TargetFile": "/w/c.go"}}}},
		{Type: "USER_INPUT", Line: 5, Content: "<USER_REQUEST>next</USER_REQUEST>"},
		{Type: stepGeneric, Line: 6, Content: "unpaired"},
		{Type: stepPlannerResponse, Line: 7, ToolCalls: []ToolCall{{Name: "mcp_github_search", Arguments: map[string]interface{}{"q": "x"}}}},
		{Type: stepGeneric, Line: 8, Content: "results"},
	}}
	var results []schema.Event
	for _, item := range MapSession(data, MapOptions{SkipStarted: true}) {
		if item.Event.Event.Action != "tool.invoked" && item.Event.Event.Action != "prompt.submitted" {
			results = append(results, item.Event)
		}
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3: %+v", len(results), results)
	}
	read, write, mcp := results[0], results[1], results[2]
	if read.Event.Action != "file.read" || read.File.Path != "/w/a.go" || read.File.Operation != "read" || read.GenAI.Tool.Call.Result != "package a" {
		t.Fatalf("read = %+v %+v", read.Event, read.File)
	}
	if write.Event.Action != "file.modified" || write.File.Path != "/w/b.go" || write.File.Operation != "write" {
		t.Fatalf("write = %+v %+v", write.Event, write.File)
	}
	if mcp.Event.Action != "mcp.tool_invoked" || mcp.MCP == nil || mcp.MCP.Tool != "mcp_github_search" {
		t.Fatalf("mcp = %+v", mcp)
	}
}

func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	f := newFixture(t)
	for i := range 60 {
		f.writeConversation(t, fmt.Sprintf("conversation-%03d", i), fullTranscriptName, fixtureSteps)
	}
	opts := CollectOptions{BrainDir: f.brainDir, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}

func mapAll(t *testing.T, brainDir string) []MappedEvent {
	t.Helper()
	store := NewStore(brainDir)
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []MappedEvent
	for _, ref := range refs {
		data, _, err := store.Read(ref)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, MapSession(data, MapOptions{})...)
	}
	return out
}

func actionsOf(mapped []MappedEvent) []string {
	out := make([]string, 0, len(mapped))
	for _, item := range mapped {
		out = append(out, item.Event.Event.Action)
	}
	return out
}

func byAction(mapped []MappedEvent, action string) []schema.Event {
	var out []schema.Event
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			out = append(out, item.Event)
		}
	}
	return out
}

func readLog(t *testing.T, path string) []schema.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []schema.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal runtime line: %v", err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func logActions(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, ev := range readLog(t, path) {
		out = append(out, ev.Event.Action)
	}
	return out
}

func countActions(actions []string) map[string]int {
	out := map[string]int{}
	for _, action := range actions {
		out[action]++
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func rewriteLine(t *testing.T, path string, lineNo int, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	lines[lineNo-1] = content
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
}

// touchNewer moves the transcript's modification time forward, so a test that writes twice inside
// one filesystem timestamp tick is not skipped by the collector's modification-time pre-filter.
func touchNewer(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	next := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatal(err)
	}
}
