package fxsession

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// This test runs against a session record fx actually wrote.
//
// testdata/fx-0.0.7-session holds the events.jsonl and session.json left behind by the released fx
// 0.0.7 binary driving a real turn -- a command that exits 3, a file read, a file write -- against a
// local fake gateway. The only edit is the workspace path, rewritten to /home/dev/api so the fixture
// does not carry the machine it was captured on (and the manifest's byte watermark adjusted to
// match, since the rewrite changed the file's length).
//
// It exists because hand-transcribing another project's format is exactly the kind of work that
// looks right and is wrong, and because this capture found two real defects the transcribed
// fixtures could not: fx 0.0.7 writes no turn summary at all, so every per-turn token count was
// being dropped, and a command's full output lives in a framed replay file whose handle the mapper
// was discarding.
func TestRealFxSessionRecordCollectsEndToEnd(t *testing.T) {
	dir := filepath.Join("testdata", "fx-0.0.7-session")
	store := &Store{Dir: dir}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected one recorded session, got %d", len(refs))
	}
	ref := refs[0]
	if ref.Manifest == nil {
		t.Fatal("the recorded session's manifest was not readable")
	}

	events, stats, err := store.Read(ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if stats.Malformed != 0 {
		t.Fatalf("%d malformed lines in a log fx wrote: %v", stats.Malformed, stats.FirstError)
	}
	// fx writes far more storage bookkeeping than agent activity: recovery checkpoints and usage
	// snapshots outnumber the one committed turn roughly twenty to one. Skipping them by kind
	// rather than emitting them is what keeps the runtime log readable.
	if stats.Skipped == 0 {
		t.Error("no storage-only records were skipped, which is not what an fx log looks like")
	}

	mapped := MapSession(ref, events, MapOptions{})
	got := make([]string, 0, len(mapped))
	for _, item := range mapped {
		got = append(got, item.Event.Event.Action)
	}
	want := []string{
		"session.started",
		"prompt.submitted",
		"command.executed",
		"file.read",
		"file.created",
		"agent.message",
		"token.usage",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", got, want)
	}
}

// The command in the recorded session exited 3 through fx's `terminal` tool, which is the tool the
// released binary actually runs commands with -- `run_command` is rejected as unsupported there.
// Both names are classified as commands for that reason.
func TestRealFxCommandCarriesItsExitCodeAndOutputHandle(t *testing.T) {
	mapped := mapRealSession(t)
	command := findMapped(t, mapped, "command.executed")

	if command.Tool == nil || command.Tool.Name != "terminal" {
		t.Fatalf("tool = %+v, want fx's terminal tool", command.Tool)
	}
	if command.Command == nil || command.Command.Command != "echo beacon-verify && exit 3" {
		t.Fatalf("command = %+v", command.Command)
	}
	if command.Command.ExitCode == nil || *command.Command.ExitCode != 3 {
		t.Fatalf("exit code = %+v, want 3", command.Command.ExitCode)
	}
	// fx stores the full captured output in a framed file beside the session and leaves a handle
	// in the record. Beacon carries the handle rather than presenting the stored excerpt as the
	// whole output.
	raw := rawFx(t, command)
	if handle, _ := raw["command_output_handle"].(string); !strings.HasPrefix(handle, "fx-command-replay-") {
		t.Errorf("raw.fx.command_output_handle = %v, want fx's replay handle", raw["command_output_handle"])
	}
}

// fx 0.0.7 writes no turn summary, so the per-turn token counts have to come from the difference in
// the session's cumulative totals. Without the fallback this session reported no token usage at
// all, which is how the gap was found.
func TestRealFxSessionReportsTokenUsageWithoutATurnSummary(t *testing.T) {
	mapped := mapRealSession(t)
	usage := findMapped(t, mapped, "token.usage")

	if usage.GenAI == nil || usage.GenAI.Usage == nil {
		t.Fatal("no gen_ai.usage on the usage event")
	}
	if usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 1234 {
		t.Errorf("input tokens = %+v, want the 1234 the provider reported", usage.GenAI.Usage.InputTokens)
	}
	if usage.GenAI.Usage.OutputTokens == nil || *usage.GenAI.Usage.OutputTokens != 56 {
		t.Errorf("output tokens = %+v, want 56", usage.GenAI.Usage.OutputTokens)
	}
	// Which source the numbers came from is on the event, because a difference of two running
	// totals and an exact per-turn count are not equally trustworthy.
	if source := rawFx(t, usage)["token_source"]; source != "session_totals_delta" {
		t.Errorf("raw.fx.token_source = %v, want session_totals_delta", source)
	}
}

// The write in the recorded session produced a real committed diff, which is the field a reviewer
// and a sensitive-edit rule both read.
func TestRealFxWriteCarriesItsDiff(t *testing.T) {
	mapped := mapRealSession(t)
	created := findMapped(t, mapped, "file.created")

	if created.File == nil || !strings.HasSuffix(created.File.Path, "/health.ts") {
		t.Fatalf("file = %+v", created.File)
	}
	if !strings.Contains(created.File.Diff, "+export function health()") {
		t.Fatalf("diff does not carry the added line:\n%s", created.File.Diff)
	}
	if created.File.Operation != "create" {
		t.Errorf("operation = %q, want create", created.File.Operation)
	}
	if created.File.Language != "ts" {
		t.Errorf("language = %q, want ts", created.File.Language)
	}
}

// fx resolves the model from its own catalog rather than from what the request asked for, and the
// session record is where that resolution is visible. Every event from the session carries it.
func TestRealFxSessionCarriesTheResolvedModelAndProvider(t *testing.T) {
	mapped := mapRealSession(t)
	for _, item := range mapped {
		if item.Event.Model == "" {
			t.Errorf("%s carries no model", item.Event.Event.Action)
		}
		if item.Event.GenAI == nil || item.Event.GenAI.Provider == nil || item.Event.GenAI.Provider.Name != "gateway" {
			t.Errorf("%s provider = %+v, want gateway", item.Event.Event.Action, item.Event.GenAI)
		}
	}
}

func mapRealSession(t *testing.T) []MappedEvent {
	t.Helper()
	store := &Store{Dir: filepath.Join("testdata", "fx-0.0.7-session")}
	refs, err := store.List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("List: %v (%d sessions)", err, len(refs))
	}
	events, _, err := store.Read(refs[0])
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return MapSession(refs[0], events, MapOptions{})
}

func findMapped(t *testing.T, mapped []MappedEvent, action string) schema.Event {
	t.Helper()
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			return item.Event
		}
	}
	t.Fatalf("no %q event in the recorded session", action)
	return schema.Event{}
}
