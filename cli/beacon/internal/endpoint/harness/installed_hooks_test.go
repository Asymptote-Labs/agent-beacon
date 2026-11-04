package harness

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// Status and doctor must recognize exactly the hooks the installer writes. They once looked only
// for the BEACON_ENDPOINT_MODE=1 prefix, which install stopped writing when the settings moved to
// --log/--config flags, so every fresh install reported these runtimes' hooks as missing.
func TestDiscoveryRecognizesTheHooksInstallWrites(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HERMES_HOME", filepath.Join(home, ".hermes"))
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	cases := []struct {
		name     string
		install  func() error
		discover func() Harness
	}{
		{"cursor", func() error {
			_, err := hooks.InstallCursor(hooks.CursorOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
			return err
		}, DiscoverCursor},
		{"antigravity", func() error {
			_, err := hooks.InstallAntigravity(hooks.AntigravityOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
			return err
		}, DiscoverAntigravity},
		{"hermes", func() error {
			_, err := hooks.InstallHermes(hooks.HermesOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
			return err
		}, DiscoverHermes},
		{"devin", func() error {
			_, err := hooks.InstallDevin(hooks.DevinOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
			return err
		}, DiscoverDevin},
		{"devin desktop", func() error {
			_, err := hooks.InstallDevinDesktop(hooks.DevinDesktopOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
			return err
		}, DiscoverDevinDesktop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.install(); err != nil {
				t.Fatalf("install: %v", err)
			}
			got := tc.discover()
			if got.TelemetryStatus != TelemetryEnabled {
				t.Fatalf("after install, status = %s (%s), want enabled", got.TelemetryStatus, got.Message)
			}
		})
	}
}
