package logging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// TestMain keeps the active-session record every endpoint event updates out of the real home
// directory; tests that check it point BEACON_ACTIVE_SESSIONS_FILE somewhere of their own.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beacon-hooks-active-")
	if err != nil {
		panic(err)
	}
	os.Setenv(activesession.PathEnv, filepath.Join(dir, activesession.FileName))
	// Auto-share is off unless a test turns it on in a settings file of its own.
	settingsPath := filepath.Join(dir, "share-settings.json")
	ShareSettingsPath = func() string { return settingsPath }
	// Never send to a real collector's ingest socket, which a developer's restricted-mode machine
	// has; tests that exercise the socket start a server of their own.
	EndpointIngest = localingest.Deliverer{
		Client: localingest.Client{SocketPath: filepath.Join(dir, "no-ingest.sock")},
		Spool:  localingest.Spool{Dir: filepath.Join(dir, "spool")},
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
