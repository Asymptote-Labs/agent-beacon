package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type copilotCommandFixture struct {
	copilotDir string
	statePath  string
	logPath    string
}

func newCopilotCommandFixture(t *testing.T) copilotCommandFixture {
	t.Helper()
	root := t.TempDir()
	f := copilotCommandFixture{
		copilotDir: filepath.Join(root, "copilot"),
		statePath:  filepath.Join(root, "state", "copilot.json"),
		logPath:    filepath.Join(root, "logs", "runtime.jsonl"),
	}
	f.writeSession(t)
	endpointCopilotOpts.copilotDir = f.copilotDir
	endpointCopilotOpts.statePath = f.statePath
	endpointCopilotOpts.logPath = f.logPath
	endpointCopilotOpts.print = false
	endpointCopilotOpts.watch = false
	endpointCopilotOpts.workspace = ""
	endpointCopilotOpts.sessionID = ""
	endpointOpts.jsonOutput = false
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointCopilotOpts.copilotDir = ""
		endpointCopilotOpts.statePath = ""
		endpointCopilotOpts.logPath = ""
		endpointCopilotOpts.print = false
		endpointCopilotOpts.watch = false
		endpointCopilotOpts.workspace = ""
		endpointCopilotOpts.sessionID = ""
		endpointOpts.jsonOutput = false
	})
	return f
}

func (f copilotCommandFixture) writeSession(t *testing.T) {
	t.Helper()
	const sessionID = "caab1e17-509c-43b4-9ff2-13d1427c36a1"
	dir := filepath.Join(f.copilotDir, "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := strings.Join([]string{
		"id: " + sessionID,
		"cwd: /repo",
		"git_root: /repo",
		"repository: Asymptote-Labs/agent-beacon",
		"branch: main",
		"name: Fixture Session",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(workspace), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"id":"s1","timestamp":1770000000000,"type":"session.start","data":{"sessionId":"caab1e17-509c-43b4-9ff2-13d1427c36a1","copilotVersion":"1.2.3","selectedModel":"gpt-5.3-codex","context":{"cwd":"/repo"}}}`,
		`{"id":"u1","timestamp":1770000000100,"type":"user.message","data":{"content":"run tests"}}`,
		`{"id":"t1","timestamp":1770000000200,"type":"tool.execution_start","data":{"toolCallId":"call_bash","toolName":"bash","arguments":{"command":"go test ./..."}}}`,
		`{"id":"t2","timestamp":1770000000500,"type":"tool.execution_complete","data":{"toolCallId":"call_bash","success":true,"result":{"content":"ok"}}}`,
		`{"id":"a1","timestamp":1770000000800,"type":"assistant.message","data":{"content":"Done.","model":"gpt-5.3-codex","outputTokens":20}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCopilotCommand(t *testing.T, cmd *cobra.Command, run func(*cobra.Command, []string) error) string {
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

func TestEndpointCopilotSyncWritesRuntimeLogAndIsIdempotent(t *testing.T) {
	f := newCopilotCommandFixture(t)

	output := runCopilotCommand(t, endpointCopilotSyncCmd, runEndpointCopilotSync)
	if !strings.Contains(output, "copilot sync:") {
		t.Fatalf("sweep reported nothing readable: %q", output)
	}
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if !strings.Contains(string(data), `"copilot_cli"`) || !strings.Contains(string(data), `"command.executed"`) {
		t.Fatalf("runtime log has no Copilot command event:\n%s", data)
	}

	runCopilotCommand(t, endpointCopilotSyncCmd, runEndpointCopilotSync)
	again, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read runtime log again: %v", err)
	}
	if !bytes.Equal(data, again) {
		t.Fatalf("second sweep changed the runtime log:\nfirst:\n%s\nsecond:\n%s", data, again)
	}
}

func TestEndpointCopilotSyncPrintLeavesNoTrace(t *testing.T) {
	f := newCopilotCommandFixture(t)
	endpointCopilotOpts.print = true

	output := runCopilotCommand(t, endpointCopilotSyncCmd, runEndpointCopilotSync)
	if !strings.Contains(output, `"copilot_cli"`) {
		t.Fatalf("--print showed no events:\n%s", output)
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("--print emitted a non-JSON line %q: %v", line, err)
		}
	}
	if _, err := os.Stat(f.logPath); !os.IsNotExist(err) {
		t.Fatal("--print wrote the runtime log")
	}
	if _, err := os.Stat(f.statePath); !os.IsNotExist(err) {
		t.Fatal("--print wrote a cursor file")
	}
}

func TestEndpointCopilotStatusReportsProgress(t *testing.T) {
	newCopilotCommandFixture(t)
	before := runCopilotCommand(t, endpointCopilotStatusCmd, runEndpointCopilotStatus)
	if !strings.Contains(before, "pending") {
		t.Fatalf("status before sync:\n%s", before)
	}
	runCopilotCommand(t, endpointCopilotSyncCmd, runEndpointCopilotSync)
	after := runCopilotCommand(t, endpointCopilotStatusCmd, runEndpointCopilotStatus)
	if !strings.Contains(after, "collected") {
		t.Fatalf("status after sync:\n%s", after)
	}
}

func TestEndpointCopilotStatusJSONDescribesSessions(t *testing.T) {
	newCopilotCommandFixture(t)
	runCopilotCommand(t, endpointCopilotSyncCmd, runEndpointCopilotSync)
	endpointOpts.jsonOutput = true
	output := runCopilotCommand(t, endpointCopilotStatusCmd, runEndpointCopilotStatus)
	var report copilotStatusReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, output)
	}
	if !report.Present || len(report.Sessions) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !report.Sessions[0].Collected || report.Sessions[0].Workspace != "/repo" {
		t.Fatalf("session = %+v", report.Sessions[0])
	}
}

func TestEndpointCopilotStatusOnMachineWithoutCopilotSaysSo(t *testing.T) {
	newCopilotCommandFixture(t)
	endpointCopilotOpts.copilotDir = filepath.Join(t.TempDir(), "missing")
	output := runCopilotCommand(t, endpointCopilotStatusCmd, runEndpointCopilotStatus)
	if !strings.Contains(output, "none") {
		t.Fatalf("status on missing Copilot dir: %q", output)
	}
}

func TestCopilotStatePathAlwaysResolvesSomewhereDurable(t *testing.T) {
	if got := resolveCopilotStatePath("/explicit/path.json", true); got != "/explicit/path.json" {
		t.Errorf("explicit --state overridden: %q", got)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	got := resolveCopilotStatePath("", true)
	if want := filepath.Join(home, ".beacon", "endpoint", "state", "copilot.json"); got != want {
		t.Errorf("resolveCopilotStatePath = %q, want %q", got, want)
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	fallback := resolveCopilotStatePath("", true)
	if fallback == "" || !filepath.IsAbs(fallback) {
		t.Errorf("with no home the cursor path is %q", fallback)
	}
}
