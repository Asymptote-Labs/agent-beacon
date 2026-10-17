//go:build unix

package asymptoteobserve

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// asRoot makes OpenRuntimeLogFile take its root path for the rest of the test. The refusals it adds
// (O_NOFOLLOW, O_NONBLOCK, the regular-file and link-count checks) behave the same for any user,
// so the tests need not run as root to prove them.
func asRoot(t *testing.T) {
	t.Helper()
	prev := runningAsRoot
	runningAsRoot = func() bool { return true }
	t.Cleanup(func() { runningAsRoot = prev })
}

// A shared log path that someone replaced with a symlink must not lead a root writer to append to
// or chmod the link's target (a binary root later runs, say).
func TestOpenRuntimeLogFileAsRootRefusesSymlink(t *testing.T) {
	asRoot(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "beacon")
	writeMode(t, target, 0o755)
	log := filepath.Join(dir, "runtime.jsonl")
	if err := os.Symlink(target, log); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenRuntimeLogFile(log, log, os.O_CREATE|os.O_WRONLY|os.O_APPEND); err == nil {
		_ = f.Close()
		t.Fatal("root opened a symlinked runtime log")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %v, want unchanged 0755", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(target); string(data) != "{}\n" {
		t.Fatalf("target content = %q, want unchanged", data)
	}
}

func TestOpenRuntimeLogFileAsRootRefusesFIFOWithoutBlocking(t *testing.T) {
	asRoot(t)
	log := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := syscall.Mkfifo(log, 0o666); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() {
		// The writers' own flags: O_WRONLY on a FIFO with no reader blocks unless O_NONBLOCK is set.
		f, err := OpenRuntimeLogFile(log, log, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("root opened a FIFO as the runtime log")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO runtime log blocked")
	}
}

// A hard link to a root-owned file is a regular file, so it passes O_NOFOLLOW and the regular-file
// check; a root writer must still not append to it.
func TestOpenRuntimeLogFileAsRootRefusesAHardLink(t *testing.T) {
	asRoot(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts")
	writeMode(t, target, 0o644)
	log := filepath.Join(dir, "runtime.jsonl")
	if err := os.Link(target, log); err != nil {
		t.Skip(err)
	}
	if f, err := OpenRuntimeLogFile(log, log, os.O_CREATE|os.O_WRONLY|os.O_APPEND); err == nil {
		_ = f.Close()
		t.Fatal("root opened a hard-linked runtime log")
	}
	if data, _ := os.ReadFile(target); string(data) != "{}\n" {
		t.Fatalf("target content = %q, want unchanged", data)
	}
}

func TestSettleRuntimeLogFileModeLeavesSymlinkTargetAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeMode(t, target, 0o755)
	archive := filepath.Join(dir, "runtime.jsonl.1")
	if err := os.Symlink(target, archive); err != nil {
		t.Fatal(err)
	}
	_ = SettleRuntimeLogFileMode(archive, filepath.Join(dir, "runtime.jsonl"), false)
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %v, want unchanged 0755", info.Mode().Perm())
	}
}

func TestOpenRuntimeLogFileCreatesAndAppends(t *testing.T) {
	log := filepath.Join(t.TempDir(), "runtime.jsonl")
	for i := 0; i < 2; i++ {
		f, err := OpenRuntimeLogFile(log, log, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("{}\n"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if data, _ := os.ReadFile(log); string(data) != "{}\n{}\n" {
		t.Fatalf("log = %q", data)
	}
}
