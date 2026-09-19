package dashboard

import (
	"path/filepath"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The dashboard's harness list is a hand-written sequence of calls, one per runtime, so a runtime
// left out of it is simply absent from the page with nothing to say it should have been there. An
// operator checking their coverage would conclude DeepSeek Harness is not supported.
func TestHookStatusesIncludesDsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")

	logPath := filepath.Join(home, "runtime.jsonl")
	find := func() *HookStatus {
		statuses := hookStatuses(logPath, true)
		for i := range statuses {
			if statuses[i].Target == "dsh" {
				return &statuses[i]
			}
		}
		return nil
	}

	dsh := find()
	if dsh == nil {
		t.Fatal("hookStatuses omits dsh; the dashboard would show it as an unsupported runtime")
	}
	if dsh.Installed {
		t.Fatalf("dsh reported installed with nothing in the Harness home: %+v", dsh)
	}
	if dsh.Path == "" {
		t.Fatalf("dsh reported no path: %+v", dsh)
	}

	if _, err := endpointhooks.InstallDsh(endpointhooks.DshOptions{
		Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true,
	}); err != nil {
		t.Fatalf("InstallDsh: %v", err)
	}
	after := find()
	if after == nil {
		t.Fatal("dsh disappeared from hookStatuses after an install")
	}
	if !after.Installed {
		t.Fatalf("dsh still reported not installed after an install: %+v", after)
	}
}
