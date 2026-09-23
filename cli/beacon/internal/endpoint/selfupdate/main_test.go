package selfupdate

import (
	"os"
	"testing"
)

// TestMain keeps every test in this package away from the machine's launchd. A rollback that
// restarts the collector also reloads the package-helper forwarders, and on a developer Mac with
// an S3 forwarder installed the real call would bootstrap it.
func TestMain(m *testing.M) {
	restoreScriptForwarders = func() error { return nil }
	os.Exit(m.Run())
}
