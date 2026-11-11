//go:build !windows

package intel

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// The system inventory job reads the console user's launcher config as root, and an npmrc can
// send it anywhere. Under its confinement, a planted FIFO must neither hang it nor read as an
// absent file: what Beacon cannot read, it cannot vouch for.
func TestConfinedRegistriesHoldBackWhatTheyCannotRead(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	fifo := filepath.Join(outside, "npmrc")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	public := filepath.Join(outside, "public-npmrc")
	if err := os.WriteFile(public, []byte("registry=https://registry.npmjs.org/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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

	if reg := read("userconfig=" + fifo + "\n"); !reg.NPM {
		t.Errorf("a FIFO the npmrc names did not hold npm packages back")
	}
	if reg := read("userconfig=" + public + "\n"); reg.NPM {
		t.Errorf("a readable npmrc of the user's naming the public registry held npm packages back")
	}
	if reg := read("userconfig=" + filepath.Join(outside, "missing") + "\n"); reg.NPM {
		t.Errorf("a missing npmrc held npm packages back")
	}
	// A path root cannot even look up (macOS privacy protection, a root-squashed network home;
	// here a directory no one may enter) is absent, as it is unconfined.
	locked := filepath.Join(outside, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)
	if os.Geteuid() != 0 {
		if reg := read("userconfig=" + filepath.Join(locked, "npmrc") + "\n"); reg.NPM {
			t.Errorf("an npmrc root could not look up held npm packages back")
		}
	}
}
