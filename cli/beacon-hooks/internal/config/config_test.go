package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetStateDir(t *testing.T) {
	tests := []struct {
		platform string
		wantDir  string
	}{
		{"claude", ClaudeDir},
		{"copilot", CopilotDir},
		{"cursor", CursorDir},
		{"vscode", VSCodeDir},
		// An unregistered platform falls back to ClaudeDir, so this row is what proves cline is
		// registered at all: without the case, cline's hook log and per-session state would be
		// written into Claude Code's state directory and the two runtimes would share files.
		{"cline", ClineDir},
		{"unknown", ClaudeDir}, // defaults to claude
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			got := GetStateDir(tt.platform)
			if got != tt.wantDir {
				t.Errorf("GetStateDir(%q) = %q, want %q", tt.platform, got, tt.wantDir)
			}
		})
	}
}

func TestRotateLogIfNeededForPlatformArchivesExistingLog(t *testing.T) {
	dir := t.TempDir()
	oldCursorDir := CursorDir
	CursorDir = dir
	t.Cleanup(func() {
		CursorDir = oldCursorDir
	})
	logFile := GetLogFile("cursor")
	if err := os.WriteFile(logFile, []byte("old log"), 0644); err != nil {
		t.Fatalf("write hook log: %v", err)
	}
	if err := os.WriteFile(logFile+".1", []byte("older log"), 0644); err != nil {
		t.Fatalf("write hook archive: %v", err)
	}
	if err := os.Truncate(logFile, LogMaxSizeBytes+1); err != nil {
		t.Fatalf("inflate hook log: %v", err)
	}

	if !RotateLogIfNeededForPlatform("cursor") {
		t.Fatal("RotateLogIfNeededForPlatform returned false, want rotation")
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("active log should be rotated away, stat err=%v", err)
	}
	if info, err := os.Stat(logFile + ".1"); err != nil || info.Size() != LogMaxSizeBytes+1 {
		t.Fatalf(".1 archive info=%v err=%v, want rotated active log", info, err)
	}
	if data, err := os.ReadFile(logFile + ".2"); err != nil || string(data) != "older log" {
		t.Fatalf(".2 archive = %q err=%v, want older log", string(data), err)
	}
}

// A sandbox that assembles its own root filesystem often leaves the home out. The hook's state
// directories must not conjure one there: it would vanish with the sandbox, and make the account's
// event spool beneath it look available when it is not.
func TestStateDirsNeverCreateAMissingHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing-home")
	oldBeaconDir, oldCursorDir := BeaconDir, CursorDir
	BeaconDir = filepath.Join(home, ".beacon")
	CursorDir = filepath.Join(BeaconDir, "cursor")
	t.Cleanup(func() { BeaconDir, CursorDir = oldBeaconDir, oldCursorDir })

	if err := EnsureStateDir("cursor"); err == nil {
		t.Fatal("EnsureStateDir succeeded under a home that does not exist")
	}
	if err := EnsureSessionLogDir("cursor"); err == nil {
		t.Fatal("EnsureSessionLogDir succeeded under a home that does not exist")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("the home was created: %v", err)
	}

	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSessionLogDir("cursor"); err != nil {
		t.Fatalf("EnsureSessionLogDir under an existing home = %v", err)
	}
}
