package writer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureSystemLogWritableCreatesTheDirectoryItIsAskedAbout covers an ordering bug that took
// down the whole install.
//
// Install calls this before anything has written a log, so on a fresh system-mode install the
// directory does not exist yet. The Windows implementation grants access by path; granting access
// to a path that is not there fails, and the failure came back from Install as "prepare the log
// directory" before the service was ever registered -- so the first install on a clean machine was
// the one case that could not work.
//
// Platform-neutral on purpose. The POSIX grant is a no-op and the Windows one is not, but the
// directory has to exist either way, and keeping that guarantee in the shared function is what
// stops the next platform from reintroducing it.
func TestEnsureSystemLogWritableCreatesTheDirectoryItIsAskedAbout(t *testing.T) {
	// Nested, so this fails if the implementation only creates a leaf under an existing parent.
	dir := filepath.Join(t.TempDir(), "Beacon", "Endpoint", "logs")

	if err := EnsureSystemLogWritable(dir); err != nil {
		t.Fatalf("EnsureSystemLogWritable on a directory that does not exist yet: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the log directory afterwards: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
}

func TestEnsureSystemLogWritableIsRepeatable(t *testing.T) {
	// Repair and reinstall both call this over a live endpoint, so a second call must not fail on
	// the directory it created the first time.
	dir := filepath.Join(t.TempDir(), "logs")
	for i := 0; i < 2; i++ {
		if err := EnsureSystemLogWritable(dir); err != nil {
			t.Fatalf("EnsureSystemLogWritable call %d: %v", i+1, err)
		}
	}
}
