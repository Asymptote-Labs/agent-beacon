//go:build unix

package asymptoteobserve

import (
	"os"
	"path/filepath"
	"testing"
)

func withPOSIXACL(t *testing.T, has bool) {
	t.Helper()
	old := hasPOSIXACL
	t.Cleanup(func() { hasPOSIXACL = old })
	hasPOSIXACL = func(string) bool { return has }
}

func writeMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// A private log another account reads through a POSIX ACL keeps that grant: an existing file loses
// only its "other" bits, never the group bits that are the ACL's mask, and a file created where a
// default ACL applies keeps a read mask.
func TestSettlePrivateLogKeepsAnACLGrant(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	log := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	withPOSIXACL(t, true)
	writeMode(t, log, 0o666)
	if err := SettleRuntimeLogFileMode(log, log, false); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, log); got != 0o660 {
		t.Fatalf("existing log with an ACL = %o, want 0660 (other bits removed, mask kept)", got)
	}

	created := filepath.Join(filepath.Dir(log), "runtime.jsonl.lock")
	writeMode(t, created, 0o600)
	if err := SettleRuntimeLogFileMode(created, log, true); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, created); got != PrivateACLRuntimeLogFileMode {
		t.Fatalf("new file under a default ACL = %o, want %o", got, PrivateACLRuntimeLogFileMode)
	}

	withPOSIXACL(t, false)
	writeMode(t, log, 0o666)
	if err := SettleRuntimeLogFileMode(log, log, false); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, log); got != PrivateRuntimeLogFileMode {
		t.Fatalf("existing log without an ACL = %o, want %o", got, PrivateRuntimeLogFileMode)
	}
}

// A writer never widens or narrows a file it does not own. Root running in a user's home (sudo
// keeping HOME) decides "shared" for that user's log and must leave their private log as it is.
func TestSettleLeavesAFileItDoesNotOwn(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to own a home and a log as another user")
	}
	const otherUID, otherGID = 54321, 54321
	home := t.TempDir()
	if err := os.Chown(home, otherUID, otherGID); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)
	log := filepath.Join(home, "runtime.jsonl")
	writeMode(t, log, 0o600)
	if err := os.Chown(log, otherUID, otherGID); err != nil {
		t.Fatal(err)
	}
	if RuntimeLogIsPrivate(log) {
		t.Fatal("root in a home it does not own must decide shared")
	}
	if err := SettleRuntimeLogFileMode(log, log, false); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, log); got != 0o600 {
		t.Fatalf("root settled the user's private log to %o, want it left at 0600", got)
	}
}

// Settling archives never follows a link swapped in after the archive was checked: an account that
// can write the log's directory (a custom log path, or root keeping a user's HOME) replaces the
// archive with a symlink to a file of its choosing between the check and the chmod, and that
// file must keep its mode. A chmod by path would follow the link and widen it.
func TestSettleArchivesNeverFollowsALinkSwappedIn(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "runtime.jsonl")
	writeMode(t, log, 0o666)
	archive := log + ".1"
	writeMode(t, archive, 0o600)
	victim := filepath.Join(t.TempDir(), "victim")
	writeMode(t, victim, 0o600)
	if RuntimeLogIsPrivate(log) {
		t.Fatal("a log outside the home must be shared")
	}
	t.Cleanup(func() { archiveChecked = nil })
	archiveChecked = func(path string) {
		link := filepath.Join(dir, "swap")
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(link, path); err != nil {
			t.Fatal(err)
		}
	}

	SettleRuntimeLogArchives(log, 1)

	if got := modeOf(t, victim); got != 0o600 {
		t.Fatalf("settling archives chmodded a symlink's target to %o, want it left at 0600", got)
	}
}

// A hard link to another file planted as an archive, whose planted name is removed once root has it
// open, leaves the descriptor on that file with a single link. Settling must not widen it.
func TestSettleArchivesNeverWidensAFileWhoseHardLinkWasRemovedAfterTheOpen(t *testing.T) {
	asRoot(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "runtime.jsonl")
	writeMode(t, log, 0o666)
	archive := log + ".1"
	victim := filepath.Join(dir, "victim")
	writeMode(t, victim, 0o600)
	if err := os.Link(victim, archive); err != nil {
		t.Fatal(err)
	}
	if RuntimeLogIsPrivate(log) {
		t.Fatal("a log outside the home must be shared")
	}
	t.Cleanup(func() { archiveOpened = nil })
	archiveOpened = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	SettleRuntimeLogArchives(log, 1)

	if got := modeOf(t, victim); got != 0o600 {
		t.Fatalf("settling archives chmodded a hard link's other file to %o, want it left at 0600", got)
	}
}
