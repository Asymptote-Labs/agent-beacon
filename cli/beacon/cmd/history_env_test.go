package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Every test in this package runs with the local history pointed at a path that does not exist, so
// a command under test never reads or writes a developer's real ~/.beacon/endpoint/history.db, and
// with the session backfill off, so an install under test never sweeps a real agent session store
// that a runtime-specific environment variable points outside the test's home.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beacon-cmd-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv(endpointconfig.HistoryStoreEnv, filepath.Join(dir, "absent", "history.db"))
	_ = os.Setenv(endpointBackfillEnv, "0")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
