package config

import (
	"os"
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
