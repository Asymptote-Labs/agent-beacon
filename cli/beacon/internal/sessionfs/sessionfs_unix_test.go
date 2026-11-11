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

// Inside the home, OpenOutside reads what Open would and declines what Open refuses, a link out of
// the home included, wherever it leads (a home-manager .npmrc into the Nix store).
func TestOpenOutsideInsideTheHomeIsTheConfinement(t *testing.T) {
	f := newFixture(t)
	out := filepath.Join(f.home, ".npmrc")
	if err := os.Symlink(f.secret, out); err != nil {
		t.Fatal(err)
	}
	nix := filepath.Join(f.home, ".yarnrc.yml")
	if err := os.Symlink("/etc/hosts", nix); err != nil {
		t.Fatal(err)
	}
	confine(t, f.home, os.Getuid())
	file, err := OpenOutside(f.real)
	if err != nil {
		t.Fatalf("OpenOutside of the console user's own file in the home: %v", err)
	}
	_ = file.Close()
	for _, path := range []string{out, nix} {
		_, err := OpenOutside(path)
		requireDeclined(t, "a link out of the home", err)
	}
	_, err = OpenOutside(filepath.Join(f.home, "missing"))
	requireRefusedAsAbsent(t, "a missing file in the home", err)
}

// Outside the home, only what every local user could read is read, and nothing is looked at past
// what they could all see: the console user's own private file is declined, and so is anything in
// or through a directory others may not enter, whether or not it is there.
func TestOpenOutsideReadsOnlyWhatEveryoneCould(t *testing.T) {
	home, outside, secret := reachableFixture(t)
	public := filepath.Join(outside, "public")
	if err := os.WriteFile(public, []byte("registry=https://registry.npmjs.org/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(outside, "private")
	mustWrite(t, filepath.Join(private, "key"), "x\n")
	if err := os.WriteFile(filepath.Join(private, "readable"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outside, "link")
	if err := os.Symlink(public, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(outside, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// The console user owns all of it; outside the home that buys nothing.
	c := confine(t, home, os.Getuid())
	file, err := OpenOutside(public)
	if err != nil {
		t.Fatalf("OpenOutside of a file anyone may read: %v", err)
	}
	_ = file.Close()

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
	for what, path := range map[string]string{
		"a 0600 file":                               secret,
		"a file in a 0700 directory":                filepath.Join(private, "key"),
		"a world-readable file in a 0700 directory": filepath.Join(private, "readable"),
		"a missing file in a 0700 directory":        filepath.Join(private, "missing"),
		"a user's symlink":                          link,
		"a directory":                               outside,
		"a relative path":                           "npmrc",
	} {
		_, err := OpenOutside(path)
		requireDeclined(t, what, err)
	}
	// A ".." in a named path is resolved by its name alone, so whether the directory it backs out
	// of is there, or may be entered, never shows.
	for _, path := range []string{private + "/../public", outside + "/missing/../public"} {
		file, err := OpenOutside(path)
		if err != nil {
			t.Fatalf("OpenOutside(%s): %v", path, err)
		}
		_ = file.Close()
	}
	before, _ := c.Refused()
	_, err = OpenOutside(filepath.Join(outside, "missing"))
	requireRefusedAsAbsent(t, "a missing file in a directory anyone may enter", err)
	if after, _ := c.Refused(); after != before {
		t.Fatalf("a missing file was counted as refused")
	}
}

// Root's own links at the top of the filesystem (macOS's /etc, /tmp and /var) are the only links
// followed: no user can plant or change one.
func TestOpenOutsideFollowsRootsTopLevelLinks(t *testing.T) {
	const system = "/etc/hosts"
	info, err := os.Lstat(system)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o004 == 0 {
		t.Skipf("%s is not a world-readable regular file here", system)
	}
	home, _, _ := reachableFixture(t)
	confine(t, home, os.Getuid())
	file, err := OpenOutside(system)
	if err != nil {
		t.Fatalf("OpenOutside(%s): %v", system, err)
	}
	_ = file.Close()
	if got, err := ResolveFileOutside(system); err != nil || got != mustReal(t, system) {
		t.Fatalf("ResolveFileOutside(%s) = %q, %v; want %q", system, got, err, mustReal(t, system))
	}
}

func TestResolveFileOutside(t *testing.T) {
	home, outside, _ := reachableFixture(t)
	public := filepath.Join(outside, "node")
	if err := os.WriteFile(public, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outside, "link")
	if err := os.Symlink(public, link); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home, "bin", "node")
	mustWrite(t, own, "x")
	// Unconfined it is filepath.EvalSymlinks.
	if got, err := ResolveFileOutside(link); err != nil || got != mustReal(t, public) {
		t.Fatalf("unconfined ResolveFileOutside(link) = %q, %v", got, err)
	}
	confine(t, home, os.Getuid())
	for path, want := range map[string]string{public: mustReal(t, public), own: own} {
		if got, err := ResolveFileOutside(path); err != nil || got != want {
			t.Errorf("ResolveFileOutside(%s) = %q, %v; want %q", path, got, err, want)
		}
	}
	_, err := ResolveFileOutside(link)
	requireDeclined(t, "a user's symlink", err)
	_, err = ResolveFileOutside(outside)
	requireDeclined(t, "a directory", err)
	_, err = ResolveFileOutside(filepath.Join(outside, "missing"))
	requireRefusedAsAbsent(t, "a missing file", err)
}

func mustReal(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
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
	if _, _, _, _, err := walkPublic(device); !errors.Is(err, errNotRegular) {
		t.Fatalf("walkPublic(%s) = %v, want it turned down as not a regular file, before any open", device, err)
	}
	home, _, _ := reachableFixture(t)
	confine(t, home, os.Getuid())
	_, err := OpenOutside(device)
	requireDeclined(t, "a device", err)
}
