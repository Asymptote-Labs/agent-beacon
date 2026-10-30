//go:build windows

package writer

import (
	"os"
	"path/filepath"
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
	before, _ := os.Stat(path)
	if err := RestrictSystemLog(path, 0); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
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
}
