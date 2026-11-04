package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The whole point of SetHome is that the standard library's idea of home moves with it. Asserting
// through os.UserHomeDir rather than by reading the variables back is what makes this fail on the
// platform where setting HOME alone was the bug.
func TestSetHomeRedirectsUserHomeDir(t *testing.T) {
	dir := t.TempDir()
	SetHome(t, dir)

	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir: %v", err)
	}
	if got != dir {
		t.Fatalf("os.UserHomeDir() = %q, want %q", got, dir)
	}
	for _, name := range []string{"HOME", "USERPROFILE"} {
		if value := os.Getenv(name); value != dir {
			t.Errorf("%s = %q, want %q: callers that read it directly would escape the sandbox", name, value, dir)
		}
	}
}

func TestSetHomeRedirectsWindowsAppDataUnderTheNewHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("APPDATA and LOCALAPPDATA are only redirected on Windows")
	}
	dir := t.TempDir()
	SetHome(t, dir)

	for name, want := range map[string]string{
		"APPDATA":      filepath.Join(dir, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(dir, "AppData", "Local"),
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// The helper is a claim about the platform, so check the claim rather than the constant: on a
// platform that reports it, a file written 0600 must read back as 0600.
func TestHasPOSIXFileModesMatchesWhatStatReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	preserved := info.Mode().Perm() == 0o600
	if HasPOSIXFileModes() != preserved {
		t.Fatalf("HasPOSIXFileModes() = %t, but a 0600 file reads back as %o", HasPOSIXFileModes(), info.Mode().Perm())
	}
}

// RequireCommands is only worth having if it narrows: the declared command must resolve to the
// file the machine's PATH finds, a command the test did not declare must not resolve at all, and a
// declared command that is not installed must skip the test rather than let it run without it.
func TestRequireCommandsKeepsOnlyTheDeclaredCommands(t *testing.T) {
	exe := func(dir, name string) string {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	declaredDir, otherDir := t.TempDir(), t.TempDir()
	declared := exe(declaredDir, "beacon-declared")
	exe(otherDir, "beacon-undeclared")
	saved := hostPath
	hostPath = strings.Join([]string{declaredDir, otherDir}, string(os.PathListSeparator))
	t.Cleanup(func() { hostPath = saved })

	t.Run("declared", func(t *testing.T) {
		RequireCommands(t, "beacon-declared")
		if got, err := exec.LookPath("beacon-declared"); err != nil || got != declared {
			t.Fatalf("LookPath(declared) = %q, %v; want %q", got, err, declared)
		}
		if got, err := exec.LookPath("beacon-undeclared"); err == nil {
			t.Fatalf("LookPath(undeclared) = %q; a command the test did not declare must not resolve", got)
		}
	})

	ranWithoutIt := false
	t.Run("missing", func(t *testing.T) {
		RequireCommands(t, "beacon-not-installed")
		ranWithoutIt = true
	})
	if ranWithoutIt {
		t.Fatal("a test that requires a missing command ran instead of skipping")
	}
}
