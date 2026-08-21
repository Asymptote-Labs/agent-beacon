//go:build !windows

package logging

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The hook adapter appends to a log a system-mode collector created while running elevated, so the
// file has to stay writable by both. A restrictive umask on whichever process creates it first
// would otherwise produce a log the other cannot write, and telemetry would stop while status kept
// reporting healthy.
//
// Unix-only because umask is. On Windows the same property is an ACL on the system log directory
// rather than a file mode, and there it is a real gap rather than a non-issue.

func TestEndpointEventCreatesSharedRuntimeFilesDespiteUmask(t *testing.T) {
	oldUmask := syscall.Umask(0022)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("agent-thought", "cursor")
	if err := logger.EndpointEvent("agent.reasoning", "session", "info", "Agent reasoning captured", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	for _, target := range []string{logPath, logPath + ".lock"} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat %s: %v", target, err)
		}
		if got := info.Mode().Perm(); got != endpointRuntimeFileMode {
			t.Fatalf("%s mode = %o, want %o", target, got, endpointRuntimeFileMode)
		}
	}
}
