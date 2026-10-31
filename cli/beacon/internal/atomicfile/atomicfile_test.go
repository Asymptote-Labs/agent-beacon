package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestWriteSystemPrivateReplacesTheFileAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSystemPrivate(path, []byte("token: hec\n")); err != nil {
		t.Fatalf("WriteSystemPrivate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "token: hec\n" {
		t.Fatalf("content = %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries after the write, want just the file", len(entries))
	}
	if testenv.HasPOSIXFileModes() {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode = %o, want 0600", got)
		}
	}
}
