package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Every test in this package runs with the local history pointed at a path that does not exist, so
// a command under test never reads or writes a developer's real ~/.beacon/endpoint/history.db, and
// with the session backfill off, so an install under test never sweeps a real agent session store
// that a runtime-specific environment variable points outside the test's home.
//
// The git hooks' wall-clock budgets are raised too. A hook spawns git a dozen or more times, and on
// a loaded Windows runner each spawn can cost a few hundred milliseconds, so a test of what a hook
// writes would otherwise fail whenever the runner is slow rather than when the hook is wrong. Tests
// of the budgets themselves set the one they exercise.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beacon-cmd-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv(endpointconfig.HistoryStoreEnv, filepath.Join(dir, "absent", "history.db"))
	_ = os.Setenv(endpointBackfillEnv, "0")
	gitHookBudget = time.Minute
	gitShareBudget = time.Minute
	gitShareUploadBudget = gitShareBudget - gitShareNotesReserve - gitShareNoteWriteBudget
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
