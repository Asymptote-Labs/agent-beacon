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
// would conclude Kiro is not a supported runtime.
func TestHookStatusesIncludesKiro(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIRO_HOME", "")

	logPath := filepath.Join(home, "runtime.jsonl")
	var kiro *HookStatus
	statuses := hookStatuses(logPath, true)
	for i := range statuses {
		if statuses[i].Target == "kiro" {
			kiro = &statuses[i]
			break
		}
	}
	if kiro == nil {
		t.Fatal("hookStatuses omits kiro; the dashboard would show it as an unsupported runtime")
	}
	if kiro.Installed {
		t.Fatalf("kiro reported installed with no hook file present: %+v", kiro)
	}
	if kiro.Path == "" {
		t.Fatalf("kiro reported no path: %+v", kiro)
	}

	// And it flips once the hooks are actually there, so the row reflects the install rather than
	// being a permanently empty placeholder.
	if err := os.MkdirAll(filepath.Join(home, ".kiro", "hooks"), 0755); err != nil {
		t.Fatalf("create .kiro/hooks: %v", err)
	}
	if _, err := endpointhooks.InstallKiro(endpointhooks.KiroOptions{
		Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true,
	}); err != nil {
		t.Fatalf("InstallKiro: %v", err)
	}
	for _, status := range hookStatuses(logPath, true) {
		if status.Target != "kiro" {
			continue
		}
		if !status.Installed {
			t.Fatalf("kiro still reported not installed after an install: %+v", status)
		}
		return
	}
	t.Fatal("kiro disappeared from hookStatuses after an install")
}
