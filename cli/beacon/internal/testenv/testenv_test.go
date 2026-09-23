package testenv

import (
	"os"
	"path/filepath"
	"runtime"
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
