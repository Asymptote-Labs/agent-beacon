package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/harness2session"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/spf13/cobra"
)

// harness2CommandFixture writes one LAB Codex rollout and one JobBench OpenCode rollout under a runs
// directory and points the command's flags at it, with the runtime log and cursor in temp
// directories so nothing touches the machine running the test.
type harness2CommandFixture struct {
	runsDir   string
	statePath string
	logPath   string
}

const harness2CodexStream = `{"type":"thread.started","thread_id":"th-cmd-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"bash -lc ls","aggregated_output":"a\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2,"reasoning_output_tokens":0}}
`

const harness2OpenCodeStream = `{"type":"step_start","timestamp":1793004900100,"sessionID":"ses_cmd_1","part":{"type":"step-start"}}
{"type":"text","timestamp":1793004900200,"sessionID":"ses_cmd_1","part":{"type":"text","text":"hello","time":{"start":1,"end":2}}}
`

func newHarness2CommandFixture(t *testing.T) harness2CommandFixture {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	t.Setenv("HARNESS2_RUNS_DIR", "")
	root := t.TempDir()
	f := harness2CommandFixture{
		runsDir:   filepath.Join(root, "harness2", "runs"),
		statePath: filepath.Join(root, "state", "harness2.json"),
		logPath:   filepath.Join(root, "logs", "runtime.jsonl"),
	}
	for rel, body := range map[string]string{
		"lab/results/cx/run-1/codex_session.jsonl":                         harness2CodexStream,
		"jb/dataset/p/task1/model_traj/base/p_task1_base_attempt1_x.jsonl": harness2OpenCodeStream,
		"jb/judged/scores.jsonl":                                           `{"task":"p/task1","score":1}` + "\n",
	} {
		path := filepath.Join(f.runsDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Command flags are package-level in this CLI, so each test sets the whole set it depends on.
	endpointHarness2Opts.runsDirs = []string{f.runsDir}
	endpointHarness2Opts.statePath = f.statePath
	endpointHarness2Opts.logPath = f.logPath
	endpointHarness2Opts.print = false
	endpointHarness2Opts.watch = false
	endpointOpts.jsonOutput = false
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointHarness2Opts.runsDirs = nil
		endpointHarness2Opts.statePath = ""
		endpointHarness2Opts.logPath = ""
		endpointHarness2Opts.print = false
		endpointOpts.jsonOutput = false
	})
	return f
}

func runHarness2(t *testing.T, cmd *cobra.Command, run func(*cobra.Command, []string) error) string {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	t.Cleanup(func() { cmd.SetOut(nil); cmd.SetErr(nil) })
	if err := run(cmd, nil); err != nil {
		t.Fatalf("%s: %v\n%s", cmd.Name(), err, out.String())
	}
	return out.String()
}

func readHarness2Log(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestEndpointHarness2SyncWritesTheRuntimeLog(t *testing.T) {
	f := newHarness2CommandFixture(t)
	out := runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync)
	if !strings.Contains(out, "Harness² sync: 2 rollouts, 2 changed, 6 events, 0 errors") {
		t.Fatalf("output = %q", out)
	}
	events := readHarness2Log(t, f.logPath)
	if len(events) != 6 {
		t.Fatalf("log holds %d events, want 6", len(events))
	}
	for _, ev := range events {
		harness := ev["harness"].(map[string]interface{})
		if harness["name"] != "harness2" || harness["collection_method"] != "poll" {
			t.Fatalf("harness = %v", harness)
		}
	}

	// A second run writes nothing, and a run with no --runs-dir still reads the remembered one.
	endpointHarness2Opts.runsDirs = nil
	out = runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync)
	if !strings.Contains(out, "2 rollouts, 0 changed, 0 events") {
		t.Fatalf("second run output = %q", out)
	}
	if len(readHarness2Log(t, f.logPath)) != 6 {
		t.Fatal("second run wrote events again")
	}
}

func TestEndpointHarness2SyncPrintLeavesNoTrace(t *testing.T) {
	f := newHarness2CommandFixture(t)
	endpointHarness2Opts.print = true
	out := runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync)
	if lines := strings.Count(out, "\n"); lines != 6 {
		t.Fatalf("printed %d lines, want 6 events:\n%s", lines, out)
	}
	for _, path := range []string{f.statePath, f.logPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("--print created %s (err=%v)", path, err)
		}
	}
}

// --print with no --runs-dir reads what a real sync would: the default checkout and the roots
// earlier syncs remembered.
func TestEndpointHarness2PrintReadsRememberedRoots(t *testing.T) {
	newHarness2CommandFixture(t)
	runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync)
	endpointHarness2Opts.runsDirs = nil
	endpointHarness2Opts.print = true
	if out := runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync); strings.Count(out, "\n") != 6 {
		t.Fatalf("print with remembered roots = %q", out)
	}
}

func TestEndpointHarness2StatusJSONDescribesEachRollout(t *testing.T) {
	f := newHarness2CommandFixture(t)
	runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync)
	endpointOpts.jsonOutput = true
	out := runHarness2(t, endpointHarness2StatusCmd, runEndpointHarness2Status)
	var report harness2StatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status JSON %q: %v", out, err)
	}
	if !report.Present || len(report.Rollouts) != 2 || report.StatePath != f.statePath {
		t.Fatalf("report = %+v", report)
	}
	byExecutor := map[string]harness2RolloutStatus{}
	for _, r := range report.Rollouts {
		byExecutor[r.Executor] = r
	}
	if cx := byExecutor["codex"]; !cx.Collected || cx.Events != 4 || cx.SessionID != "th-cmd-1" || cx.Benchmark != "lab" || cx.RunID != "run-1" {
		t.Fatalf("codex rollout = %+v", cx)
	}
	if oc := byExecutor["opencode"]; !oc.Collected || oc.TaskID != "p/task1" || oc.Label != "base" {
		t.Fatalf("opencode rollout = %+v", oc)
	}
}

func TestEndpointHarness2OnAMachineWithoutHarness2IsAQuietNoOp(t *testing.T) {
	f := newHarness2CommandFixture(t)
	endpointHarness2Opts.runsDirs = []string{filepath.Join(t.TempDir(), "missing")}
	out := runHarness2(t, endpointHarness2SyncCmd, runEndpointHarness2Sync)
	if !strings.Contains(out, "0 rollouts") || !strings.Contains(out, "--runs-dir") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(f.logPath); !os.IsNotExist(err) {
		t.Fatal("a sync with nothing to read created the runtime log")
	}
	out = runHarness2(t, endpointHarness2StatusCmd, runEndpointHarness2Status)
	if !strings.Contains(out, "none") {
		t.Fatalf("status output = %q", out)
	}
}

func TestHarness2StatePathAlwaysResolvesSomewhereDurable(t *testing.T) {
	if got := resolveHarness2StatePath("/x/state.json", true); got != "/x/state.json" {
		t.Fatalf("override = %q", got)
	}
	if got := resolveHarness2StatePath("", true); got != harness2session.DefaultStatePath() {
		t.Fatalf("user default = %q", got)
	}
	if got := resolveHarness2StatePath("", false); filepath.Base(got) != "harness2-state.json" {
		t.Fatalf("system default = %q", got)
	}
}

// A broken runs directory is reported beside the rollouts the others hold, not instead of them.
func TestEndpointHarness2StatusReportsABrokenRootBesideTheRest(t *testing.T) {
	f := newHarness2CommandFixture(t)
	bad := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	endpointHarness2Opts.runsDirs = []string{bad, f.runsDir}
	endpointOpts.jsonOutput = true
	out := runHarness2(t, endpointHarness2StatusCmd, runEndpointHarness2Status)
	var report harness2StatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status JSON %q: %v", out, err)
	}
	if len(report.Rollouts) != 2 || len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "not a directory") {
		t.Fatalf("report = %+v", report)
	}
}
