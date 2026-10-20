package inventory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// A hooks file the installer wrote is reported as Beacon-managed. Hooks written since the
// settings moved to --log/--config flags carry no BEACON_ENDPOINT_MODE=1 prefix.
func TestBeaconManagedRecognizesTheHooksInstallWrites(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	cursor, err := hooks.InstallCursor(hooks.CursorOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
	if err != nil {
		t.Fatal(err)
	}
	antigravity, err := hooks.InstallAntigravity(hooks.AntigravityOptions{Level: hooks.LevelUser, LogPath: logPath, UserMode: true})
	if err != nil {
		t.Fatal(err)
	}
	for runtime, path := range map[string]string{"cursor": cursor.HooksJSONPath, "antigravity_cli": antigravity.ConfigPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !beaconManaged(candidate{runtime: runtime}, data) {
			t.Errorf("%s hooks the installer wrote at %s are not reported as Beacon-managed", runtime, path)
		}
	}
	if beaconManaged(candidate{runtime: "cursor"}, []byte(`{"version":1,"hooks":{"stop":[{"command":"/usr/local/bin/notify --done"}]}}`)) {
		t.Error("someone else's Cursor hook is reported as Beacon-managed")
	}
}
