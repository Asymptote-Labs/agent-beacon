package harness2session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mapRecorded maps a stream recorded from a real executor run, placed where a benchmark would put it.
func mapRecorded(t *testing.T, name, rel string) []MappedEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "recorded", name))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "runs")
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, ok := listFixtures(t, root)[rel]
	if !ok {
		t.Fatalf("recorded %s was not recognized as a trajectory", name)
	}
	trajectory, stats, err := (&Store{}).Read(ref)
	if err != nil || stats.Malformed != 0 || stats.PartialTail {
		t.Fatalf("read: stats=%+v err=%v", stats, err)
	}
	return MapTrajectory(trajectory, MapOptions{})
}

// codex-exec-0.144.6.jsonl is the stdout of the Codex release Harness² pins (config.CODEX_VERSION),
// run the way its reference argv runs it -- `codex exec --json --ephemeral
// --dangerously-bypass-approvals-and-sandbox` under a CODEX_HOME naming a custom provider -- against
// a local stand-in for the Responses endpoint that asked for one shell command and then answered.
// Two model calls made the turn, so its usage is their sum: turn.completed reports the thread total.
func TestRecordedCodexExecStream(t *testing.T) {
	events := mapRecorded(t, "codex-exec-0.144.6.jsonl", "lab/results/cx/recorded/codex_session.jsonl")
	if got := strings.Join(actions(events), ","); got != "session.started,session.error,command.executed,agent.message,token.usage" {
		t.Fatalf("actions = %s", got)
	}
	if id := events[0].Event.Session.ID; id != "01a1769f-fbe7-72b3-a4d2-a9b94a6e6dc4" {
		t.Fatalf("session id = %q", id)
	}
	// Codex reports a missing model-metadata entry as an error item and carries on.
	if events[1].Event.Error.Type != "item_error" || !strings.Contains(events[1].Event.Raw["error_message"].(string), "Model metadata") {
		t.Fatalf("warning = %+v %v", events[1].Event.Error, events[1].Event.Raw)
	}
	cmd := events[2].Event.Command
	if cmd.Command != "/bin/bash -lc 'echo harness2-real-run'" || cmd.Output != "harness2-real-run\n" || *cmd.ExitCode != 0 {
		t.Fatalf("command = %+v", cmd)
	}
	usage := events[4].Event.GenAI.Usage
	if *usage.InputTokens != 1201 || *usage.CacheRead.InputTokens != 800 || *usage.OutputTokens != 100 || *usage.Reasoning.OutputTokens != 20 {
		t.Fatalf("usage = %s", mustJSON(t, usage))
	}
}

// opencode-run-1.14.18.jsonl is the stdout of the OpenCode release Harness² pins (v1.14.18), run as
// its JobBench runner runs it -- `opencode run <prompt> --dir <ws> --model <provider/model> --title
// <task | label | aN> --format json` with OPENCODE_PERMISSION fencing the workspace -- against a local
// stand-in for an OpenAI-compatible provider that asked for one bash call and then answered. OpenCode
// reports each step's own usage, with cached input already taken out of input.
func TestRecordedOpenCodeRunStream(t *testing.T) {
	events := mapRecorded(t, "opencode-run-1.14.18.jsonl", "jb/dataset/p/task1/model_traj/l/p_task1_l_attempt1_x.jsonl")
	if got := strings.Join(actions(events), ","); got != "session.started,command.executed,token.usage,agent.message,token.usage" {
		t.Fatalf("actions = %s", got)
	}
	if id := events[0].Event.Session.ID; id != "ses_e895c12c2ffeRtDssTNzjq5b5M" {
		t.Fatalf("session id = %q", id)
	}
	cmd := events[1].Event
	if cmd.Command.Command != "echo harness2-real-run" || cmd.Command.Output != "harness2-real-run\n" || *cmd.Command.ExitCode != 0 ||
		cmd.GenAI.Tool.Call.ID != "call_bash_1" {
		t.Fatalf("command = %+v call=%+v", cmd.Command, cmd.GenAI.Tool.Call)
	}
	for i, want := range map[int]int64{2: 600, 4: 601} {
		usage := events[i].Event.GenAI.Usage
		if *usage.InputTokens != want || *usage.CacheRead.InputTokens != 300 || *usage.OutputTokens != 40 || usage.CostUSD != nil {
			t.Fatalf("step usage %d = %s", i, mustJSON(t, usage))
		}
	}
}
