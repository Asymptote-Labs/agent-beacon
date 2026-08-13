//go:build !windows

package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

func logAt(t *testing.T, dirMode, fileMode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "beacon-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		t.Fatal(err)
	}
	// Directory last: chmod on a file inside it must happen while it is still traversable.
	if err := os.Chmod(dir, dirMode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	return path
}

// In system mode the collector runs as root and hooks run as the logged-in user, so the question
// is whether a non-root process can write. mode&0222 could not answer it -- the owner's write bit
// satisfies it, so a root-owned 0600 log passed as healthy while every hook write failed.
func TestSystemLogPermissionsRejectAnOwnerOnlyLog(t *testing.T) {
	path := logAt(t, 0o755, 0o600)

	got := checkLogPermissions(path, false)
	if got.Status != StatusFail {
		t.Errorf("status = %v for a 0600 log in system mode, want fail: no hook can write it", got.Status)
	}
	if got.Action == "" {
		t.Error("a failure an operator can fix should say how")
	}
}

// The file mode alone is not enough. A hook that cannot traverse the directory never reaches the
// log, however permissive the log itself is -- which is the state a hardened umask produces.
func TestSystemLogPermissionsRejectAnUnreachableDirectory(t *testing.T) {
	path := logAt(t, 0o700, 0o666)

	got := checkLogPermissions(path, false)
	if got.Status != StatusFail {
		t.Errorf("status = %v for a 0666 log inside a 0700 directory, want fail", got.Status)
	}
	if got.Evidence != "log_dir_not_traversable" {
		t.Errorf("evidence = %q, want the directory named as the cause", got.Evidence)
	}
}

func TestSystemLogPermissionsAcceptTheShippedLayout(t *testing.T) {
	path := logAt(t, 0o755, 0o666)

	if got := checkLogPermissions(path, false); got.Status != StatusOK {
		t.Errorf("status = %v (%s) for the layout install actually creates, want ok", got.Status, got.Message)
	}
}

// User mode is a different question: the log lives in that user's own profile and is written by
// processes running as them, so an owner-only file is correct and must not be failed.
func TestUserModeLogPermissionsAllowAnOwnerOnlyLog(t *testing.T) {
	path := logAt(t, 0o700, 0o600)

	if got := checkLogPermissions(path, true); got.Status == StatusFail {
		t.Errorf("status = fail (%s) for a private user-mode log, which is the correct layout there", got.Message)
	}
}
