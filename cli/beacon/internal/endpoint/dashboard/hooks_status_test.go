package dashboard

import (
	"path/filepath"
	"testing"
)

// The dashboard keeps its own list of hook runtimes, separate from the CLI's target registry, so a
// runtime wired into `beacon endpoint hooks` but not here is simply invisible in the inventory view
// -- with no error anywhere to say the view is incomplete.
//
// Asserted for both plugin-shaped runtimes together because they are the two whose status comes from
// a plugin file rather than a settings file, and they are the pair most likely to be updated as one.
func TestHookStatusesIncludeThePluginRuntimes(t *testing.T) {
	statuses := hookStatuses(filepath.Join(t.TempDir(), "runtime.jsonl"), true)

	byTarget := map[string]int{}
	for _, status := range statuses {
		byTarget[status.Target]++
	}
	for _, target := range []string{"cline", "opencode"} {
		switch byTarget[target] {
		case 1:
		case 0:
			t.Errorf("hook status list is missing %q", target)
		default:
			t.Errorf("%q appears %d times in the hook status list", target, byTarget[target])
		}
	}
}

// A status row with no path tells an operator nothing about where to look, and "not_installed" is
// the answer every row must be able to give without a Cline install present.
func TestHookStatusesReportClineWithItsPluginPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, status := range hookStatuses(filepath.Join(t.TempDir(), "runtime.jsonl"), true) {
		if status.Target != "cline" {
			continue
		}
		if status.Installed {
			t.Errorf("cline reported installed with no plugin present: %+v", status)
		}
		if status.Status != "not_installed" {
			t.Errorf("cline status = %q, want not_installed", status.Status)
		}
		if want := filepath.Join(home, ".cline", "plugins", "beacon.ts"); status.Path != want {
			t.Errorf("cline path = %q, want %q", status.Path, want)
		}
		return
	}
	t.Fatal("no cline row in the hook status list")
}
