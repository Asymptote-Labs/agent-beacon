package cmd

import (
	"bytes"
	"errors"
	"path/filepath"
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
	var gotWait []time.Duration
	runSessionSweep = func(logPath string, userMode bool, wait time.Duration) endpointBackfillResult {
		gotLog = logPath
		gotWait = append(gotWait, wait)
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
	// The scheduled run skips while another sweep runs; a manual one waits like install.
	if len(gotWait) != 2 || gotWait[0] != 0 || gotWait[1] <= 0 {
		t.Fatalf("lock waits = %v, want [0, >0]", gotWait)
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
	runSessionSweep = func(string, bool, time.Duration) endpointBackfillResult { swept++; return endpointBackfillResult{} }
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
	result := runEndpointBackfillLocked(filepath.Join(t.TempDir(), "runtime.jsonl"), true, 0)
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
