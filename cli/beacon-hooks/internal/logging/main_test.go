package logging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
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
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
