package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// appendForRotationTest appends with a size limit every line in these tests goes past.
func appendForRotationTest(path string, line []byte) error {
	return appendJSONL(path, line, 8, DefaultRotateArchives, nil, nil)
}

// rotationBlockedFixture is a log over its size limit with archives .1 to .4 and, where the oldest
// archive goes, a directory that is not empty. A rotation would have to remove it first, which
// every platform refuses, so this blocks rotation the way a held file does on Windows.
func rotationBlockedFixture(t *testing.T) (path, blocker string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(path, []byte("{\"existing\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.%d", path, i), []byte(fmt.Sprintf("{\"archive\":%d}\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	blocker = path + ".5"
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, blocker
}

func readRotationFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A rotation that cannot happen is deferred and the line still lands, past the size limit. It used
// to be returned as an error before the line was written, which dropped the event. Once the blocker
// is gone the next write rotates normally.
func TestAppendKeepsTheLineWhenRotationCannotHappen(t *testing.T) {
	path, blocker := rotationBlockedFixture(t)
	if err := appendForRotationTest(path, []byte("{\"next\":true}\n")); err != nil {
		t.Fatalf("append with rotation blocked: %v", err)
	}
	if got, want := readRotationFile(t, path), "{\"existing\":true}\n{\"next\":true}\n"; got != want {
		t.Fatalf("log = %q, want the new line appended past the limit", got)
	}
	for i := 1; i <= 4; i++ {
		if got, want := readRotationFile(t, fmt.Sprintf("%s.%d", path, i)), fmt.Sprintf("{\"archive\":%d}\n", i); got != want {
			t.Fatalf("archive %d = %q after a rotation that could not happen, want %q", i, got, want)
		}
	}

	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if err := appendForRotationTest(path, []byte("{\"after\":true}\n")); err != nil {
		t.Fatal(err)
	}
	if got, want := readRotationFile(t, path), "{\"after\":true}\n"; got != want {
		t.Fatalf("log after the blocker cleared = %q, want only the new line", got)
	}
	if got, want := readRotationFile(t, path+".1"), "{\"existing\":true}\n{\"next\":true}\n"; got != want {
		t.Fatalf("archive 1 = %q, want the rotated log", got)
	}
	if got, want := readRotationFile(t, path+".5"), "{\"archive\":4}\n"; got != want {
		t.Fatalf("archive 5 = %q, want what was archive 4", got)
	}
}
