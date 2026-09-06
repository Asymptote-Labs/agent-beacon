package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The dashboard's harness list is a hand-written sequence of calls, one per runtime, so a runtime
// left out of it is simply absent from the page with nothing to say it should have been there.
// That is the failure this guards: an operator looking at the dashboard to check their coverage
// would conclude OpenHands is not a supported runtime.
func TestHookStatusesIncludesOpenHands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OH_PERSISTENCE_DIR", "")

	logPath := filepath.Join(home, "runtime.jsonl")
	var openhands *HookStatus
	statuses := hookStatuses(logPath, true)
	for i := range statuses {
		if statuses[i].Target == "openhands" {
			openhands = &statuses[i]
			break
		}
	}
	if openhands == nil {
		t.Fatal("hookStatuses omits openhands; the dashboard would show it as an unsupported runtime")
	}
	if openhands.Installed {
		t.Fatalf("openhands reported installed with no hooks.json present: %+v", openhands)
	}
	if openhands.Path == "" {
		t.Fatalf("openhands reported no path: %+v", openhands)
	}

	// And it flips once the hooks are actually there, so the row reflects the install rather than
	// being a permanently empty placeholder.
	if err := os.MkdirAll(filepath.Join(home, ".openhands"), 0755); err != nil {
		t.Fatalf("create .openhands: %v", err)
	}
	if _, err := endpointhooks.InstallOpenHands(endpointhooks.OpenHandsOptions{
		Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true,
	}); err != nil {
		t.Fatalf("InstallOpenHands: %v", err)
	}
	for _, status := range hookStatuses(logPath, true) {
		if status.Target != "openhands" {
			continue
		}
		if !status.Installed {
			t.Fatalf("openhands still reported not installed after an install: %+v", status)
		}
		return
	}
	t.Fatal("openhands disappeared from hookStatuses after an install")
}
