package beaconjsonexporter

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

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
