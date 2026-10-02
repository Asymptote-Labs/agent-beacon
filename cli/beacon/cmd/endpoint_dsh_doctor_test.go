package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// DeepSeek Harness runs hook commands sandboxed to the session workspace unless the sandbox mode is
// danger-full-access, so a hook commonly cannot write the runtime log while exiting 0 (#605). In
// workspace-write it stages the event in a workspace spool instead (dsh_spool_pending_drain until a
// sweep drains it); with nothing writable it records nothing at all. `beacon endpoint dsh sync` still
// fills the log with poll events under the same harness name, so harness_observed stays green.
// dsh_hook_capture is the check that notices.

// dshDoctorFixture is a Harness home with a hooks file installed at installedAt, and a runtime log.
type dshDoctorFixture struct {
	home        string
	hooksPath   string
	logPath     string
	installedAt time.Time
}

func newDshDoctorFixture(t *testing.T) dshDoctorFixture {
	t.Helper()
	root := t.TempDir()
	f := dshDoctorFixture{
		home:        filepath.Join(root, "dsh-home"),
		logPath:     filepath.Join(root, "runtime.jsonl"),
		installedAt: time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC),
	}
	f.hooksPath = filepath.Join(f.home, "beacon-endpoint-hooks.json")
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.hooksPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.hooksPath, f.installedAt, f.installedAt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// session writes a native session record last modified at modified.
func (f dshDoctorFixture) session(t *testing.T, id string, modified time.Time) {
	t.Helper()
	dir := filepath.Join(f.home, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session","id":"`+id+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

// event appends a deepseek_harness event collected by method at ts.
func (f dshDoctorFixture) event(t *testing.T, method string, ts time.Time) {
	t.Helper()
	file, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	line := `{"timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","harness":{"name":"deepseek_harness","collection_method":"` + method + `"}}` + "\n"
	if _, err := file.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func (f dshDoctorFixture) check() diagnostics.Check {
	return dshHookCaptureCheckAt(f.logPath, f.hooksPath, f.home)
}

// The reported case: sessions ran after install, `dsh sync` backfilled them as poll events, and no
// live hook event exists. That must be a warning that names the sandbox and the remedies.
func TestDshHookCaptureWarnsWhenSessionsRanWithoutHookEvents(t *testing.T) {
	f := newDshDoctorFixture(t)
	f.session(t, "sess-a", f.installedAt.Add(time.Hour))
	f.session(t, "sess-b", f.installedAt.Add(2*time.Hour))
	f.event(t, "poll", f.installedAt.Add(3*time.Hour))

	got := f.check()
	if got.Name != "dsh_hook_capture" || got.Target != dshsession.Harness {
		t.Fatalf("check identity = %s/%s, want dsh_hook_capture/%s", got.Name, got.Target, dshsession.Harness)
	}
	if got.Status != diagnostics.StatusWarn || got.Evidence != "dsh_sessions_without_hook_events" {
		t.Fatalf("status/evidence = %s/%s, want warn/dsh_sessions_without_hook_events (%s)", got.Status, got.Evidence, got.Message)
	}
	for _, want := range []string{"2 DeepSeek Harness session(s)", "danger-full-access", f.logPath} {
		if !strings.Contains(got.Message, want) {
			t.Fatalf("message does not mention %q: %s", want, got.Message)
		}
	}
	if !strings.Contains(got.Action, "beacon endpoint dsh sync") {
		t.Fatalf("action does not name the backfill: %s", got.Action)
	}
}

// A working install: hook events land in the same turns the session store records, a little before
// the store's final write. That must not be flagged.
func TestDshHookCaptureIsOKWhenHookEventsAccompanySessions(t *testing.T) {
	f := newDshDoctorFixture(t)
	f.event(t, "hook", f.installedAt.Add(time.Hour))
	// The store commits after the Stop hook, so its file is slightly newer than the last hook event.
	f.session(t, "sess-a", f.installedAt.Add(time.Hour+time.Minute))

	got := f.check()
	if got.Status != diagnostics.StatusOK || got.Evidence != "dsh_hook_events_observed" {
		t.Fatalf("status/evidence = %s/%s, want ok/dsh_hook_events_observed (%s)", got.Status, got.Evidence, got.Message)
	}
}

// A fresh install with only older sessions is not evidence of anything: those sessions ran before
// the hooks existed. It must not warn.
func TestDshHookCaptureIsOKBeforeAnySessionHasRunSinceInstall(t *testing.T) {
	f := newDshDoctorFixture(t)
	f.session(t, "old", f.installedAt.Add(-24*time.Hour))

	got := f.check()
	if got.Status != diagnostics.StatusOK || got.Evidence != "dsh_no_sessions_since_install" {
		t.Fatalf("status/evidence = %s/%s, want ok/dsh_no_sessions_since_install (%s)", got.Status, got.Evidence, got.Message)
	}
}

// Capture that worked and then stopped -- a later session run in a sandboxed mode -- is flagged too,
// and the message says since when.
func TestDshHookCaptureWarnsWhenLiveCaptureStopsLater(t *testing.T) {
	f := newDshDoctorFixture(t)
	lastHook := f.installedAt.Add(time.Hour)
	f.event(t, "hook", lastHook)
	f.session(t, "worked", lastHook.Add(time.Minute))
	f.session(t, "sandboxed", lastHook.Add(3*time.Hour))

	got := f.check()
	if got.Status != diagnostics.StatusWarn {
		t.Fatalf("status = %s, want warn (%s)", got.Status, got.Message)
	}
	if !strings.Contains(got.Message, "1 DeepSeek Harness session(s)") {
		t.Fatalf("message should count only the session after the last hook event: %s", got.Message)
	}
	if !strings.Contains(got.Message, "since "+lastHook.UTC().Format(time.RFC3339)) {
		t.Fatalf("message does not say when live capture stopped: %s", got.Message)
	}
}

// Without an installed hook there is nothing to check; the harness check already reports that the
// hooks are missing, and a second finding saying the same thing would be noise.
func TestDshHookCaptureCheckIsSkippedWhenHooksAreNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", filepath.Join(home, "dsh"))

	if check, ok := dshHookCaptureCheck(filepath.Join(home, "runtime.jsonl")); ok {
		t.Fatalf("dshHookCaptureCheck ran without installed hooks: %+v", check)
	}
}

// End to end through a real install: the check resolves the hooks file and Harness home itself.
func TestDshHookCaptureCheckFindsARealInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dshHome := filepath.Join(home, "dsh")
	t.Setenv("DSH_HOME", dshHome)
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	logPath := filepath.Join(home, "runtime.jsonl")
	if err := installEndpointHookTarget("dsh", endpointconfig.Config{LogPath: logPath, UserMode: true}); err != nil {
		t.Fatalf("install dsh hooks: %v", err)
	}
	f := dshDoctorFixture{home: dshHome, logPath: logPath}
	f.session(t, "after-install", time.Now().Add(time.Minute))

	check, ok := dshHookCaptureCheck(logPath)
	if !ok {
		t.Fatal("dshHookCaptureCheck skipped an installed dsh")
	}
	if check.Status != diagnostics.StatusWarn {
		t.Fatalf("status = %s, want warn for a session with no hook events (%s)", check.Status, check.Message)
	}
}

// Doctor runs the check when DeepSeek Harness is detected, reports it as a warning rather than a
// failure (the sandbox mode is the operator's choice, and rollout automation keys on doctor's exit
// status), and --fix lists it as a manual skip rather than silently repairing nothing.
func TestDoctorRunsDshHookCaptureOnlyWhenDshIsDetected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	want := diagnostics.Check{
		Name: "dsh_hook_capture", Target: dshsession.Harness, Status: diagnostics.StatusWarn,
		Severity: diagnostics.SeverityMedium, Evidence: "dsh_sessions_without_hook_events",
		Action: "run `beacon endpoint dsh sync`",
	}
	orig := doctorDshHookCaptureCheck
	t.Cleanup(func() { doctorDshHookCaptureCheck = orig })
	calls := 0
	doctorDshHookCaptureCheck = func(string) (diagnostics.Check, bool) {
		calls++
		return want, true
	}

	status := lifecycle.Status{
		LogPath:    filepath.Join(home, "runtime.jsonl"),
		RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
		Harnesses:  []harness.Harness{{Name: dshsession.Harness, Detected: true, TelemetryStatus: harness.TelemetryEnabled}},
	}
	result := buildDoctorResult(status, time.Now())
	var found *diagnostics.Check
	for i := range result.Checks {
		if result.Checks[i].Name == "dsh_hook_capture" {
			found = &result.Checks[i]
		}
	}
	if found == nil || found.Status != diagnostics.StatusWarn {
		t.Fatalf("doctor did not report dsh_hook_capture as a warning: %+v", result.Checks)
	}

	plan := planDoctorFixes(result, status)
	skipped := false
	for _, s := range plan.Skipped {
		if s.Target == dshsession.Harness && s.Action == "manual_fix" {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("--fix did not list dsh_hook_capture as a manual skip: %+v", plan.Skipped)
	}

	status.Harnesses[0].Detected = false
	calls = 0
	result = buildDoctorResult(status, time.Now())
	if calls != 0 {
		t.Fatal("doctor ran dsh_hook_capture for an undetected DeepSeek Harness")
	}
	for _, c := range result.Checks {
		if c.Name == "dsh_hook_capture" {
			t.Fatalf("dsh_hook_capture reported without DeepSeek Harness: %+v", c)
		}
	}
}

// Hook events that rotated into runtime.jsonl.1 still count; otherwise a busy machine would report
// captured sessions as missed after every rotation.
func TestDshHookCaptureReadsTheRotatedArchive(t *testing.T) {
	f := newDshDoctorFixture(t)
	f.event(t, "hook", f.installedAt.Add(time.Hour))
	if err := os.Rename(f.logPath, f.logPath+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.session(t, "sess-a", f.installedAt.Add(time.Hour+time.Minute))

	if got := f.check(); got.Status != diagnostics.StatusOK {
		t.Fatalf("status = %s, want ok when the hook event is in the rotated archive (%s)", got.Status, got.Message)
	}
}

// Evidence doctor cannot read is a warning, not a pass: doctor's text output prints only failures
// and warnings, so an ok would report hook capture as checked when it was not.
func TestDshHookCaptureWarnsWhenEvidenceIsUnreadable(t *testing.T) {
	f := newDshDoctorFixture(t)
	if err := os.Remove(f.hooksPath); err != nil {
		t.Fatal(err)
	}

	got := f.check()
	if got.Status != diagnostics.StatusWarn || got.Evidence != "dsh_hooks_unreadable" {
		t.Fatalf("status/evidence = %s/%s, want warn/dsh_hooks_unreadable (%s)", got.Status, got.Evidence, got.Message)
	}
	if !strings.Contains(got.Message, f.hooksPath) || got.Action == "" {
		t.Fatalf("the warning does not name what could not be read, or has no action: %+v", got)
	}
}

// writeArchive writes one rotated archive of the runtime log holding the given lines.
func (f dshDoctorFixture) writeArchive(t *testing.T, index int, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(fmt.Sprintf("%s.%d", f.logPath, index), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dshLogLine(method string, ts time.Time) string {
	return `{"timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","harness":{"name":"deepseek_harness","collection_method":"` + method + `"}}`
}

// Poll backfill writes into the same log, so on a busy machine the last hook event can sit several
// rotations back. Every retained archive is read, not only the newest one.
func TestDshHookCaptureReadsEveryRetainedArchive(t *testing.T) {
	f := newDshDoctorFixture(t)
	hookAt := f.installedAt.Add(time.Hour)
	// The oldest retained slot holds the hook event; every newer file holds only poll events.
	oldest := writer.DefaultRotateArchives
	f.writeArchive(t, oldest, dshLogLine("hook", hookAt))
	for i := oldest - 1; i >= 1; i-- {
		f.writeArchive(t, i, dshLogLine("poll", hookAt.Add(time.Duration(oldest-i)*time.Second)))
	}
	f.event(t, "poll", hookAt.Add(time.Minute))
	f.session(t, "sess-a", hookAt.Add(time.Minute))

	if got := f.check(); got.Status != diagnostics.StatusOK || got.Evidence != "dsh_hook_events_observed" {
		t.Fatalf("status/evidence = %s/%s, want ok when the last hook event is in archive .%d (%s)", got.Status, got.Evidence, oldest, got.Message)
	}
}

// Once every archive slot is in use, rotation has started discarding history. A session older than
// the oldest retained event may have had hook events that are gone, so it is not reported as missed.
func TestDshHookCaptureIgnoresSessionsOlderThanRetainedHistory(t *testing.T) {
	f := newDshDoctorFixture(t)
	historyStart := f.installedAt.Add(10 * time.Hour)
	for i := writer.DefaultRotateArchives; i >= 1; i-- {
		f.writeArchive(t, i, dshLogLine("poll", historyStart.Add(time.Duration(writer.DefaultRotateArchives-i)*time.Minute)))
	}
	f.session(t, "before-history", f.installedAt.Add(time.Hour))

	if got := f.check(); got.Status != diagnostics.StatusOK {
		t.Fatalf("status = %s, want ok for a session older than the retained log history (%s)", got.Status, got.Message)
	}

	// A session inside the retained history with no hook event is still flagged.
	f.session(t, "inside-history", historyStart.Add(time.Hour))
	if got := f.check(); got.Status != diagnostics.StatusWarn || !strings.Contains(got.Message, "1 DeepSeek Harness session(s)") {
		t.Fatalf("status = %s, want warn counting only the session inside the history (%s)", got.Status, got.Message)
	}
}
