//go:build windows

package asymptoteobserve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// systemLogFixture is a log path outside this test's profile, so it reads as a system log rather
// than the user's own, and a marker writer that trusts this test's account.
func systemLogFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", os.Getenv("USERPROFILE"))
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	prev := logAccessMarkerTrustedSID
	logAccessMarkerTrustedSID = user.User.Sid.String()
	t.Cleanup(func() { logAccessMarkerTrustedSID = prev })
	dir, err := os.MkdirTemp("", "beacon-agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "runtime.jsonl")
}

func TestTheRestrictedMarkerOnWindows(t *testing.T) {
	log := systemLogFixture(t)
	if RuntimeLogIsPrivate(log) {
		t.Fatal("fixture: the log reads as private")
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("restricted before any marker")
	}
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	if !RuntimeLogAccessRestricted(log) {
		t.Fatal("the marker was not read")
	}
	if file, _, _ := RuntimeLogModes(log); file != RestrictedRuntimeLogFileMode {
		t.Fatalf("RuntimeLogModes = %v, want the restricted mode", file)
	}
	if err := SetRuntimeLogAccess(log, LogAccessShared); err != nil {
		t.Fatal(err)
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("still restricted after the marker was removed")
	}
}

// The marker counts only when no account but SYSTEM and Administrators could have written it. This
// one sits in the test's temp directory, which gives the test's own account full control of its
// files; once that account is no longer trusted the marker is ignored, whether or not the test runs
// elevated (an elevated account's files are owned by Administrators, but the grant remains).
func TestAMarkerAnOrdinaryAccountCouldWriteIsIgnored(t *testing.T) {
	log := systemLogFixture(t)
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	logAccessMarkerTrustedSID = ""
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("a marker an ordinary account can write was trusted")
	}
}

func TestAMarkerOthersCanWriteIsIgnored(t *testing.T) {
	log := systemLogFixture(t)
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	marker := LogAccessMarkerPath(log)
	if out, err := exec.Command("icacls", marker, "/grant", "*S-1-5-32-545:(W)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("a marker Users can write was trusted")
	}
}

func TestAHardLinkedMarkerIsIgnored(t *testing.T) {
	log := systemLogFixture(t)
	other := filepath.Join(filepath.Dir(log), "elsewhere")
	if err := os.WriteFile(other, []byte(LogAccessRestricted+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, LogAccessMarkerPath(log)); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("a hard-linked marker was trusted")
	}
}

func TestAMarkerWithOtherContentIsNotRestricted(t *testing.T) {
	log := systemLogFixture(t)
	if err := os.WriteFile(LogAccessMarkerPath(log), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("a marker that says shared read as restricted")
	}
}
