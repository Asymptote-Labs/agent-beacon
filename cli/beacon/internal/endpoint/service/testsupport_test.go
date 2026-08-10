package service

import (
	"os"
	"testing"
	"time"
)

// stubCollectorEnv makes the test binary impersonate a long-running collector when set.
//
// See stubCollectorPath for why the stub is this binary rather than a script.
const stubCollectorEnv = "BEACON_TEST_STUB_COLLECTOR"

// TestMain lets the test binary re-exec itself as a stand-in collector.
func TestMain(m *testing.M) {
	if os.Getenv(stubCollectorEnv) != "" {
		// Long-lived and harmless. The supervised backend only needs the child to still be
		// running when Status is asked, and to die when it is signalled.
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// stubCollectorPath returns a program the supervised backend can actually start, on any platform.
//
// The obvious fixture -- a `#!/bin/sh` script written to a temp dir -- is not executable on
// Windows, where there are no shebangs and execution is decided by extension. It failed there with
// "executable file not found in %PATH%", which meant the one test covering process detach,
// graceful termination and liveness never ran on the platform whose implementations of all three
// are new.
//
// Re-execing the test binary avoids the question entirely: it is a real executable everywhere, it
// needs no shell, and it tolerates the `--config <path>` arguments the loader appends because it
// never looks at them. The child inherits the environment, so setting the marker here is what
// makes the re-exec sleep instead of running the suite again.
func stubCollectorPath(t *testing.T) string {
	t.Helper()
	t.Setenv(stubCollectorEnv, "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to use as a stub collector: %v", err)
	}
	return exe
}

// setTestHome redirects the user's home directory for a test on every platform.
//
// t.Setenv("HOME") alone is a POSIX-only redirect: os.UserHomeDir reads USERPROFILE on Windows, so
// a test that sets only HOME there silently keeps using the real profile. That is worse than a
// plain failure -- tests write into the developer's actual ~/.beacon and then see each other's
// state, which is why several of these reported "nothing installed yet" while finding an install.
func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
