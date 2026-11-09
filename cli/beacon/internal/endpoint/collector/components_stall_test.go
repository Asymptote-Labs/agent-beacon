package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// A binary that hangs on `components` and leaves a child holding its output pipe -- a shell script
// whose sleep is a forked child, as macOS's /bin/sh runs it -- must not hold readComponents past its
// timeout: killing the shell does not close the pipe the child still has, and without WaitDelay
// Output() waits for that child. The ci tests' fake collectors did exactly this, and every
// Provision stalled a minute on macOS.
func TestReadComponentsDoesNotWaitForAChildHoldingThePipe(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	binary := filepath.Join(t.TempDir(), "collector")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 30\necho done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevTimeout := componentsTimeout
	componentsTimeout = 200 * time.Millisecond
	t.Cleanup(func() { componentsTimeout = prevTimeout })

	start := time.Now()
	if _, ok := readComponents(binary); ok {
		t.Fatal("a binary that never answered listed its components")
	}
	if elapsed := time.Since(start); elapsed > componentsTimeout+componentsWaitDelay+5*time.Second {
		t.Fatalf("readComponents took %v; a child holding the pipe kept it waiting", elapsed)
	}
}
