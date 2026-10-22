package dashboard

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
)

// The dashboard keeps its own hand-written list of hook runtimes, separate from the CLI's target
// registry, so a runtime wired into `beacon endpoint hooks` but not here is simply invisible in the
// inventory view -- an operator checking coverage would conclude it is unsupported. Each row also has
// to carry the path an operator opens: "not_installed" with no path tells them nothing. Where the
// runtime's status names several files, the expected path pins which one the row reports.
func TestHookStatusesListEachRuntimeOnceWithItsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range []string{"CLINE_DIR", "QWEN_HOME", "DSH_HOME", "KIMI_CODE_HOME", "KIRO_HOME", "OH_PERSISTENCE_DIR"} {
		t.Setenv(env, "")
	}
	dshtest.Ready(t, filepath.Join(home, ".dsh"), "0.2.1-rc.2")

	rows := map[string][]HookStatus{}
	for _, status := range hookStatuses(filepath.Join(t.TempDir(), "runtime.jsonl"), true) {
		rows[status.Target] = append(rows[status.Target], status)
	}
	for target, wantPath := range map[string]string{
		"cline":     filepath.Join(home, ".cline", "plugins", "beacon.ts"),
		"qwen":      filepath.Join(home, ".qwen", "settings.json"),
		"opencode":  "",
		"dsh":       "",
		"kimi":      "",
		"kiro":      "",
		"openhands": "",
	} {
		if len(rows[target]) != 1 {
			t.Errorf("%q appears %d times in the hook status list, want 1", target, len(rows[target]))
			continue
		}
		status := rows[target][0]
		if status.Installed || status.Status != "not_installed" {
			t.Errorf("%s reported %q (installed=%v) with nothing installed", target, status.Status, status.Installed)
		}
		if status.Path == "" || (wantPath != "" && status.Path != wantPath) {
			t.Errorf("%s path = %q, want %q", target, status.Path, wantPath)
		}
	}
}
