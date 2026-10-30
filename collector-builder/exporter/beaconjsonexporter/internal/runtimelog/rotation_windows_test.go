//go:build windows

package runtimelog

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// holdWithoutDeleteSharing opens path the way PowerShell's Get-Content and [IO.File]::OpenRead do,
// sharing reads and writes but not deletes, which makes Windows refuse to rename it.
func holdWithoutDeleteSharing(t *testing.T, path string) syscall.Handle {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The failure behind this fix, for real: a reader holds the live log without delete sharing, so its
// rename is refused. The line must still land and no archive may move; once the reader lets go the
// next write rotates.
func TestAppendKeepsTheLineWhileAReaderHoldsTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(path, []byte("{\"existing\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("{\"archive\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := holdWithoutDeleteSharing(t, path)
	held := true
	defer func() {
		if held {
			_ = syscall.CloseHandle(h)
		}
	}()
	if err := appendForRotationTest(path, []byte("{\"next\":true}\n")); err != nil {
		t.Fatalf("append while a reader holds the log: %v", err)
	}
	if got, want := readRotationFile(t, path), "{\"existing\":true}\n{\"next\":true}\n"; got != want {
		t.Fatalf("log = %q, want the new line appended past the limit", got)
	}
	if got, want := readRotationFile(t, path+".1"), "{\"archive\":1}\n"; got != want {
		t.Fatalf("archive 1 = %q, want it untouched", got)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("an archive moved although the rotation could not finish: %v", err)
	}

	held = false
	if err := syscall.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	if err := appendForRotationTest(path, []byte("{\"after\":true}\n")); err != nil {
		t.Fatal(err)
	}
	if got, want := readRotationFile(t, path+".1"), "{\"existing\":true}\n{\"next\":true}\n"; got != want {
		t.Fatalf("archive 1 after the reader let go = %q, want the rotated log", got)
	}
	if got, want := readRotationFile(t, path+".2"), "{\"archive\":1}\n"; got != want {
		t.Fatalf("archive 2 = %q, want what was archive 1", got)
	}
	if got, want := readRotationFile(t, path), "{\"after\":true}\n"; got != want {
		t.Fatalf("log = %q, want only the new line", got)
	}
}
