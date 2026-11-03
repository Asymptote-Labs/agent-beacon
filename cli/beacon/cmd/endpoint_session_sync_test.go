package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestSessionSyncOption(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })

	endpointOpts.sessionSync, endpointOpts.noSessionSync = false, false
	if got, err := sessionSyncOption(); err != nil || got != nil {
		t.Fatalf("no flag = %v, %v; want nil so repair keeps the setting", got, err)
	}
	endpointOpts.sessionSync = true
	if got, err := sessionSyncOption(); err != nil || got == nil || !*got {
		t.Fatalf("--session-sync = %v, %v", got, err)
	}
	endpointOpts.sessionSync, endpointOpts.noSessionSync = false, true
	if got, err := sessionSyncOption(); err != nil || got == nil || *got {
		t.Fatalf("--no-session-sync = %v, %v", got, err)
	}
	endpointOpts.sessionSync = true
	if _, err := sessionSyncOption(); err == nil {
		t.Fatal("both flags together should be refused")
	}
}

// The scheduled job reports a runtime that failed and still exits cleanly, so the scheduler keeps
// running it; run by hand, the same failure is an error.
func TestSessionsSyncReportsAndOnlyFailsByHand(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	enableSessionSyncConfig(t, true)
	oldOpts, oldSweep := endpointOpts, runSessionSweep
	t.Cleanup(func() { endpointOpts, runSessionSweep = oldOpts, oldSweep; endpointSessionsOpts.scheduled = false })
	endpointOpts.userMode, endpointOpts.systemMode = true, false

	var gotLog string
	var gotScheduled []bool
	runSessionSweep = func(logPath string, userMode, scheduled bool) endpointBackfillResult {
		gotLog = logPath
		gotScheduled = append(gotScheduled, scheduled)
		return endpointBackfillResult{Events: 3, Runtimes: []endpointBackfillRuntimeResult{
			{Name: "Claude Code", Sync: "beacon endpoint claude sync", Events: 3},
			{Name: "Codex CLI", Sync: "beacon endpoint codex sync", Err: errors.New("store unreadable")},
		}}
	}

	var out bytes.Buffer
	endpointSessionsSyncCmd.SetOut(&out)
	endpointSessionsOpts.scheduled = true
	if err := runEndpointSessionsSync(endpointSessionsSyncCmd, nil); err != nil {
		t.Fatalf("scheduled run returned %v", err)
	}
	if !strings.Contains(out.String(), "Claude Code 3") || !strings.Contains(out.String(), "Codex CLI: store unreadable") {
		t.Fatalf("report = %q", out.String())
	}
	if !strings.HasSuffix(gotLog, "runtime.jsonl") {
		t.Fatalf("swept into %q, want the endpoint's runtime log", gotLog)
	}

	endpointSessionsOpts.scheduled = false
	if err := runEndpointSessionsSync(endpointSessionsSyncCmd, nil); err == nil || !strings.Contains(err.Error(), "Codex CLI") {
		t.Fatalf("manual run error = %v, want the failed runtime named", err)
	}
	if len(gotScheduled) != 2 || !gotScheduled[0] || gotScheduled[1] {
		t.Fatalf("sweeps scheduled = %v, want [true false]", gotScheduled)
	}
}

// A job that outlived the setting writes nothing; a manual run is not gated.
func TestScheduledSessionsSyncHonoursConfigOff(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	enableSessionSyncConfig(t, false)
	oldOpts, oldSweep := endpointOpts, runSessionSweep
	t.Cleanup(func() { endpointOpts, runSessionSweep = oldOpts, oldSweep; endpointSessionsOpts.scheduled = false })
	endpointOpts.userMode, endpointOpts.systemMode = true, false

	swept := 0
	runSessionSweep = func(string, bool, bool) endpointBackfillResult { swept++; return endpointBackfillResult{} }
	var out bytes.Buffer
	endpointSessionsSyncCmd.SetOut(&out)
	endpointSessionsOpts.scheduled = true
	if err := runEndpointSessionsSync(endpointSessionsSyncCmd, nil); err != nil {
		t.Fatal(err)
	}
	if swept != 0 || !strings.Contains(out.String(), "Session sync is off") {
		t.Fatalf("scheduled run with the setting off swept %d time(s), output %q", swept, out.String())
	}
	endpointSessionsOpts.scheduled = false
	if err := runEndpointSessionsSync(endpointSessionsSyncCmd, nil); err != nil || swept != 1 {
		t.Fatalf("manual run: err=%v swept=%d", err, swept)
	}
}

// Two sweeps never run together: the second waits for, or (not waiting) skips, the first.
func TestEndpointSweepLockSerializes(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	release, err := acquireEndpointSweepLock(true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireEndpointSweepLock(true, 0); !errors.Is(err, errEndpointSweepBusy) {
		t.Fatalf("second sweep without waiting = %v, want busy", err)
	}
	if _, err := acquireEndpointSweepLock(true, 50*time.Millisecond); !errors.Is(err, errEndpointSweepBusy) {
		t.Fatalf("second sweep after a short wait = %v, want busy", err)
	}
	result := runEndpointBackfillLocked(filepath.Join(t.TempDir(), "runtime.jsonl"), true, 0, sweepScheduledSync)
	if result.Skipped == "" || result.Events != 0 {
		t.Fatalf("locked-out sweep = %+v, want skipped", result)
	}
	release()
	again, err := acquireEndpointSweepLock(true, 0)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

// A per-runtime sync shares the sweep's cursor files, so it takes the same lock: while a sweep runs,
// a sync that writes waits and then gives up without reading or writing anything, and a --print dry
// run, which moves no cursor, is not held up.
func TestPerRuntimeSyncWaitsForTheSweepLock(t *testing.T) {
	f := newClaudeCommandFixture(t)
	oldWait := endpointSweepLockWait
	t.Cleanup(func() { endpointSweepLockWait = oldWait; endpointClaudeOpts.print = false })
	endpointSweepLockWait = 50 * time.Millisecond
	release, err := acquireEndpointSweepLock(true, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runClaudeSync(t); !errors.Is(err, errEndpointSweepBusy) {
		t.Fatalf("sync during a sweep = %v, want busy", err)
	}
	if _, statErr := os.Stat(f.logPath); !os.IsNotExist(statErr) {
		t.Fatal("a sync that could not take the lock wrote the runtime log")
	}
	endpointClaudeOpts.print = true
	if out, err := runClaudeSync(t); err != nil || !strings.Contains(out, "sess-1") {
		t.Fatalf("--print during a sweep: err=%v out=%q", err, out)
	}
	endpointClaudeOpts.print = false

	release()
	if _, err := runClaudeSync(t); err != nil {
		t.Fatalf("sync after the sweep: %v", err)
	}
	if _, statErr := os.Stat(f.statePath); statErr != nil {
		t.Fatalf("sync after the sweep did not save its cursor: %v", statErr)
	}
}

// Every per-runtime sync command reaches its collector through collectUnderSweepLock, so a new
// `beacon endpoint <runtime> sync` cannot race the scheduled session sync by calling it directly.
// The backfill runners are exempt: runEndpointBackfillLocked holds the lock around all of them.
func TestPerRuntimeSyncCommandsTakeTheSweepLock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`\b\w+session\.CollectOnce\(`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "endpoint_backfill.go" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if direct.MatchString(line) {
				t.Errorf("%s:%d calls a session-store collector without the sweep lock; use collectUnderSweepLock", file, i+1)
			}
		}
	}
}

// An install or connect that skipped its backfill leaves a floor: a session sync then reads only
// sessions active since, instead of the 30 days the operator opted out of. A backfill that runs
// reads the whole window again and lifts the floor.
func TestSkippedBackfillFloorsTheSessionSync(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateSweepState(t)
	var since time.Time
	stubBackfill(t, 1<<20, 1<<20, writingRunner("Claude", 1, &since))
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	now := endpointBackfillNow()
	window := now.Add(-endpointBackfillWindow)

	recordSkippedBackfill(&bytes.Buffer{}, true)
	result := runSessionSweep(logPath, true, true)
	if !since.Equal(now) || !result.Floor {
		t.Fatalf("session sync after a skipped backfill read from %s (floor %t), want %s", since, result.Floor, now)
	}
	var out bytes.Buffer
	reportSessionSweep(&out, "Session sync", result)
	if !strings.Contains(out.String(), "since the skipped backfill at "+now.UTC().Format(time.RFC3339)) {
		t.Fatalf("report = %q", out.String())
	}

	runEndpointBackfill(logPath, true)
	if !since.Equal(window) {
		t.Fatalf("backfill read from %s, want the whole window from %s", since, window)
	}
	if result := runSessionSweep(logPath, true, true); !since.Equal(window) || result.Floor {
		t.Fatalf("session sync after a backfill read from %s (floor %t), want the window again", since, result.Floor)
	}

	// A dry run records nothing.
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	endpointOpts.dryRun = true
	recordSkippedBackfill(&bytes.Buffer{}, true)
	if _, err := os.Stat(sessionSyncFloorPath(true)); !os.IsNotExist(err) {
		t.Fatalf("a dry run recorded a floor: %v", err)
	}
}

// Only the first skipped backfill sets a floor. A later skip must not move it, and an endpoint whose
// cursors already exist gets none: a floor set then would drop sessions that went idle with records
// the cursors have not reached.
func TestSkippedBackfillNeverMovesTheFloorOrHidesUnreadRecords(t *testing.T) {
	stubBackfill(t, 1<<20, 1<<20)
	isolateSweepState(t)
	stateDir := filepath.Dir(sessionSyncFloorPath(true))
	first := endpointBackfillNow()

	recordSkippedBackfill(&bytes.Buffer{}, true)
	endpointBackfillNow = func() time.Time { return first.Add(time.Hour) }
	recordSkippedBackfill(&bytes.Buffer{}, true)
	if got := readSessionSyncFloor(true); !got.Equal(first) {
		t.Fatalf("a second skipped backfill moved the floor to %s, want %s", got, first)
	}

	_ = os.Remove(sessionSyncFloorPath(true))
	if err := os.WriteFile(filepath.Join(stateDir, "claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	recordSkippedBackfill(&bytes.Buffer{}, true)
	if got := readSessionSyncFloor(true); !got.IsZero() {
		t.Fatalf("a skipped backfill beside existing cursors set a floor at %s", got)
	}
}

// The scheduled system sync copies a SQLite store at most once an hour; JSONL stores, read in place,
// are read every run. Nothing else throttles.
func TestScheduledSystemSyncCopiesSQLiteStoresHourly(t *testing.T) {
	isolateSweepState(t)
	runs := map[string]int{}
	runner := func(name string, sqlite bool) endpointBackfillRunner {
		return endpointBackfillRunner{Name: name, Sync: "beacon endpoint x sync", SQLite: sqlite, Run: func(endpointBackfillRun) (int, error) {
			runs[name]++
			return 0, nil
		}}
	}
	stubBackfill(t, 1<<20, 1<<20, runner("Claude Code", false), runner("Cursor", true))
	start := endpointBackfillNow()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	throttled := endpointSweep{ThrottleSnapshots: true}

	for _, offset := range []time.Duration{0, 15 * time.Minute, 45 * time.Minute, time.Hour} {
		endpointBackfillNow = func() time.Time { return start.Add(offset) }
		sweepEndpointBackfill(logPath, false, throttled)
	}
	if runs["Claude Code"] != 4 || runs["Cursor"] != 2 {
		t.Fatalf("runs = %v, want Claude Code every run and Cursor at 0 and 60 minutes", runs)
	}

	sweepEndpointBackfill(logPath, false, endpointSweep{})
	if runs["Cursor"] != 3 {
		t.Fatalf("an unthrottled sweep skipped the SQLite store: runs = %v", runs)
	}
}

// isolateSweepState gives a test its own sweep lock and floor, away from the directory TestMain shares
// with every install test (each of which skips its backfill and so may leave a floor there).
func isolateSweepState(t *testing.T) {
	t.Helper()
	old := endpointSweepLockPath
	t.Cleanup(func() { endpointSweepLockPath = old })
	dir := t.TempDir()
	endpointSweepLockPath = func(bool) string { return filepath.Join(dir, "session-sweep.lock") }
}

func enableSessionSyncConfig(t *testing.T, enabled bool) {
	t.Helper()
	cfg := endpointconfig.Default(true, filepath.Join(endpointconfig.BaseDir(true), "logs", "runtime.jsonl"))
	cfg.SessionSync = &endpointconfig.SessionSync{Enabled: enabled}
	if _, err := endpointconfig.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSyncStatusLine(t *testing.T) {
	if got := sessionSyncStatusLine(lifecycle.SessionSyncStatus{}); got != "Session sync: off" {
		t.Fatalf("off = %q", got)
	}
	on := lifecycle.SessionSyncStatus{Enabled: true, Interval: "15m0s"}
	on.Job.Loaded = true
	if got := sessionSyncStatusLine(on); got != "Session sync: every 15m0s; job loaded=true" {
		t.Fatalf("on = %q", got)
	}
}
