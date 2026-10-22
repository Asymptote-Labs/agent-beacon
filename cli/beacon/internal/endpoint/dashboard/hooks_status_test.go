package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
)

// The dashboard keeps its own hand-written list of hook runtimes, separate from the CLI's target
// registry, so a runtime wired into `beacon endpoint hooks` but not here is simply invisible in the
// inventory view -- an operator checking coverage would conclude it is unsupported. Each row also has
// to carry the path an operator opens: "not_installed" with no path tells them nothing. Where the
// runtime's status names several files, the expected path pins which one the row reports. And a
// row flips once its hooks are installed, rather than being a permanently empty placeholder.
func TestHookStatusesListEachRuntimeOnceWithItsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range []string{"CLINE_DIR", "QWEN_HOME", "DSH_HOME", "KIMI_CODE_HOME", "KIRO_HOME", "OH_PERSISTENCE_DIR"} {
		t.Setenv(env, "")
	}
	dshtest.Ready(t, filepath.Join(home, ".dsh"), "0.2.1-rc.2")

	rows := map[string][]HookStatus{}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	for _, status := range hookStatuses(logPath, true) {
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

	for _, dir := range []string{filepath.Join(home, ".kiro", "hooks"), filepath.Join(home, ".openhands")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	level := endpointhooks.LevelUser
	for target, install := range map[string]func() error{
		"dsh": func() error {
			_, err := endpointhooks.InstallDsh(endpointhooks.DshOptions{Level: level, LogPath: logPath, UserMode: true})
			return err
		},
		"kimi": func() error {
			_, err := endpointhooks.InstallKimi(endpointhooks.KimiOptions{Level: level, LogPath: logPath, UserMode: true})
			return err
		},
		"kiro": func() error {
			_, err := endpointhooks.InstallKiro(endpointhooks.KiroOptions{Level: level, LogPath: logPath, UserMode: true})
			return err
		},
		"openhands": func() error {
			_, err := endpointhooks.InstallOpenHands(endpointhooks.OpenHandsOptions{Level: level, LogPath: logPath, UserMode: true})
			return err
		},
	} {
		if err := install(); err != nil {
			t.Fatalf("install %s: %v", target, err)
		}
	}
	installed := map[string]HookStatus{}
	for _, status := range hookStatuses(logPath, true) {
		installed[status.Target] = status
	}
	for _, target := range []string{"dsh", "kimi", "kiro", "openhands"} {
		if status := installed[target]; !status.Installed || status.Status != "configured" {
			t.Errorf("%s still reported %q (installed=%v) after an install", target, status.Status, status.Installed)
		}
	}
}
