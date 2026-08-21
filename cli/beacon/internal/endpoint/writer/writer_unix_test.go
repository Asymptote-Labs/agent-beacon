//go:build !windows

package writer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// The runtime log has to be writable by more than its creator: a system-mode collector runs
// elevated and creates it, while hooks running as the console user append to it. A restrictive
// umask on the creating process would otherwise silently produce a log the hooks cannot write, and
// telemetry would stop while status and doctor -- which describe the collector, not who exports to
// it -- kept reporting healthy.
//
// Unix-only because umask is. The Windows equivalent of this property is an ACL on the system log
// directory rather than a file mode, and it is a real gap there rather than a non-issue: %ProgramData%
// inherits an ACL that denies non-admins write access. That belongs with the Windows system paths.
func TestAppendEventCreatesSharedRuntimeFilesDespiteUmask(t *testing.T) {
	oldUmask := syscall.Umask(0022)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "test event",
	})
	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}

	for _, target := range []string{path, path + ".lock"} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat %s: %v", target, err)
		}
		if got := info.Mode().Perm(); got != runtimeFileMode {
			t.Fatalf("%s mode = %o, want %o", target, got, runtimeFileMode)
		}
	}
}

func TestEnsureRuntimeFileCreatesSharedRuntimeLogDespiteUmask(t *testing.T) {
	oldUmask := syscall.Umask(0022)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := EnsureRuntimeFile(path); err != nil {
		t.Fatalf("EnsureRuntimeFile returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != runtimeFileMode {
		t.Fatalf("%s mode = %o, want %o", path, got, runtimeFileMode)
	}
}
