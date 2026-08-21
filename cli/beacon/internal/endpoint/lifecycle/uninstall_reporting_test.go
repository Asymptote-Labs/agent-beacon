package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure this exists to catch: `beacon endpoint uninstall --system` without privileges used to
// print "Endpoint service, config, and managed files removed." and exit 0 while the systemd unit
// stayed enabled, so the collector returned at the next reboot after the operator had been told it
// was gone. Install has always refused unprivileged system writes; uninstall never did.
func TestUninstallRefusesAnUnprivilegedSystemRemoval(t *testing.T) {
	if HasSystemPrivileges() {
		t.Skip("running as root, so the unprivileged path cannot be exercised here")
	}

	err := Uninstall(UninstallOptions{UserMode: false})
	if err == nil {
		t.Fatal("an unprivileged system uninstall returned nil, which the CLI prints as success")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error %q should say privileges are the problem, so the operator knows to rerun with sudo", err)
	}
}

// A user-mode uninstall needs no privileges and must not be caught by that gate.
func TestUninstallAllowsUserModeWithoutPrivileges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := Uninstall(UninstallOptions{UserMode: true}); err != nil {
		t.Errorf("user-mode uninstall failed with nothing installed: %v", err)
	}
}

// Rotation turns one log into six files. Removing only runtime.jsonl left the archives -- up to
// 50 MB of retained prompt text and command lines -- on disk after an uninstall that was not asked
// to keep logs, and invisible, because the file an operator would look for was gone.
func TestUninstallRemovesRotatedArchivesToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logDir := filepath.Join(home, ".beacon", "endpoint", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "runtime.jsonl")
	written := []string{logPath, logPath + ".1", logPath + ".2", logPath + ".lock"}
	for _, p := range written {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := Uninstall(UninstallOptions{UserMode: true, LogPath: logPath}); err != nil {
		t.Fatal(err)
	}

	for _, p := range written {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived an uninstall that was not asked to keep logs", filepath.Base(p))
		}
	}
}

// --keep-logs has to keep all of them, not just the current one.
func TestUninstallKeepLogsKeepsTheArchives(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logDir := filepath.Join(home, ".beacon", "endpoint", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "runtime.jsonl")
	for _, p := range []string{logPath, logPath + ".1"} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := Uninstall(UninstallOptions{UserMode: true, LogPath: logPath, KeepLogs: true}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{logPath, logPath + ".1"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed despite --keep-logs", filepath.Base(p))
		}
	}
}
