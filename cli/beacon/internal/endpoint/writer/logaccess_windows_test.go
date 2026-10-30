//go:build windows

package writer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// backupOperators stands in for the beacon-log group, which the test does not create.
const backupOperators = "S-1-5-32-551"

// restrictFixture is a shared system log with a lock and an archive, outside the test's profile so
// it reads as a system log, and the readers group pointed at backupOperators.
func restrictFixture(t *testing.T) string {
	t.Helper()
	requireSystemACLRights(t)
	testenv.SetHome(t, t.TempDir())
	prev := logReadersSID
	logReadersSID = func() (string, error) { return backupOperators, nil }
	t.Cleanup(func() { logReadersSID = prev })
	path := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	if err := EnsureSystemLogWritable(path); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"runtime.jsonl": "{\"n\":1}\n", "runtime.jsonl.1": "{\"n\":0}\n"} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRestrictSystemLogOnWindows(t *testing.T) {
	path := restrictFixture(t)
	dir := filepath.Dir(path)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatalf("RestrictSystemLog: %v", err)
	}
	if !asymptoteobserve.RuntimeLogAccessRestricted(path) {
		t.Fatal("the marker is missing or not trusted")
	}
	problems, err := sysacl.InspectRestrictedLogDir(dir, "runtime.jsonl", backupOperators)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("the restricted log reads back with problems: %v", problems)
	}
	for name, want := range map[string]string{"runtime.jsonl": "{\"n\":1}\n", "runtime.jsonl.1": "{\"n\":0}\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; the copy lost content", name, got, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".restrict-") {
			t.Fatalf("a temporary copy was left behind: %s", entry.Name())
		}
	}

	// Already restricted: the files are not copied again.
	before := fileIndex(t, path)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatal(err)
	}
	if fileIndex(t, path) != before {
		t.Fatal("a log that was already restricted was replaced again")
	}

	if err := ShareSystemLog(path); err != nil {
		t.Fatalf("ShareSystemLog: %v", err)
	}
	if asymptoteobserve.RuntimeLogAccessRestricted(path) {
		t.Fatal("still marked restricted after sharing")
	}
	state, err := CheckSystemLogACL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Problems) > 0 || !state.HooksCanAppend {
		t.Fatalf("after sharing: problems %v, hooks can append %v", state.Problems, state.HooksCanAppend)
	}
}

// A handle opened while the log was shared keeps the access it was opened with. Restricting must
// leave it on a file nothing reads: what it writes afterwards does not reach the log.
func TestAHandleOpenedWhileSharedDoesNotReachTheRestrictedLog(t *testing.T) {
	path := restrictFixture(t)
	p, _ := windows.UTF16PtrFromString(path)
	// Shared for delete, as a process that wants to keep its handle across a rename would open it.
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := os.NewFile(uintptr(h), path)
	defer held.Close()

	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatalf("RestrictSystemLog: %v", err)
	}
	// Whether the orphaned file still takes the write depends on the file system's rename
	// semantics; either way it must not land in the log.
	if _, err := held.WriteAt([]byte("{\"forged\":true}\n"), 0); err != nil {
		t.Logf("the old handle no longer writes: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "forged") {
		t.Fatalf("a write through a handle opened while shared reached the restricted log: %q", got)
	}
}

// A file another process holds open without sharing delete cannot be replaced, and the restriction
// says so instead of reporting a log it could not close off.
func TestRestrictSystemLogFailsWhileAHandleBlocksTheReplacement(t *testing.T) {
	path := restrictFixture(t)
	prev := replaceWait
	replaceWait = 300 * time.Millisecond
	t.Cleanup(func() { replaceWait = prev })
	archive := path + ".1"
	held, err := os.Open(archive) // Go opens without FILE_SHARE_DELETE
	if err != nil {
		t.Fatal(err)
	}
	err = RestrictSystemLog(path, 0)
	_ = held.Close()
	if err == nil || !strings.Contains(err.Error(), "holds it open") {
		t.Fatalf("RestrictSystemLog = %v, want it to name the held file", err)
	}
	// The marker is written only once every file was replaced, so a failed run is not taken for a
	// finished one: the next run copies again, and once nothing holds the file it succeeds.
	if asymptoteobserve.RuntimeLogAccessRestricted(path) {
		t.Fatal("a restriction that failed left the marker, so a rerun would skip the copies")
	}
	// And the directory is shared again, so hooks can still append while the cause is fixed.
	dirObj, err := sysacl.ReadObject(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(sysacl.RestrictedProblems(dirObj, backupOperators, false)) == 0 {
		t.Fatal("a restriction that failed left the directory restricted, with no marker to say so")
	}
	before := fileIndex(t, archive)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatalf("the rerun: %v", err)
	}
	if fileIndex(t, archive) == before {
		t.Fatal("the rerun did not replace the archive the first run could not")
	}
	if !asymptoteobserve.RuntimeLogAccessRestricted(path) {
		t.Fatal("no marker after the rerun succeeded")
	}
}

// The other files the shared ACL reached are replaced too, so a handle opened on one while the log
// was shared reads nothing the collector writes afterwards; one with another hard link is left
// alone, since its copy would hand whatever the other link names to the readers group.
func TestRestrictSystemLogReplacesTheOtherFilesBesideTheLog(t *testing.T) {
	path := restrictFixture(t)
	dir := filepath.Dir(path)
	inventory := filepath.Join(dir, "inventory_state.jsonl")
	if err := os.WriteFile(inventory, []byte("{\"snapshot\":1}\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("not the log's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "linked.txt")
	if err := os.Link(outside, linked); err != nil {
		t.Fatal(err)
	}
	inventoryBefore, linkedBefore := fileIndex(t, inventory), fileIndex(t, linked)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatalf("RestrictSystemLog: %v", err)
	}
	if fileIndex(t, inventory) == inventoryBefore {
		t.Fatal("a file beside the log that the shared ACL reached was not replaced")
	}
	if got, _ := os.ReadFile(inventory); string(got) != "{\"snapshot\":1}\n" {
		t.Fatalf("the copy lost content: %q", got)
	}
	if fileIndex(t, linked) != linkedBefore {
		t.Fatal("a hard-linked file outside the log family was copied")
	}

	// Back to shared, the files beside the log get the shared grants back with the log, not only
	// the log family.
	if err := ShareSystemLog(path); err != nil {
		t.Fatalf("ShareSystemLog: %v", err)
	}
	obj, err := sysacl.ReadObject(inventory)
	if err != nil {
		t.Fatal(err)
	}
	interactive := false
	for _, ace := range obj.DACL {
		if ace.SID == "S-1-5-4" {
			interactive = true
		}
	}
	if !interactive {
		t.Fatalf("inventory_state.jsonl kept the restricted ACL after the log was shared again: %+v", obj.DACL)
	}
}

// Every file the restriction creates is owned by Administrators from the start, and the new lock
// carries only SYSTEM and Administrators: a reader who could open it for a moment could hold it.
func TestRestrictedCopiesHaveTheirFinalSecurityFromCreation(t *testing.T) {
	path := restrictFixture(t)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".lock", asymptoteobserve.LogAccessMarkerPath(path)} {
		obj, err := sysacl.ReadObject(name)
		if err != nil {
			t.Fatal(err)
		}
		if obj.Owner != "S-1-5-32-544" {
			t.Errorf("%s is owned by %s, want Administrators", name, obj.Owner)
		}
	}
	lock, err := sysacl.ReadObject(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if problems := sysacl.RestrictedProblems(lock, backupOperators, true); len(problems) > 0 || !lock.Protected {
		t.Fatalf("the new lock: protected %v, problems %v", lock.Protected, problems)
	}
}

// fileIndex is the file system's id for the file at path now. Not os.SameFile on two os.Stat
// results: on Windows a FileInfo from os.Stat reads its file id lazily, by opening its path again
// when first compared, so both would name whatever file is at the path at comparison time.
func fileIndex(t *testing.T, path string) uint64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		t.Fatal(err)
	}
	return uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
}

// A shared install on a log directory Beacon never secured -- one still inheriting from its
// parent, or made by hand -- is not taken for a restricted one, so nothing else in it is reset.
func TestShareSystemLogLeavesAnUnsecuredDirectoryAlone(t *testing.T) {
	requireSystemACLRights(t)
	testenv.SetHome(t, t.TempDir())
	prev := logReadersSID
	logReadersSID = func() (string, error) { return backupOperators, nil }
	t.Cleanup(func() { logReadersSID = prev })
	path := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	operator := filepath.Join(filepath.Dir(path), "operator.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(operator, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sysacl.LooksRestricted(filepath.Dir(path), backupOperators) {
		t.Fatal("a directory that inherits from its parent reads as restricted")
	}
	before, err := sysacl.ReadObject(operator)
	if err != nil {
		t.Fatal(err)
	}
	if err := ShareSystemLog(path); err != nil {
		t.Fatalf("ShareSystemLog: %v", err)
	}
	after, err := sysacl.ReadObject(operator)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.DACL, after.DACL) {
		t.Fatalf("a shared install reset a file it never restricted: %+v -> %+v", before.DACL, after.DACL)
	}
}
