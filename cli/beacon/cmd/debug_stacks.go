package cmd

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// debugStacksAfterEnv names a duration after which a beacon that is still running writes the stack
// of every goroutine to stderr, then carries on.
//
// It exists for the one place a hang leaves nothing else behind: a Windows MSI custom action, which
// has no console and whose output Windows Installer does not keep. The package scripts
// (packaging/windows/package-common.ps1) run every beacon.exe call under a time bound, set this a
// little inside it, and record stderr in their package log, so a call that wedges names the code it
// is stuck in before it is stopped. Go on Windows has no SIGQUIT to ask for the same dump from
// outside. Unset, or anything that is not a positive duration, does nothing.
const debugStacksAfterEnv = "BEACON_DEBUG_STACKS_AFTER"

// maxDebugStackBytes caps the dump. runtime.Stack truncates to the buffer it is given, so the
// buffer grows until the dump fits or reaches this.
const maxDebugStackBytes = 64 << 20

func startDebugStackTimer(stderr io.Writer) *time.Timer {
	after, err := time.ParseDuration(os.Getenv(debugStacksAfterEnv))
	if err != nil || after <= 0 {
		return nil
	}
	return time.AfterFunc(after, func() { writeGoroutineStacks(stderr, after) })
}

func writeGoroutineStacks(w io.Writer, after time.Duration) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= maxDebugStackBytes {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	fmt.Fprintf(w, "beacon: still running after %s (%s); the stack of every goroutine follows\n%s\n", after, debugStacksAfterEnv, buf)
}
