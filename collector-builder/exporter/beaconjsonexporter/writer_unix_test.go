//go:build !windows

package beaconjsonexporter

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The collector runs elevated in a system-mode install and creates the runtime log, while hooks
// running as the console user append to the same file. A restrictive umask would leave the hooks
// unable to write, and telemetry would stop while the collector looked healthy.
//
// Unix-only because umask is. On Windows the equivalent property is an ACL on the system log
// directory, which is a real gap there rather than a non-issue.

func TestAppendJSONLCreatesSharedRuntimeFilesDespiteUmask(t *testing.T) {
	oldUmask := syscall.Umask(0022)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := appendJSONL(path, []byte(`{"message":"test"}`+"\n"), defaultRotateBytes, defaultRotateArchives); err != nil {
		t.Fatalf("appendJSONL returned error: %v", err)
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
