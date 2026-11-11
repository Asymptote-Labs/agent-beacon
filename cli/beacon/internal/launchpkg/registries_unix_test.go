//go:build !windows

package launchpkg

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Launcher config is read with the launcher user's rights. A file that is not there is not there for
// the launcher either; anything Beacon cannot read for another reason, it cannot vouch for, so the
// ecosystem is held back as private. A FIFO is turned down without waiting for a writer.
func TestUnreadableLauncherConfigHoldsTheEcosystemBack(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	fifo := filepath.Join(outside, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	public := filepath.Join(outside, "public-npmrc")
	writeConfig(t, public, "registry=https://registry.npmjs.org/\n")
	locked := filepath.Join(outside, "locked")
	writeConfig(t, filepath.Join(locked, "npmrc"), "registry=https://registry.npmjs.org/\n")
	writeConfig(t, filepath.Join(locked, "bin", "node"), "")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	read := func(npmrc string) Registries {
		t.Helper()
		writeConfig(t, filepath.Join(home, ".npmrc"), npmrc)
		done := make(chan Registries, 1)
		go func() { done <- LocalRegistries(RegistryEnv{Home: home}) }()
		select {
		case reg := <-done:
			return reg
		case <-time.After(5 * time.Second):
			t.Fatal("reading registry config blocked on a FIFO")
			return Registries{}
		}
	}
	cases := map[string]bool{
		fifo:                              true,
		outside:                           true,
		public:                            false,
		filepath.Join(outside, "missing"): false,
	}
	if os.Geteuid() != 0 {
		// Root enters any directory.
		cases[filepath.Join(locked, "npmrc")] = true
		cases[filepath.Join(locked, "missing")] = true
	}
	for path, private := range cases {
		if reg := read("userconfig=" + path + "\n"); reg.NPM != private {
			t.Errorf("userconfig=%s: npm private = %v, want %v", path, reg.NPM, private)
		}
	}

	// A server's own node is looked up the same way.
	reg := read("")
	if reg.forServer(filepath.Join(outside, "nowhere", "npx"), nil, map[string]interface{}{}, nil).NPM {
		t.Error("a server run from a directory that is not there held npm back")
	}
	if os.Geteuid() != 0 && !reg.forServer(filepath.Join(locked, "bin", "npx"), nil, map[string]interface{}{}, nil).NPM {
		t.Error("a server whose node could not be looked up left npm public")
	}
}
