//go:build windows

package sysacl

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// backupOperators stands in for the beacon-log group: a real local group no test account is in.
const backupOperators = "S-1-5-32-551"

// A shared log directory, restricted, then shared again: each step reads back exactly as that
// mode sets it, and a file outside the log family is left alone throughout.
func TestRestrictingAndSharingTheLogDirectory(t *testing.T) {
	requireElevated(t)
	dir := filepath.Join(t.TempDir(), "logs")
	if err := SecureLogDir(dir, "runtime.jsonl"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime.jsonl", "runtime.jsonl.lock", "runtime.jsonl.1", "operator-notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	notesBefore := mustRead(t, filepath.Join(dir, "operator-notes.txt"))

	if err := SecureRestrictedLogDir(dir, "runtime.jsonl", backupOperators); err != nil {
		t.Fatalf("SecureRestrictedLogDir: %v", err)
	}
	assertNoUserMayChange(t, dir)
	problems, err := InspectRestrictedLogDir(dir, "runtime.jsonl", backupOperators)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("the restricted directory reads back with problems: %v", problems)
	}
	if obj := mustRead(t, dir); !reflect.DeepEqual(obj.DACL, RestrictedLogDirDACL(backupOperators)) || !obj.Protected {
		t.Fatalf("directory DACL %+v (protected %v), want %+v", obj.DACL, obj.Protected, RestrictedLogDirDACL(backupOperators))
	}
	for _, name := range []string{"runtime.jsonl", "runtime.jsonl.1"} {
		obj := mustRead(t, filepath.Join(dir, name))
		if want := InheritedDACL(RestrictedLogDirDACL(backupOperators), false); !reflect.DeepEqual(obj.DACL, want) {
			t.Errorf("%s carries %+v, want exactly the inherited %+v", name, obj.DACL, want)
		}
		if CanAppend(obj.DACL, obj.NullDACL, false) {
			t.Errorf("hooks could still append to %s directly", name)
		}
	}
	if lock := mustRead(t, filepath.Join(dir, "runtime.jsonl.lock")); !lock.Protected || !reflect.DeepEqual(lock.DACL, RestrictedLockDACL()) {
		t.Errorf("the lock carries %+v (protected %v), want only SYSTEM and Administrators", lock.DACL, lock.Protected)
	}
	if notesAfter := mustRead(t, filepath.Join(dir, "operator-notes.txt")); !reflect.DeepEqual(notesBefore, notesAfter) {
		t.Errorf("a file outside the log family was changed: %+v -> %+v", notesBefore, notesAfter)
	}

	// The collector re-creates the log on rotation: the new file inherits the restricted entries.
	rotated := filepath.Join(dir, "runtime.jsonl.2")
	if err := os.WriteFile(rotated, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if problems := RestrictedProblems(mustRead(t, rotated), backupOperators, false); len(problems) > 0 {
		t.Errorf("a newly created log inherits a grant: %v", problems)
	}

	// Back to shared: the shared ACL in full, the lock's protected DACL gone with the rest.
	if err := SecureLogDir(dir, "runtime.jsonl"); err != nil {
		t.Fatal(err)
	}
	state, err := InspectLogDir(dir, "runtime.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Problems) > 0 || !state.HooksCanAppend {
		t.Fatalf("after sharing again: problems %v, hooks can append %v", state.Problems, state.HooksCanAppend)
	}
	if lock := mustRead(t, filepath.Join(dir, "runtime.jsonl.lock")); lock.Protected {
		t.Error("the lock kept the restricted mode's protected DACL")
	}
}

func TestInspectRestrictedLogDirReportsALeftoverGrant(t *testing.T) {
	requireElevated(t)
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "runtime.jsonl")
	if err := os.WriteFile(log, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := SecureRestrictedLogDir(dir, "runtime.jsonl", backupOperators); err != nil {
		t.Fatal(err)
	}
	icacls(t, log, "/grant", `*S-1-5-32-545:(R)`)
	problems, err := InspectRestrictedLogDir(dir, "runtime.jsonl", backupOperators)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("a read grant to Users on the restricted log went unreported")
	}
}

func TestSecureRestrictedLockRefusesALink(t *testing.T) {
	requireElevated(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "runtime.jsonl.lock")
	if err := os.Link(target, lock); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	before := mustRead(t, target)
	if err := SecureRestrictedLock(lock); err == nil {
		t.Fatal("secured a lock that is a hard link to another file")
	}
	if after := mustRead(t, target); !reflect.DeepEqual(before, after) {
		t.Fatal("the link's target was changed")
	}
}
