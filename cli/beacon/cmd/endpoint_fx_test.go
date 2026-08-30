package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fxCommandFixture writes one fx session on disk and points the command's flags at it, with the
// runtime log and cursor in temp directories so nothing touches the machine running the test.
type fxCommandFixture struct {
	sessionsDir string
	statePath   string
	logPath     string
}

func newFxCommandFixture(t *testing.T) fxCommandFixture {
	t.Helper()
	root := t.TempDir()
	f := fxCommandFixture{
		sessionsDir: filepath.Join(root, "fx-sessions"),
		statePath:   filepath.Join(root, "state", "fx.json"),
		logPath:     filepath.Join(root, "logs", "runtime.jsonl"),
	}
	f.writeSession(t)

	// Command flags are package-level in this CLI, so each test sets the whole set it depends on
	// rather than inheriting whatever the previous test left behind.
	endpointFxOpts.sessionsDir = f.sessionsDir
	endpointFxOpts.statePath = f.statePath
	endpointFxOpts.logPath = f.logPath
	endpointFxOpts.print = false
	endpointFxOpts.watch = false
	endpointOpts.jsonOutput = false
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointFxOpts.sessionsDir = ""
		endpointFxOpts.statePath = ""
		endpointFxOpts.logPath = ""
		endpointFxOpts.print = false
		endpointOpts.jsonOutput = false
	})
	return f
}

// The fixture is fx's wire format, transcribed from its own writers rather than produced by
// Beacon's structs. See the package-level note in internal/fxsession/fixtures_test.go.
func (f fxCommandFixture) writeSession(t *testing.T) {
	t.Helper()
	const generation = "1f4b2c8e6d3a5a7c9b613f0d5e8c2a14"
	const sessionID = "1770000000000-1770000000000000000-a1b2c3d4e5f60718"
	started := fmt.Sprintf(
		`{"schema_version":1,"log_generation":%q,"seq":1,"event_id":"a1","timestamp_ms":1770000000000,"kind":"session_started",`+
			`"payload":{"id":%q,"created_at_ms":1770000000000,"origin_workspace_root":"/repo","workspace_root":"/repo",`+
			`"conversation_language":"en","preferences":{"provider":"gateway","model":"anthropic/claude-opus-4","effort":"medium","fast_mode":false},"usage":null}}`,
		generation, sessionID)
	turn := fmt.Sprintf(
		`{"schema_version":1,"log_generation":%q,"seq":2,"event_id":"a2","timestamp_ms":1770000004000,"kind":"history_turn_committed",`+
			`"payload":{"conversation_language":"en","total_input_tokens":100,"total_output_tokens":20,"turn":{"kind":"assistant",`+
			`"user":{"text":"run the tests","images":[]},"assistant":"Done.","execution":{"schema_version":5,"tool_steps":[`+
			`{"assistant":null,"tool_calls":[{"id":"call_1","name":"run_command","arguments_json":"{\"command\":\"npm test\"}","provider_result":null}],`+
			`"tool_results":[{"tool_call_id":"call_1","tool_name":"run_command","status":"success","output":"ok","output_handle":null,"preview":null,`+
			`"output_bytes":2,"stored_output_bytes":2,"truncated":false,"provider_native":false,"created_at_ms":1770000003000,"permission_feedback":[],`+
			`"committed_file_presentation":null,"command_output_replay":null,"command_process_presentation":{"kind":"exit_code","value":0},`+
			`"terminal_action_presentation":null}]}],"files":[],"turn_summary":{"started_at_ms":1770000000500,"completed_at_ms":1770000004000,`+
			`"thinking_duration_ms":100,"turn_duration_ms":3500,"token_progress":{"input_tokens":100,"output_tokens":20,"input_exact":true,"output_exact":true}}}}}}`,
		generation)
	body := started + "\n" + turn + "\n"

	dir := filepath.Join(f.sessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(
		`{"schema_version":3,"storage_format":"event_log_v1","id":%q,"authority_id":"ab","log_generation":%q,`+
			`"created_at_ms":1770000000000,"updated_at_ms":1770000004000,"origin_workspace_root":"/repo","workspace_root":"/repo",`+
			`"conversation_language":"en","history_len":1,"total_input_tokens":100,"total_output_tokens":20,`+
			`"last_event_seq":2,"event_log_bytes":%d,"event_log_stat_fingerprint":"cd",`+
			`"generation_base_seq":0,"generation_base_bytes":0,"checkpoint_seq":null,"checkpoint_sha256":null,`+
			`"preferences":{"provider":"gateway","model":"anthropic/claude-opus-4","effort":"medium","fast_mode":false}}`,
		sessionID, generation, len(body))
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runFxCommand(t *testing.T, cmd *cobra.Command, run func(*cobra.Command, []string) error) string {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	if err := run(cmd, nil); err != nil {
		t.Fatalf("command failed: %v", err)
	}
	return out.String()
}

func TestEndpointFxSyncWritesTheRuntimeLogAndReportsWhatItDid(t *testing.T) {
	f := newFxCommandFixture(t)

	output := runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	if !strings.Contains(output, "fx sync:") {
		t.Fatalf("sweep reported nothing readable: %q", output)
	}

	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if !strings.Contains(string(data), `"vercel_fx"`) {
		t.Fatalf("runtime log has no fx events:\n%s", data)
	}
	if !strings.Contains(string(data), `"command.executed"`) {
		t.Errorf("the session's command was not collected:\n%s", data)
	}
}

// The command is what a schedule runs, so its second run over an unchanged machine has to be a
// no-op. If it is not, a cron entry doubles the log every time it fires.
func TestEndpointFxSyncIsIdempotentAcrossRuns(t *testing.T) {
	f := newFxCommandFixture(t)

	runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	first, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	second, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("a second sweep changed the runtime log:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// --print is the flag someone reaches for to see what Beacon would collect before letting it write
// anything. It has to leave no trace at all -- neither log lines nor a cursor that would make a
// later real sweep skip the events it just previewed.
func TestEndpointFxSyncPrintLeavesNoTrace(t *testing.T) {
	f := newFxCommandFixture(t)
	endpointFxOpts.print = true

	output := runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	if !strings.Contains(output, `"vercel_fx"`) {
		t.Fatalf("--print showed no events:\n%s", output)
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("--print emitted a non-JSON line %q: %v", line, err)
		}
	}
	if _, err := os.Stat(f.logPath); !os.IsNotExist(err) {
		t.Error("--print wrote the runtime log")
	}
	if _, err := os.Stat(f.statePath); !os.IsNotExist(err) {
		t.Error("--print wrote a cursor file")
	}

	// And the real sweep afterwards still collects everything the preview showed.
	endpointFxOpts.print = false
	runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if !strings.Contains(string(data), `"command.executed"`) {
		t.Error("the preview consumed events the real sweep should have collected")
	}
}

func TestEndpointFxStatusReportsCollectionProgress(t *testing.T) {
	newFxCommandFixture(t)

	before := runFxCommand(t, endpointFxStatusCmd, runEndpointFxStatus)
	if !strings.Contains(before, "pending") {
		t.Fatalf("status before a sweep does not report pending work:\n%s", before)
	}

	runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)

	after := runFxCommand(t, endpointFxStatusCmd, runEndpointFxStatus)
	if !strings.Contains(after, "collected") {
		t.Fatalf("status after a sweep does not report the session as collected:\n%s", after)
	}
}

func TestEndpointFxStatusJSONDescribesEachSession(t *testing.T) {
	newFxCommandFixture(t)
	runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	endpointOpts.jsonOutput = true

	output := runFxCommand(t, endpointFxStatusCmd, runEndpointFxStatus)
	var report fxStatusReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, output)
	}
	if !report.Present || len(report.Sessions) != 1 {
		t.Fatalf("report = %+v", report)
	}
	session := report.Sessions[0]
	if !session.Collected || session.CollectedSeq != 2 || session.LastEventSeq != 2 {
		t.Errorf("session status = %+v, want a collected session at sequence 2", session)
	}
	if session.Workspace != "/repo" || session.Turns != 1 {
		t.Errorf("session metadata = %+v", session)
	}
}

// A machine without fx is the common case. Status has to say so plainly rather than fail, because
// this command runs inside `beacon endpoint status` territory where an error reads as a broken
// install.
func TestEndpointFxStatusOnAMachineWithoutFxSaysSoWithoutFailing(t *testing.T) {
	newFxCommandFixture(t)
	endpointFxOpts.sessionsDir = filepath.Join(t.TempDir(), "no-fx-here")

	output := runFxCommand(t, endpointFxStatusCmd, runEndpointFxStatus)
	if !strings.Contains(output, "none") {
		t.Fatalf("status on a machine without fx: %q", output)
	}
}

func TestEndpointFxSyncOnAMachineWithoutFxIsAQuietNoOp(t *testing.T) {
	f := newFxCommandFixture(t)
	endpointFxOpts.sessionsDir = filepath.Join(t.TempDir(), "no-fx-here")

	output := runFxCommand(t, endpointFxSyncCmd, runEndpointFxSync)
	if !strings.Contains(output, "0 sessions") {
		t.Fatalf("sweep output = %q", output)
	}
	if _, err := os.Stat(f.logPath); !os.IsNotExist(err) {
		t.Error("a sweep with nothing to collect created a runtime log")
	}
}

// The cursor has to land somewhere durable even when the process has no home directory, which is
// the case in some cron and service environments. A sweep with no cursor re-appends every session's
// whole history on every run.
func TestFxStatePathAlwaysResolvesSomewhereDurable(t *testing.T) {
	if got := resolveFxStatePath("/explicit/path.json", true); got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden: %q", got)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	got := resolveFxStatePath("", true)
	if want := filepath.Join(home, ".beacon", "endpoint", "state", "fx.json"); got != want {
		t.Errorf("resolveFxStatePath = %q, want %q", got, want)
	}

	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	fallback := resolveFxStatePath("", true)
	if fallback == "" || !filepath.IsAbs(fallback) {
		t.Errorf("with no home the cursor path is %q, which is not somewhere it can be written", fallback)
	}
}
