//go:build !windows

package writer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A hardened host sets a restrictive umask, and MkdirAll's mode argument is masked by it. Under
// umask 027 the log directory came out 0750 root:root; under 077, 0700. The runtime log inside is
// deliberately 0666 so hooks can append, but a process that cannot traverse the directory never
// reaches the file -- so every hook-sourced event was lost while the root-owned collector stayed
// healthy and doctor reported nothing wrong.
func TestSystemLogDirIsTraversableUnderARestrictiveUmask(t *testing.T) {
	for name, mask := range map[string]int{
		"umask 022 (default)": 0o022,
		"umask 027 (CIS)":     0o027,
		"umask 077 (strict)":  0o077,
	} {
		t.Run(name, func(t *testing.T) {
			old := syscall.Umask(mask)
			t.Cleanup(func() { syscall.Umask(old) })

			dir := filepath.Join(t.TempDir(), "beacon-agent")
			if err := EnsureSystemLogWritable(dir); err != nil {
				t.Fatal(err)
			}

			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			mode := info.Mode().Perm()
			// Traverse (x) and read (r) for other: a hook runs as the logged-in user, not as the
			// root process that created this.
			if mode&0o001 == 0 || mode&0o004 == 0 {
				t.Errorf("log directory is mode %o under umask %o; hooks running as another user "+
					"cannot reach the runtime log inside it", mode, mask)
			}
		})
	}
}

// The directory may already exist from an earlier install performed under a different umask.
// Creating it correctly is not enough if an existing one is left as it was found.
func TestSystemLogDirIsCorrectedWhenItAlreadyExists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "beacon-agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := EnsureSystemLogWritable(dir); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o001 == 0 {
		t.Errorf("an existing 0700 log directory was left at %o; a reinstall or repair is exactly "+
			"when an operator expects this to be put right", mode)
	}
}
