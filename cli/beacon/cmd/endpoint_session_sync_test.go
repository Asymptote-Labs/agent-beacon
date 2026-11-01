package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

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
	oldOpts, oldSweep := endpointOpts, runSessionSweep
	t.Cleanup(func() { endpointOpts, runSessionSweep = oldOpts, oldSweep; endpointSessionsOpts.scheduled = false })
	endpointOpts.userMode, endpointOpts.systemMode = true, false

	var gotLog string
	runSessionSweep = func(logPath string, userMode bool) endpointBackfillResult {
		gotLog = logPath
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
