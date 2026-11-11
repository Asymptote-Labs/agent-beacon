//go:build !windows

package launchpkg

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// The system inventory job reads the console user's launcher config as root, and an npmrc or a
// server's command can send it anywhere. Under its confinement it reads outside the home only what
// every local user could, and holds back what it will not read: a planted FIFO neither hangs it nor
// reads as absent, and whatever lies past a directory others may not enter makes the ecosystem
// private whether or not it is there, so the snapshot never tells the console user what is.
func TestConfinedRegistriesHoldBackWhatTheyCannotRead(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "launchpkg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home, outside := filepath.Join(base, "home"), filepath.Join(base, "outside")
	for _, dir := range []string{base, home, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fifo := filepath.Join(outside, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	public := filepath.Join(outside, "public-npmrc")
	if err := os.WriteFile(public, []byte("registry=https://registry.npmjs.org/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(outside, "own-npmrc")
	if err := os.WriteFile(own, []byte("registry=https://registry.npmjs.org/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(outside, "private")
	writeConfig(t, filepath.Join(private, "npmrc"), "registry=https://registry.npmjs.org/\n")
	writeConfig(t, filepath.Join(private, "bin", "node"), "")
	c, err := sessionfs.Confine(home, os.Getuid(), sessionfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()

	read := func(npmrc string) Registries {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte(npmrc), 0o600); err != nil {
			t.Fatal(err)
		}
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
	for path, private := range map[string]bool{
		fifo:                              true,
		public:                            false,
		filepath.Join(outside, "missing"): false,
		own:                               true,
		filepath.Join(private, "npmrc"):   true,
		filepath.Join(private, "missing"): true,
	} {
		if reg := read("userconfig=" + path + "\n"); reg.NPM != private {
			t.Errorf("userconfig=%s: npm private = %v, want %v", path, reg.NPM, private)
		}
	}

	// A server's own node is looked up the same way: one past a directory others may not enter
	// holds npm back whether or not it is there.
	reg := read("")
	for _, dir := range []string{filepath.Join(private, "bin"), filepath.Join(private, "nothing")} {
		if !reg.forServer(filepath.Join(dir, "npx"), nil, map[string]interface{}{}, nil).NPM {
			t.Errorf("a server run from %s left npm public", dir)
		}
	}
}
