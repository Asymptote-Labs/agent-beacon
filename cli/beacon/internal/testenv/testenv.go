// Package testenv holds environment helpers shared by tests across the CLI.
//
// It exists for one recurring mistake. Redirecting the home directory with t.Setenv("HOME", dir)
// is a POSIX-only redirect: os.UserHomeDir reads USERPROFILE on Windows, so a test that sets only
// HOME there silently keeps using the real profile. That is worse than a plain failure -- tests
// write into the developer's actual ~/.beacon and then observe each other's state, which is how
// several of them came to report "nothing installed yet" while finding an install.
//
// Importing testing from a non-test file is deliberate: only _test.go files import this package,
// so nothing reaches a shipped binary, and the alternative -- a copy of SetHome in every package
// that needs it -- is what produced the inconsistency in the first place.
package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// hostPath is PATH as the test binary inherited it. Package initialization runs before any
// TestMain, so this is the machine's PATH even after IsolatePath has replaced it.
var hostPath = os.Getenv("PATH")

// IsolatePath points PATH at an empty directory under dir for the rest of the test binary's life.
// Call it from TestMain, after creating dir.
//
// PATH is the other half of what harness discovery reads, alongside the home directory SetHome
// redirects. Inherited, it made results depend on the machine: a discovered agent CLI is run for
// its --version, so the same test took different paths on a developer's laptop and in CI. It also
// made every discovery a stat of each PATH directory for each of some thirty CLI names -- times
// twelve PATHEXT extensions on Windows -- and go test fingerprints every path a test touched for
// its test cache. On a Windows runner that fingerprinting cost about as much as the tests did.
//
// A test that needs a real command declares it with RequireCommands.
func IsolatePath(dir string) error {
	empty := filepath.Join(dir, "empty-path")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		return err
	}
	return os.Setenv("PATH", empty)
}

// RequireCommands gives the test a PATH holding only the directories the machine's PATH resolves
// the named commands to, and skips the test when one is not installed. It is the declared
// exception to IsolatePath, for a test that runs a real git or shell.
func RequireCommands(t *testing.T, names ...string) {
	t.Helper()
	t.Setenv("PATH", hostPath)
	var dirs []string
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s is not installed", name)
		}
		if dir := filepath.Dir(path); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

// UseHostPath restores the machine's whole PATH for one test. It is for a test whose point is to
// run software as the user installed it -- an agent CLI that is a script needs its interpreter,
// wherever that lives -- and so cannot name everything it depends on.
func UseHostPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", hostPath)
}

// SetHome redirects the user's home directory for the duration of a test, on every platform.
//
// Both variables are set because different code reaches for different ones: os.UserHomeDir prefers
// USERPROFILE on Windows and HOME elsewhere, while some callers read HOME directly. Setting only
// one leaves whichever half of that split the caller happens to use pointing at the real profile.
func SetHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if runtime.GOOS == "windows" {
		// The per-user application directories are not derived from USERPROFILE by the programs
		// that read them -- they have their own variables, and Windows sets all of them
		// independently. Redirecting only the profile left anything resolving %APPDATA% (VS Code's
		// settings, for one) writing into the developer's real roaming profile while the test
		// believed it had been sandboxed.
		t.Setenv("APPDATA", filepath.Join(dir, "AppData", "Roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(dir, "AppData", "Local"))
	}
}

// RequirePOSIXFileModes skips a test whose assertion is about Unix permission bits.
//
// Windows does not implement them: os.Chmod only toggles a read-only attribute, and Go reports
// 0666 for any ordinary file. A mode assertion there is not merely unsupported, it is misleading --
// it would either fail for a reason unrelated to the property or pass vacuously.
//
// The property these tests protect is real on both platforms: a config file holding a credential
// must not be readable by other users. Windows expresses that as an ACL rather than a mode, so
// this is the seam where a Windows equivalent belongs once the endpoint sets ACLs on the files it
// writes. Skipping is honest in the meantime; asserting 0666 == 0600 would not be.
func RequirePOSIXFileModes(t *testing.T) {
	t.Helper()
	if !HasPOSIXFileModes() {
		t.Skip("Unix permission bits do not exist on Windows; the equivalent is an ACL check")
	}
}

// HasPOSIXFileModes reports whether this platform reports the Unix permission bits Beacon writes.
//
// It is the per-assertion form of RequirePOSIXFileModes, for a test that checks a mode alongside
// other behavior: `if testenv.HasPOSIXFileModes() && perm != 0o600`. Skipping the whole test there
// would throw away the assertions that do hold on Windows -- that the file was written, and what it
// contains -- to avoid the one that cannot.
func HasPOSIXFileModes() bool {
	return runtime.GOOS != "windows"
}

// RequirePOSIXExecutableFixtures skips a test that stands in for a CLI with a shell script.
//
// Two things stop that working on Windows, and only one of them is the extension. exec.LookPath
// resolves a bare name through PATHEXT, so a file written without one is invisible to it -- but
// renaming the fixture to `codex.exe` fixes only the lookup. These tests then *execute* the stub to
// read its --version output, and a `#!/bin/sh` file is not runnable on Windows whatever it is
// called, because there are no shebangs. That is the difference from the collector fixtures, where
// the extension alone was enough: discovery there only stats the file.
//
// Skipped rather than adapted because these tests are about discovery logic that is not
// platform-specific, and the packages they live in are gated on Windows for their *path* handling.
// A Windows fixture is possible -- copy the test binary and have TestMain impersonate the CLI, as
// the service package does for its stub collector -- and is tracked in the Windows test-suite
// issue rather than done here, where it would test nothing this change touches.
func RequirePOSIXExecutableFixtures(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script executable fixtures cannot run on Windows; see the Windows test-suite issue")
	}
}
