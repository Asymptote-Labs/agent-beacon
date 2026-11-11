//go:build !windows

package sessionfs

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fixture is a fake console-user home holding one real transcript, plus a directory outside the
// home that stands in for another user's (or root's) store.
type fixture struct {
	home, outside string
	real          string
	secret        string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{home: t.TempDir(), outside: t.TempDir()}
	f.real = filepath.Join(f.home, ".claude", "projects", "repo", "mine.jsonl")
	mustWrite(t, f.real, "mine\n")
	f.secret = filepath.Join(f.outside, ".claude", "projects", "victim", "theirs.jsonl")
	mustWrite(t, f.secret, "someone else's transcript\n")
	return f
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func confine(t *testing.T, home string, uid int) *Confinement {
	t.Helper()
	c, err := Confine(home, uid, Options{})
	if err != nil {
		t.Fatalf("Confine: %v", err)
	}
	t.Cleanup(c.Release)
	return c
}

func requireRefusedAsAbsent(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was read through the confinement", what)
	}
	if !os.IsNotExist(err) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s: err = %v, want one that reads as absent", what, err)
	}
}

func TestUnconfinedReadsFollowSymlinksAsBefore(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(f.home, ".claude", "projects", "linked.jsonl")
	if err := os.Symlink(f.secret, link); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(link)
	if err != nil || !strings.Contains(string(data), "someone else's") {
		t.Fatalf("unconfined ReadFile = %q, %v; a user's own sync follows their own links", data, err)
	}
}

func TestConfinedReadsRegularFilesTheUserOwns(t *testing.T) {
	f := newFixture(t)
	c := confine(t, f.home, os.Getuid())

	data, err := ReadFile(f.real)
	if err != nil || string(data) != "mine\n" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	entries, err := ReadDir(filepath.Join(f.home, ".claude", "projects"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "repo" {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	var walked []string
	if err := WalkDir(filepath.Join(f.home, ".claude"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			walked = append(walked, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(walked) != 1 || walked[0] != f.real {
		t.Fatalf("walk saw %v, want the absolute path %s", walked, f.real)
	}
	// A dotfile manager's link that stays inside the home is still followed.
	inside := filepath.Join(f.home, ".claude", "projects", "alias.jsonl")
	if err := os.Symlink(filepath.Join("repo", "mine.jsonl"), inside); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(inside); err != nil || string(data) != "mine\n" {
		t.Fatalf("ReadFile through an in-home link = %q, %v", data, err)
	}
	if n, _ := c.Refused(); n != 0 {
		t.Fatalf("refused %d reads of the user's own files", n)
	}
}

func TestConfinedReadsRefuseLinksOutOfTheHome(t *testing.T) {
	f := newFixture(t)
	projects := filepath.Join(f.home, ".claude", "projects")
	fileLink := filepath.Join(projects, "repo", "planted.jsonl")
	if err := os.Symlink(f.secret, fileLink); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(projects, "victim")
	if err := os.Symlink(filepath.Dir(f.secret), dirLink); err != nil {
		t.Fatal(err)
	}
	relLink := filepath.Join(projects, "dotdot.jsonl")
	rel, err := filepath.Rel(projects, f.secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, relLink); err != nil {
		t.Fatal(err)
	}
	c := confine(t, f.home, os.Getuid())

	_, err = ReadFile(fileLink)
	requireRefusedAsAbsent(t, "a file linked out of the home", err)
	_, err = ReadFile(relLink)
	requireRefusedAsAbsent(t, "a relative link climbing out of the home", err)
	_, err = ReadDir(dirLink)
	requireRefusedAsAbsent(t, "a directory linked out of the home", err)
	_, err = ReadFile(filepath.Join(dirLink, "theirs.jsonl"))
	requireRefusedAsAbsent(t, "a file under a directory linked out of the home", err)
	_, err = Stat(filepath.Join(dirLink, "theirs.jsonl"))
	requireRefusedAsAbsent(t, "Stat under a directory linked out of the home", err)
	_, err = ReadFile(f.secret)
	requireRefusedAsAbsent(t, "a path outside the home", err)
	// Listings leave the planted links out, so a collector never sees them as sessions.
	if entries, err := ReadDir(filepath.Join(projects, "repo")); err != nil || len(entries) != 1 || entries[0].Name() != "mine.jsonl" {
		t.Fatalf("ReadDir listed %v, %v; want only the real transcript", entries, err)
	}

	// The walk never descends into the linked directory, and a walk rooted at it is refused.
	var walked []string
	_ = WalkDir(projects, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
			walked = append(walked, path)
		}
		return nil
	})
	if len(walked) != 1 || walked[0] != f.real {
		t.Fatalf("walk read %v", walked)
	}
	var rootErr error
	_ = WalkDir(dirLink, func(_ string, _ fs.DirEntry, err error) error {
		if err != nil && rootErr == nil {
			rootErr = err
		}
		return nil
	})
	requireRefusedAsAbsent(t, "a walk rooted at a directory linked out of the home", rootErr)

	// A whole store linked away, the shape of ~/.claude -> /root/.claude.
	storeLink := filepath.Join(f.home, ".codex")
	if err := os.Symlink(filepath.Join(f.outside, ".claude"), storeLink); err != nil {
		t.Fatal(err)
	}
	_, err = Stat(storeLink)
	requireRefusedAsAbsent(t, "a store directory linked out of the home", err)

	n, examples := c.Refused()
	if n < 7 || len(examples) == 0 {
		t.Fatalf("refusals = %d %v, want each one counted", n, examples)
	}
}

func TestConfinedReadsRefuseSpecialFilesWithoutBlocking(t *testing.T) {
	f := newFixture(t)
	fifo := filepath.Join(f.home, ".claude", "projects", "repo", "fifo.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	confine(t, f.home, os.Getuid())

	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		requireRefusedAsAbsent(t, "a FIFO", err)
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked; a planted FIFO would hang the install")
	}

	if _, err := os.Stat("/dev/zero"); err == nil {
		dev := filepath.Join(f.home, ".claude", "projects", "repo", "zero.jsonl")
		if err := os.Symlink("/dev/zero", dev); err != nil {
			t.Fatal(err)
		}
		_, err := ReadFile(dev)
		requireRefusedAsAbsent(t, "a link to /dev/zero", err)
	}
}

func TestConfinedReadsRefuseFilesAnotherUserOwns(t *testing.T) {
	f := newFixture(t)
	// Ownership cannot be changed without root, so the console user's uid is the one injected:
	// to a confinement for uid+1, every file this test wrote belongs to someone else.
	c := confine(t, f.home, os.Getuid()+1)
	_, err := ReadFile(f.real)
	requireRefusedAsAbsent(t, "a file another uid owns", err)
	// Directories are listed whoever owns them, so a root-owned dotdir does not hide the user's
	// files; the file itself is left out of the listing.
	if entries, err := ReadDir(filepath.Dir(filepath.Dir(f.real))); err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir of the projects directory = %v, %v", entries, err)
	}
	if entries, err := ReadDir(filepath.Dir(f.real)); err != nil || len(entries) != 0 {
		t.Fatalf("ReadDir listed a file another uid owns: %v, %v", entries, err)
	}
	if n, examples := c.Refused(); n != 2 || !strings.Contains(examples[0], "not the console user") {
		t.Fatalf("refusals = %d %v", n, examples)
	}
}

func TestConfinedSQLiteGoesThroughASnapshot(t *testing.T) {
	f := newFixture(t)
	db := filepath.Join(f.home, ".local", "share", "opencode", "opencode.db")
	mustWrite(t, db, "SQLite format 3\x00 main")
	mustWrite(t, db+"-wal", "wal")
	linked := filepath.Join(f.home, ".hermes", "state.db")
	if err := os.MkdirAll(filepath.Dir(linked), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.secret, linked); err != nil {
		t.Fatal(err)
	}

	if got, err := SQLitePath(db); err != nil || got != db {
		t.Fatalf("unconfined SQLitePath = %q, %v; want the path itself", got, err)
	}

	c := confine(t, f.home, os.Getuid())
	snap, err := SQLitePath(db)
	if err != nil {
		t.Fatal(err)
	}
	if snap == db || strings.HasPrefix(snap, f.home) {
		t.Fatalf("snapshot %s is the user's own path", snap)
	}
	for suffix, want := range map[string]string{"": "SQLite format 3\x00 main", "-wal": "wal"} {
		got, err := os.ReadFile(snap + suffix)
		if err != nil || !bytes.Equal(got, []byte(want)) {
			t.Fatalf("snapshot%s = %q, %v", suffix, got, err)
		}
	}
	if again, _ := SQLitePath(db); again != snap {
		t.Fatalf("a second open copied the database again: %s vs %s", again, snap)
	}
	_, err = SQLitePath(linked)
	requireRefusedAsAbsent(t, "a database linked out of the home", err)

	DropSnapshots()
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot survived DropSnapshots: %v", err)
	}
	c.Release()
	if _, err := Confine(f.home, os.Getuid(), Options{}); err != nil {
		t.Fatalf("Release left the confinement active: %v", err)
	} else {
		active.Load().Release()
	}
}

func TestOnlyOneConfinementAtATime(t *testing.T) {
	f := newFixture(t)
	confine(t, f.home, os.Getuid())
	if _, err := Confine(t.TempDir(), os.Getuid(), Options{}); err == nil {
		t.Fatal("a second confinement replaced the first")
	}
}

func TestOwnerUIDNamesTheHomesOwner(t *testing.T) {
	uid, err := OwnerUID(t.TempDir())
	if err != nil || uid != os.Getuid() {
		t.Fatalf("OwnerUID = %d, %v; want %d", uid, err, os.Getuid())
	}
}

// requireDeclined asserts an OpenOutside error is a decline: ErrRefused, not an absent file.
func requireDeclined(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was read through the confinement", what)
	}
	if !errors.Is(err, ErrRefused) || errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
		t.Fatalf("%s: err = %v, want ErrRefused that does not read as absent", what, err)
	}
}

// reachableFixture is a home and a directory outside it under a directory anyone may enter, with
// a 0600 file outside. A confinement for os.Getuid()+1 stands in for another console user, and
// t.TempDir() sits under a per-user temporary directory that user could not enter at all.
func reachableFixture(t *testing.T) (home, outside, secret string) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "sessionfs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home, outside = filepath.Join(base, "home"), filepath.Join(base, "outside")
	for _, dir := range []string{base, home, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret = filepath.Join(outside, "theirs")
	if err := os.WriteFile(secret, []byte("someone else's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, outside, secret
}

func TestOpenOutsideReadsTheUsersOwnFiles(t *testing.T) {
	f := newFixture(t)
	// A link placed in the home to the user's own file outside it is theirs to make.
	link := filepath.Join(f.home, ".npmrc")
	if err := os.Symlink(f.secret, link); err != nil {
		t.Fatal(err)
	}
	confine(t, f.home, os.Getuid())
	for _, path := range []string{f.secret, link, f.real} {
		file, err := OpenOutside(path)
		if err != nil {
			t.Fatalf("OpenOutside(%s), a file the console user owns: %v", path, err)
		}
		_ = file.Close()
	}
}

func TestOpenOutsideDeclinesWhatTheUserCannotRead(t *testing.T) {
	home, outside, secret := reachableFixture(t)
	fifo := filepath.Join(outside, "npmrc")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	link := filepath.Join(home, ".npmrc")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	// Ownership cannot be changed without root, so the console user's uid is the one injected:
	// to a confinement for uid+1, the 0600 files this test wrote are someone else's.
	c := confine(t, home, os.Getuid()+1)

	done := make(chan error, 1)
	go func() {
		_, err := OpenOutside(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		requireDeclined(t, "a FIFO", err)
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked; a planted one would hang the inventory job")
	}
	_, err := OpenOutside(secret)
	requireDeclined(t, "a file the console user could not read", err)
	_, err = OpenOutside(link)
	requireDeclined(t, "a link to a file the console user could not read", err)
	_, err = OpenOutside(home)
	requireDeclined(t, "a directory", err)
	_, err = OpenOutside("npmrc")
	requireDeclined(t, "a relative path", err)
	if n, _ := c.Refused(); n != 5 {
		t.Fatalf("refusals = %d, want 5", n)
	}
	// An absent file is absent, not a decline nor a refusal.
	_, err = OpenOutside(filepath.Join(outside, "missing"))
	requireRefusedAsAbsent(t, "a missing file", err)
	if errors.Is(err, ErrRefused) {
		t.Fatalf("a missing file was declined: %v", err)
	}
	if n, _ := c.Refused(); n != 5 {
		t.Fatalf("a missing file was counted as refused")
	}
}

// What the console user cannot reach must look the same whether it is there or not, or the
// heartbeat would tell them what a directory only root may enter holds.
func TestOpenOutsideHidesWhatTheUserCannotReach(t *testing.T) {
	home, outside, _ := reachableFixture(t)
	private := filepath.Join(outside, "private")
	mustWrite(t, filepath.Join(private, "key"), "secret\n")
	mustWrite(t, filepath.Join(private, "world-readable"), "x\n")
	if err := os.Chmod(filepath.Join(private, "world-readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(private, "fifo"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	link := filepath.Join(home, ".npmrc")
	if err := os.Symlink(filepath.Join(private, "key"), link); err != nil {
		t.Fatal(err)
	}
	// The 0700 directory is this test's, so a confinement for uid+1 cannot enter it.
	confine(t, home, os.Getuid()+1)
	for _, path := range []string{filepath.Join(private, "key"), link, filepath.Join(private, "fifo"), filepath.Join(private, "world-readable"), filepath.Join(private, "missing")} {
		_, err := OpenOutside(path)
		requireRefusedAsAbsent(t, path, err)
		if errors.Is(err, ErrRefused) {
			t.Fatalf("%s was declined, which tells the console user it is there: %v", path, err)
		}
	}
}

func TestOpenOutsideReadsAnUnreachablePathAsAbsent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can enter any directory")
	}
	f := newFixture(t)
	locked := filepath.Join(f.outside, "locked")
	mustWrite(t, filepath.Join(locked, "npmrc"), "x\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	confine(t, f.home, os.Getuid())
	_, err := OpenOutside(filepath.Join(locked, "npmrc"))
	requireRefusedAsAbsent(t, "a file under a directory that cannot be entered", err)
	if errors.Is(err, ErrRefused) {
		t.Fatalf("an unreachable path was declined: %v", err)
	}
}

func TestOpenOutsideReadsWhatAnyoneMayRead(t *testing.T) {
	const system = "/etc/hosts"
	info, err := os.Stat(system)
	if err != nil || info.Mode().Perm()&0o004 == 0 {
		t.Skipf("%s is not a world-readable file here", system)
	}
	home, _, _ := reachableFixture(t)
	// A home-manager .npmrc is a link from the home into the world-readable Nix store.
	link := filepath.Join(home, ".npmrc")
	if err := os.Symlink(system, link); err != nil {
		t.Fatal(err)
	}
	confine(t, home, os.Getuid()+1)
	for _, path := range []string{system, link} {
		file, err := OpenOutside(path)
		if err != nil {
			t.Fatalf("OpenOutside(%s), a file anyone may read: %v", path, err)
		}
		_ = file.Close()
	}
}

func TestConfinementCoversTheHomesRealPath(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(f.home, link); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(f.real)
	if err != nil {
		t.Fatal(err)
	}
	confine(t, link, os.Getuid())
	if !Confined(filepath.Join(link, ".claude")) || !Confined(real) {
		t.Fatalf("Confined(%s) = %v, Confined(%s) = %v", link, Confined(link), real, Confined(real))
	}
	data, err := ReadFile(real)
	if err != nil || string(data) != "mine\n" {
		t.Fatalf("ReadFile of the home's real path = %q, %v", data, err)
	}
}

// Opening a device can act on it (opening /dev/watchdog arms the timer), so a device the user
// names must be turned down before it is opened, not after.
func TestOpenOutsideRefusesDevicesBeforeOpeningThem(t *testing.T) {
	const device = "/dev/null"
	if info, err := os.Stat(device); err != nil || info.Mode()&fs.ModeCharDevice == 0 {
		t.Skipf("%s is not a character device here", device)
	}
	_, _, err := openRegular(device, os.Getuid())
	if !errors.Is(err, errNotRegular) {
		t.Fatalf("openRegular(%s) = %v, want it refused as not a regular file before opening", device, err)
	}
	f := newFixture(t)
	link := filepath.Join(f.home, ".npmrc")
	if err := os.Symlink(device, link); err != nil {
		t.Fatal(err)
	}
	confine(t, f.home, os.Getuid())
	for _, path := range []string{device, link} {
		_, err := OpenOutside(path)
		requireDeclined(t, "a device", err)
	}
}

// openRegular walks the resolved path itself, so a directory that turns into a symlink after
// the path was resolved cannot redirect it.
func TestOpenRegularFollowsNoSymlink(t *testing.T) {
	f := newFixture(t)
	real, err := filepath.EvalSymlinks(f.secret)
	if err != nil {
		t.Fatal(err)
	}
	file, r, err := openRegular(real, os.Getuid())
	if err != nil {
		t.Fatalf("openRegular of a resolved regular file: %v", err)
	}
	_ = file.Close()
	if r.others || !r.user {
		t.Fatalf("openRegular(%s) reach = %+v, want the owner alone in its 0700 directories", real, r)
	}
	if _, r, _ := openRegular(real, os.Getuid()+1); r.user {
		t.Fatalf("openRegular(%s) lets another user reach a file in 0700 directories they do not own", real)
	}
	dirLink := filepath.Join(filepath.Dir(filepath.Dir(real)), "swapped")
	if err := os.Symlink(filepath.Dir(real), dirLink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRegular(filepath.Join(dirLink, filepath.Base(real)), os.Getuid()); err == nil {
		t.Fatal("openRegular followed a directory symlink")
	}
	if _, _, err := openRegular(filepath.Join(filepath.Dir(real), "missing"), os.Getuid()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("openRegular of a missing file = %v, want not-exist", err)
	}
}

// openRegular reports whether every directory on the path may be entered by others.
func TestOpenRegularReportsWhetherOthersCanEnter(t *testing.T) {
	system, err := filepath.EvalSymlinks("/etc/hosts")
	if err != nil {
		t.Skipf("/etc/hosts: %v", err)
	}
	if info, err := os.Stat(system); err != nil || info.Mode().Perm()&0o004 == 0 {
		t.Skipf("%s is not a world-readable file here", system)
	}
	file, r, err := openRegular(system, os.Getuid()+1)
	if err != nil {
		t.Fatalf("openRegular(%s): %v", system, err)
	}
	_ = file.Close()
	if !r.others || !r.user {
		t.Fatalf("openRegular(%s) reach = %+v, want anyone", system, r)
	}
}
