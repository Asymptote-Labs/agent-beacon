//go:build unix

package writer

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func ownLogs(t *testing.T) {
	t.Helper()
	prev := logOwnerUID
	logOwnerUID = os.Geteuid()
	t.Cleanup(func() { logOwnerUID = prev })
}

func ownerOf(t *testing.T, path string) (uid, gid int, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	return int(st.Uid), int(st.Gid), info.Mode()
}

func systemLogFixture(t *testing.T) string {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "beacon-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime.jsonl")
	for _, name := range []string{"runtime.jsonl", "runtime.jsonl.lock", "runtime.jsonl.1", "runtime.jsonl.2", "unrelated.txt", "runtime.jsonl.bak"} {
		target := filepath.Join(dir, name)
		if err := os.WriteFile(target, []byte("x\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRestrictSystemLogNarrowsTheDirectoryAndEveryLogFile(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	ownLogs(t)
	path := systemLogFixture(t)
	gid := os.Getegid()
	if err := RestrictSystemLog(path, gid); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	uid, g, mode := ownerOf(t, dir)
	if uid != os.Geteuid() || g != gid || mode.Perm() != 0o750 || mode&os.ModeSetgid == 0 {
		t.Fatalf("dir = uid %d gid %d mode %v; want the readers group, 0750 and setgid", uid, g, mode)
	}
	for _, name := range []string{"runtime.jsonl", "runtime.jsonl.lock", "runtime.jsonl.1", "runtime.jsonl.2"} {
		want := asymptoteobserve.RestrictedRuntimeLogFileMode
		if name == "runtime.jsonl.lock" {
			want = asymptoteobserve.RestrictedRuntimeLogLockMode
		}
		uid, g, mode := ownerOf(t, filepath.Join(dir, name))
		if uid != os.Geteuid() || g != gid || mode.Perm() != want {
			t.Fatalf("%s = uid %d gid %d mode %o", name, uid, g, mode.Perm())
		}
		if data, _ := os.ReadFile(filepath.Join(dir, name)); string(data) != "x\n" {
			t.Fatalf("%s lost its content: %q", name, data)
		}
	}
	// No temporary copies are left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".restrict-") {
			t.Fatalf("left %s behind", e.Name())
		}
	}
	for _, name := range []string{"unrelated.txt", "runtime.jsonl.bak"} {
		if _, _, mode := ownerOf(t, filepath.Join(dir, name)); mode.Perm() != 0o666 {
			t.Fatalf("%s was changed to %o; only the log's own files may be", name, mode.Perm())
		}
	}
	data, err := os.ReadFile(asymptoteobserve.LogAccessMarkerPath(path))
	if err != nil || string(data) != "restricted\n" {
		t.Fatalf("marker = %q, %v", data, err)
	}
}

func TestRestrictSystemLogCreatesAMissingDirectory(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	ownLogs(t)
	testenv.SetHome(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "new", "runtime.jsonl")
	if err := RestrictSystemLog(path, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if _, _, mode := ownerOf(t, filepath.Dir(path)); mode.Perm() != 0o750 || mode&os.ModeSetgid == 0 {
		t.Fatalf("dir mode = %v", mode)
	}
}

func TestRestrictSystemLogLeavesLinksAlone(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	ownLogs(t)
	path := systemLogFixture(t)
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(outside, 0o666)
	if err := os.Symlink(outside, path+".3"); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(t.TempDir(), "hard-victim")
	if err := os.WriteFile(hard, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(hard, 0o666)
	if err := os.Remove(path + ".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hard, path+".2"); err != nil {
		t.Skipf("hard link across temp dirs: %v", err)
	}
	if err := RestrictSystemLog(path, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	for _, victim := range []string{outside, hard} {
		if _, _, mode := ownerOf(t, victim); mode.Perm() != 0o666 {
			t.Fatalf("%s was changed through a link to %o", victim, mode.Perm())
		}
	}
}

func TestRestrictSystemLogRefusesALinkedDirectory(t *testing.T) {
	ownLogs(t)
	testenv.SetHome(t, t.TempDir())
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "beacon-agent")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := RestrictSystemLog(filepath.Join(link, "runtime.jsonl"), os.Getegid()); err == nil {
		t.Fatal("restricted a log directory reached through a link")
	}
}

// Back to shared: as root, which is the only account that can trust the marker it removes.
func TestShareSystemLogUndoesTheRestrictedMode(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the restricted marker is trusted only when root owns it")
	}
	path := systemLogFixture(t)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatal(err)
	}
	if !asymptoteobserve.RuntimeLogAccessRestricted(path) {
		t.Fatal("fixture: not restricted")
	}
	if err := ShareSystemLog(path); err != nil {
		t.Fatal(err)
	}
	if asymptoteobserve.RuntimeLogAccessRestricted(path) {
		t.Fatal("still restricted")
	}
	dir := filepath.Dir(path)
	if _, _, mode := ownerOf(t, dir); mode.Perm() != 0o755 {
		t.Fatalf("dir mode = %v", mode)
	}
	for name, want := range map[string]os.FileMode{
		"runtime.jsonl": 0o666, "runtime.jsonl.lock": 0o666, "runtime.jsonl.1": 0o644, "runtime.jsonl.2": 0o644,
	} {
		if _, _, mode := ownerOf(t, filepath.Join(dir, name)); mode.Perm() != want {
			t.Fatalf("%s mode = %o, want %o", name, mode.Perm(), want)
		}
	}
}

func TestShareSystemLogOnANeverRestrictedLogChangesNoFiles(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	path := systemLogFixture(t)
	if err := os.Chmod(path+".1", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ShareSystemLog(path); err != nil {
		t.Fatal(err)
	}
	if _, _, mode := ownerOf(t, path+".1"); mode.Perm() != 0o600 {
		t.Fatalf("an archive of a log that was never restricted was changed to %o", mode.Perm())
	}
}

func TestInLogFamily(t *testing.T) {
	for name, want := range map[string]bool{
		"runtime.jsonl": true, "runtime.jsonl.lock": true, "runtime.jsonl.1": true, "runtime.jsonl.12": true,
		"runtime.jsonl.0": false, "runtime.jsonl.-1": false, "runtime.jsonl.bak": false, "runtime.json": false,
		".beacon-log-access": false, "other.jsonl.1": false,
	} {
		if got := inLogFamily("runtime.jsonl", name); got != want {
			t.Errorf("inLogFamily(%q) = %v, want %v", name, got, want)
		}
	}
}

// A descriptor someone opened while the log was 0666 must not keep working on the restricted log:
// not to rewrite it, not to read what is written after, not to hold its lock.
func TestRestrictSystemLogStrandsDescriptorsOpenedWhileShared(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	ownLogs(t)
	path := systemLogFixture(t)
	held, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	heldLock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer heldLock.Close()

	if err := RestrictSystemLog(path, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if _, err := held.WriteAt([]byte("FORGED\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := held.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "x\n" {
		t.Fatalf("a descriptor opened before the restriction changed the log: %q", data)
	}
	// The collector's next append is not visible through the old descriptor.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("secret\n")
	_ = f.Close()
	buf := make([]byte, 64)
	n, _ := held.ReadAt(buf, 0)
	if strings.Contains(string(buf[:n]), "secret") {
		t.Fatal("a descriptor opened before the restriction reads new events")
	}
	// The old lock is not the lock any more.
	oldInfo, _ := heldLock.Stat()
	newInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(oldInfo, newInfo) {
		t.Fatal("the lock opened while shared is still the log's lock")
	}
}

func TestRestrictSystemLogLeavesAnAlreadyRestrictedLogInPlace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the restricted marker is trusted only when root owns it")
	}
	path := systemLogFixture(t)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("a repair of a restricted log copied the log again")
	}
}

// A user who hard-linked the shared log could otherwise keep it out of the restriction: a file
// with another link is copied like any other, and the user's link keeps only its old content.
func TestRestrictSystemLogReplacesAHardLinkedLog(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	ownLogs(t)
	path := systemLogFixture(t)
	link := filepath.Join(filepath.Dir(path), "..", "users-link")
	if err := os.Link(path, link); err != nil {
		t.Skipf("hard link: %v", err)
	}
	if err := RestrictSystemLog(path, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if _, _, mode := ownerOf(t, path); mode.Perm() != asymptoteobserve.RestrictedRuntimeLogFileMode {
		t.Fatalf("log mode = %o", mode.Perm())
	}
	a, _ := os.Stat(path)
	b, _ := os.Stat(link)
	if os.SameFile(a, b) {
		t.Fatal("the restricted log is still the file the user linked")
	}
}

func TestRestrictSystemLogDoesNotHangOnAHeldLock(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	ownLogs(t)
	prev := restrictLockWait
	restrictLockWait = 100 * time.Millisecond
	t.Cleanup(func() { restrictLockWait = prev })
	path := systemLogFixture(t)
	f, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- RestrictSystemLog(path, os.Getegid()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RestrictSystemLog hung on a lock another process held")
	}
}
