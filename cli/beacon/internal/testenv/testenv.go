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
	"path/filepath"
	"runtime"
	"testing"
)

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
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not exist on Windows; the equivalent is an ACL check")
	}
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
