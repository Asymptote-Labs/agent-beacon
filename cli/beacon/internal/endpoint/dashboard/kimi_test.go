package dashboard

import (
	"path/filepath"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The dashboard's harness list is a hand-written sequence of calls, one per runtime, so a runtime
// left out of it is simply absent from the page with nothing to say it should have been there. An
// operator checking their coverage would conclude Kimi Code is not supported.
func TestHookStatusesIncludesKimi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIMI_CODE_HOME", "")

	logPath := filepath.Join(home, "runtime.jsonl")
	find := func() *HookStatus {
		statuses := hookStatuses(logPath, true)
		for i := range statuses {
			if statuses[i].Target == "kimi" {
				return &statuses[i]
			}
		}
		return nil
	}

	kimi := find()
	if kimi == nil {
		t.Fatal("hookStatuses omits kimi; the dashboard would show it as an unsupported runtime")
	}
	if kimi.Installed {
		t.Fatalf("kimi reported installed with no config on the machine: %+v", kimi)
	}
	// The path is what an operator opens to check for themselves, and on this runtime it is the
	// runtime's own config rather than a file Beacon owns -- so an empty one would send them
	// looking for a Beacon file that does not exist.
	if kimi.Path == "" {
		t.Fatalf("kimi reported no path: %+v", kimi)
	}

	if _, err := endpointhooks.InstallKimi(endpointhooks.KimiOptions{
		Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true,
	}); err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	after := find()
	if after == nil {
		t.Fatal("kimi disappeared from hookStatuses after an install")
	}
	if !after.Installed {
		t.Fatalf("kimi still reported not installed after an install: %+v", after)
	}
}
