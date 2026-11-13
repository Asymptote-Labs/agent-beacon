package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
	"github.com/spf13/cobra"
)

// plantWorkspaceSpool stages one hook event for (harness, id) in workspace, the way a sandboxed
// hook would, and returns the staged byte count.
func plantWorkspaceSpool(t *testing.T, workspace, harness, id string) int {
	t.Helper()
	dir, ok := asymptoteobserve.WorkspaceSpoolDir(workspace, harness, id)
	if !ok {
		t.Fatalf("unsafe test spool %q/%q", harness, id)
	}
	body := `{"timestamp":"2026-11-13T12:00:00Z","vendor":"beacon","product":"endpoint-agent",` +
		`"schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted"},` +
		`"severity":"info","endpoint":{"os":"darwin"},"session":{"id":"` + id + `"},` +
		`"harness":{"name":"` + harness + `","collection_method":"hook"}}`
	if dropped, err := (localingest.Spool{Dir: dir}).Append([][]byte{[]byte(body)}); err != nil || dropped != 0 {
		t.Fatalf("plant spool: dropped %d, err %v", dropped, err)
	}
	return len(body) + 1
}

// spoolCommandEnv points every user-mode path at a temp home and returns the runtime log path.
func spoolCommandEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	opts, endpointOptsState, euid := endpointSpoolOpts, endpointOpts, endpointSpoolGeteuid
	t.Cleanup(func() {
		endpointSpoolOpts, endpointOpts, endpointSpoolGeteuid = opts, endpointOptsState, euid
	})
	endpointSpoolGeteuid = func() int { return 1000 }
	endpointOpts.userMode, endpointOpts.systemMode, endpointOpts.jsonOutput = true, false, false
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	endpointSpoolOpts.logPath = logPath
	endpointSpoolOpts.workspaces, endpointSpoolOpts.harness, endpointSpoolOpts.session = nil, "", ""
	return logPath
}

func runSpoolCmd(t *testing.T, run func(*cobra.Command, []string) error) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	err := run(cmd, nil)
	return out.String(), err
}

// Root never writes or removes files in a user's workspace, so the drain refuses outright and
// says whose account should run it.
func TestEndpointSpoolDrainRefusesRoot(t *testing.T) {
	spoolCommandEnv(t)
	endpointSpoolGeteuid = func() int { return 0 }
	_, err := runSpoolCmd(t, runEndpointSpoolDrain)
	if err == nil || !strings.Contains(err.Error(), "as the user who owns the workspace") {
		t.Fatalf("drain as root: err = %v, want a refusal naming the workspace owner", err)
	}
}

// An explicit --workspace vouches for its spools even when the log has never seen the session.
func TestEndpointSpoolDrainExplicitWorkspace(t *testing.T) {
	logPath := spoolCommandEnv(t)
	workspace := t.TempDir()
	plantWorkspaceSpool(t, workspace, "gemini_cli", "sess-1")
	endpointSpoolOpts.workspaces = []string{workspace}
	endpointOpts.jsonOutput = true

	out, err := runSpoolCmd(t, runEndpointSpoolDrain)
	if err != nil {
		t.Fatalf("drain: %v\n%s", err, out)
	}
	var summary struct{ Sessions, Events, Rejected int }
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("drain JSON: %v\n%s", err, out)
	}
	if summary.Sessions != 1 || summary.Events != 1 || summary.Rejected != 0 {
		t.Fatalf("summary = %+v, want 1 session, 1 event, 0 rejected", summary)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), `"sess-1"`) {
		t.Fatalf("runtime log does not hold the drained event (err %v):\n%s", err, data)
	}
}

// The command a hook prints for a staged session names its workspace, harness and session. Pasted,
// it vouches for that session alone: a spool a cloned repository planted beside it for an invented
// session stays unread.
func TestEndpointSpoolDrainNamedSessionOnly(t *testing.T) {
	logPath := spoolCommandEnv(t)
	workspace := t.TempDir()
	plantWorkspaceSpool(t, workspace, "opencode", "sess-real")
	plantWorkspaceSpool(t, workspace, "claude_code", "invented")
	endpointSpoolOpts.workspaces = []string{workspace}
	endpointSpoolOpts.harness, endpointSpoolOpts.session = "opencode", "sess-real"
	endpointOpts.jsonOutput = true

	if out, err := runSpoolCmd(t, runEndpointSpoolDrain); err != nil {
		t.Fatalf("drain: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"sess-real"`) || strings.Contains(string(data), `"invented"`) {
		t.Fatalf("drain of one named session wrote:\n%s", data)
	}
	if !spoolFileHolds(t, workspace, "claude_code", "invented") {
		t.Fatal("the planted spool beside the named session was drained")
	}
}

// spoolFileHolds reports whether a session's workspace spool still holds lines.
func spoolFileHolds(t *testing.T, workspace, harness, id string) bool {
	t.Helper()
	dir, _ := asymptoteobserve.WorkspaceSpoolDir(workspace, harness, id)
	info, err := os.Stat(localingest.Spool{Dir: dir}.FilePath())
	return err == nil && info.Size() > 0
}

// A system config holding a destination secret is root-only, and root may not drain. The ordinary
// account that may drain cannot learn the log from it, so the drain never guesses one: without
// --log-path it refuses, naming the flag, and drains nothing; with it, it drains its --workspace.
func TestEndpointSpoolDrainWithAnUnreadableConfig(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions that bind this account")
	}
	logPath := spoolCommandEnv(t)
	config := endpointconfig.ConfigPath(true)
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"user_mode":true}`), 0o000); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	plantWorkspaceSpool(t, workspace, "gemini_cli", "sess-1")
	endpointSpoolOpts.workspaces = []string{workspace}

	endpointSpoolOpts.logPath = ""
	if out, err := runSpoolCmd(t, runEndpointSpoolDrain); err == nil || !strings.Contains(err.Error(), "--log-path") {
		t.Fatalf("drain with an unreadable config and no --log-path: err %v, want a refusal naming --log-path\n%s", err, out)
	}
	if !spoolFileHolds(t, workspace, "gemini_cli", "sess-1") {
		t.Fatal("a drain with no known log took the staged event out of the spool")
	}

	endpointSpoolOpts.logPath = logPath
	out, err := runSpoolCmd(t, runEndpointSpoolDrain)
	if err != nil || !strings.Contains(out, "cannot read the endpoint config") {
		t.Fatalf("drain with an unreadable config: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(logPath); !strings.Contains(string(data), `"sess-1"`) {
		t.Fatalf("the --workspace spool was not drained:\n%s", data)
	}
}

func TestEndpointSpoolStatusListsPendingBytes(t *testing.T) {
	spoolCommandEnv(t)
	workspace := t.TempDir()
	staged := plantWorkspaceSpool(t, workspace, "qwen_code", "sess-2")
	endpointSpoolOpts.workspaces = []string{workspace}
	endpointOpts.jsonOutput = true

	out, err := runSpoolCmd(t, runEndpointSpoolStatus)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	var report struct {
		Sessions []spoolPendingStatus `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, out)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != "sess-2" || report.Sessions[0].Bytes != int64(staged) {
		t.Fatalf("sessions = %+v, want sess-2 with %d bytes", report.Sessions, staged)
	}

	endpointOpts.jsonOutput = false
	out, err = runSpoolCmd(t, runEndpointSpoolStatus)
	if err != nil || !strings.Contains(out, "sess-2") {
		t.Fatalf("text status (err %v) does not list sess-2:\n%s", err, out)
	}
}

// Staged events are capture working and the drain missing; DeepSeek Harness spools are left
// to dsh_spool_pending_drain so one spool is never reported twice.
func TestDoctorWorkspaceSpoolCheck(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	dshOnly := t.TempDir()
	plantWorkspaceSpool(t, dshOnly, "deepseek_harness", "dsh-1")
	if check, ok := workspaceSpoolCheck(logPath, []string{dshOnly}); ok {
		t.Fatalf("dsh-only spool reported by workspace_spool: %+v", check)
	}

	mixed := t.TempDir()
	plantWorkspaceSpool(t, mixed, "deepseek_harness", "dsh-2")
	plantWorkspaceSpool(t, mixed, "gemini_cli", "gem-1")
	check, ok := workspaceSpoolCheck(logPath, []string{mixed})
	if !ok {
		t.Fatal("pending spool not reported")
	}
	if check.Name != "workspace_spool" || check.Status != diagnostics.StatusWarn ||
		check.Severity != diagnostics.SeverityLow || check.Evidence != "workspace_spool_pending_drain" {
		t.Fatalf("check = %+v", check)
	}
	if !strings.HasPrefix(check.Message, "1 session(s)") || !strings.Contains(check.Action, "beacon endpoint spool drain") {
		t.Errorf("message/action = %q / %q", check.Message, check.Action)
	}
}
