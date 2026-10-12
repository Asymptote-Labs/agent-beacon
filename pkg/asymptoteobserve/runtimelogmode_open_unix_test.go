//go:build unix

package asymptoteobserve

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A shared log path that someone replaced with a symlink must not lead a root writer to append to
// or chmod the link's target (a binary root later runs, say).
func TestOpenRuntimeLogFileAsRootRefusesSymlink(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
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
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	log := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := syscall.Mkfifo(log, 0o666); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := OpenRuntimeLogFile(log, log, os.O_RDWR|os.O_APPEND)
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
