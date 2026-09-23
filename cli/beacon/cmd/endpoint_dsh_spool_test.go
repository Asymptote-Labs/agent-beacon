package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/spf13/cobra"
)

// Staged-but-undrained hook events are a distinct state from "the hooks captured nothing":
// the capture worked (the event exists in the workspace spool) and only the sweep is missing.
// Doctor must say that, with the sync remedy, instead of sending the operator to change the
// sandbox mode for a session that was captured fine.

// sessionInWorkspace writes a native session record whose cwd points at workspace, last
// modified at modified. The stock fixture's records carry no cwd, and a spool is only
// locatable through one.
func sessionInWorkspace(t *testing.T, f dshDoctorFixture, id, workspace string, modified time.Time) {
	t.Helper()
	dir := filepath.Join(f.home, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	line := `{"type":"session","time":"` + modified.UTC().Format(time.RFC3339) + `","data":{"id":"` + id + `","cwd":` + jsonString(workspace) + `}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func jsonString(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// plantSpool stages hook-event bytes in the workspace spool for id, the way a sandboxed hook
// would, and returns the staged byte count.
func plantSpool(t *testing.T, workspace, id string) int {
	t.Helper()
	path, ok := asymptoteobserve.DSHSpoolPath(workspace, id)
	if !ok {
		t.Fatalf("unsafe test session id %q", id)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"timestamp":"2026-09-23T12:00:00Z","vendor":"beacon","product":"endpoint-agent",` +
		`"schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted"},` +
		`"severity":"info","harness":{"name":"deepseek_harness","collection_method":"hook"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return len(body)
}

// A session with staged events and no hook event in the runtime log is capture working,
// drain missing: dsh_spool_pending_drain, not dsh_sessions_without_hook_events.
func TestDshHookCaptureReportsPendingSpoolInsteadOfMissedSessions(t *testing.T) {
	f := newDshDoctorFixture(t)
	workspace := t.TempDir()
	sessionInWorkspace(t, f, "pending-1", workspace, f.installedAt.Add(time.Hour))
	plantSpool(t, workspace, "pending-1")
	// Poll backfill has run, as it would before the operator ever noticed the drain.
	f.event(t, "poll", f.installedAt.Add(2*time.Hour))

	got := f.check()
	if got.Status != diagnostics.StatusWarn || got.Evidence != "dsh_spool_pending_drain" {
		t.Fatalf("status/evidence = %s/%s, want warn/dsh_spool_pending_drain (%s)", got.Status, got.Evidence, got.Message)
	}
	if got.Severity != diagnostics.SeverityLow {
		t.Errorf("severity = %s, want low: the events exist, only the drain is missing", got.Severity)
	}
	for _, want := range []string{"staged", "workspace spool", f.logPath} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message does not mention %q: %s", want, got.Message)
		}
	}
	if !strings.Contains(got.Action, "beacon endpoint dsh sync") {
		t.Errorf("action does not name the drain: %s", got.Action)
	}
	if strings.Contains(got.Message, "danger-full-access") {
		t.Errorf("a pending drain must not blame the sandbox mode: %s", got.Message)
	}
}

// A genuine miss outranks a pending drain: sessions the hooks captured nothing for are the
// more urgent finding, and their message must not be softened by a healthy sibling session.
func TestDshHookCaptureMissedSessionsOutrankAPendingDrain(t *testing.T) {
	f := newDshDoctorFixture(t)
	workspace := t.TempDir()
	sessionInWorkspace(t, f, "pending-1", workspace, f.installedAt.Add(time.Hour))
	plantSpool(t, workspace, "pending-1")
	f.session(t, "missed-1", f.installedAt.Add(2*time.Hour))

	got := f.check()
	if got.Evidence != "dsh_sessions_without_hook_events" {
		t.Fatalf("evidence = %s, want dsh_sessions_without_hook_events (%s)", got.Evidence, got.Message)
	}
	if !strings.Contains(got.Message, "1 DeepSeek Harness session(s)") {
		t.Fatalf("the pending session must not be counted as missed: %s", got.Message)
	}
}

// Draining is the fix for a pending spool, and the drain empties the file; the next check
// then falls back to the log-based evidence like any other install.
func TestDshHookCapturePendingDrainClearsOnceTheSpoolIsEmpty(t *testing.T) {
	f := newDshDoctorFixture(t)
	workspace := t.TempDir()
	sessionInWorkspace(t, f, "pending-1", workspace, f.installedAt.Add(time.Hour))
	plantSpool(t, workspace, "pending-1")
	if f.check().Evidence != "dsh_spool_pending_drain" {
		t.Fatal("precondition: planted spool must report as pending drain")
	}

	// Drain: the events land in the log as hook events and the spool file goes away.
	f.event(t, "hook", f.installedAt.Add(2*time.Hour))
	path, _ := asymptoteobserve.DSHSpoolPath(workspace, "pending-1")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	got := f.check()
	if got.Status != diagnostics.StatusOK || got.Evidence != "dsh_hook_events_observed" {
		t.Fatalf("status/evidence = %s/%s, want ok/dsh_hook_events_observed after the drain (%s)",
			got.Status, got.Evidence, got.Message)
	}
}

// dsh status is the surface an operator (and the installed skill) reads between sweeps, so
// staged bytes must show up in both the JSON report and the text line.
func TestDshStatusReportsPendingSpoolBytes(t *testing.T) {
	f := newDshDoctorFixture(t)
	workspace := t.TempDir()
	sessionInWorkspace(t, f, "staged-1", workspace, time.Now().Add(-time.Hour))
	staged := plantSpool(t, workspace, "staged-1")

	restore := stashDshOpts(t)
	endpointDshOpts.dshHome = f.home
	endpointDshOpts.statePath = filepath.Join(t.TempDir(), "state.json")
	endpointDshOpts.workspace = ""
	endpointDshOpts.sessionID = ""

	endpointOpts.jsonOutput = true
	jsonOut := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(jsonOut)
	cmd.SetErr(jsonOut)
	if err := runEndpointDshStatus(cmd, nil); err != nil {
		t.Fatalf("dsh status --json: %v", err)
	}
	var report dshStatusReport
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, jsonOut.String())
	}
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != "staged-1" {
		t.Fatalf("sessions = %+v, want staged-1", report.Sessions)
	}
	if got := report.Sessions[0].SpoolBytes; got != int64(staged) {
		t.Errorf("spool_bytes = %d, want %d", got, staged)
	}

	endpointOpts.jsonOutput = false
	textOut := &bytes.Buffer{}
	cmd = &cobra.Command{}
	cmd.SetOut(textOut)
	cmd.SetErr(textOut)
	if err := runEndpointDshStatus(cmd, nil); err != nil {
		t.Fatalf("dsh status: %v", err)
	}
	if !strings.Contains(textOut.String(), "awaiting drain") {
		t.Errorf("text status does not flag the staged spool:\n%s", textOut.String())
	}
	restore()
}

// The sweep report is how `dsh sync --watch` and one-shot runs show their work; a run that
// drained staged events says so, separately from the poll event count.
func TestReportDshSweepCountsDrainedSpoolEvents(t *testing.T) {
	restore := stashDshOpts(t)
	endpointDshOpts.print = false
	endpointOpts.jsonOutput = false

	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	reportDshSweep(cmd, dshsession.Summary{Sessions: 1, SpoolEvents: 3})
	if !strings.Contains(out.String(), "3 spooled hook event(s) drained") {
		t.Fatalf("sweep report does not mention the drain:\n%s", out.String())
	}

	out.Reset()
	endpointOpts.jsonOutput = true
	reportDshSweep(cmd, dshsession.Summary{Sessions: 1, SpoolEvents: 3})
	var decoded struct {
		SpoolEvents int `json:"spool_events"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("sweep JSON: %v\n%s", err, out.String())
	}
	if decoded.SpoolEvents != 3 {
		t.Errorf("sweep JSON spool_events = %d, want 3", decoded.SpoolEvents)
	}
	restore()
}

// stashDshOpts snapshots the package globals these command tests drive and restores them.
func stashDshOpts(t *testing.T) func() {
	t.Helper()
	opts := endpointDshOpts
	endpointOptsState := endpointOpts
	t.Cleanup(func() {
		endpointDshOpts = opts
		endpointOpts = endpointOptsState
	})
	return func() {
		endpointDshOpts = opts
		endpointOpts = endpointOptsState
	}
}
