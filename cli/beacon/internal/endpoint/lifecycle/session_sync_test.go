package lifecycle

import (
	"reflect"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The session sync is opt-in: with no config, or a config without the block, it is off, and
// reconciling removes any job a previous setting installed.
func TestReconcileSessionSyncJobIsOffByDefault(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	for _, body := range []string{"", `{"user_mode":true}`, `{"user_mode":true,"session_sync":{"enabled":false}}`} {
		if body != "" {
			writeInventoryConfig(t, true, body)
		}
		calls := &[]string{}
		result, err := reconcileSessionSyncJobWith(recordingInventoryJob{calls: calls, supported: true, unitPath: "/fake/s.plist"},
			InventoryJobOptions{UserMode: true, Program: "/bin/beacon", Load: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Enabled || !result.Removed || !reflect.DeepEqual(*calls, []string{"unload", "remove"}) {
			t.Fatalf("config %q: result %+v calls %v, want the job removed", body, result, *calls)
		}
	}
}

func TestReconcileSessionSyncJobWritesAndLoadsWhenEnabled(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"session_sync":{"enabled":true}}`)
	calls := &[]string{}
	result, err := reconcileSessionSyncJobWith(recordingInventoryJob{calls: calls, supported: true, unitPath: "/fake/s.plist"},
		InventoryJobOptions{UserMode: true, Program: "/bin/beacon", LogPath: "/l/runtime.jsonl", Load: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enabled || !result.Loaded || result.UnitPath != "/fake/s.plist" {
		t.Fatalf("result = %+v", result)
	}
	if want := []string{"write /bin/beacon /l/runtime.jsonl", "load"}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}

	// --no-start writes the unit and leaves it unloaded.
	*calls = nil
	if result, err = reconcileSessionSyncJobWith(recordingInventoryJob{calls: calls, supported: true, unitPath: "/fake/s.plist"},
		InventoryJobOptions{UserMode: true, Program: "/bin/beacon"}); err != nil || result.Loaded {
		t.Fatalf("no-start: %+v, %v", result, err)
	}

	// No scheduler: reported, not an error.
	*calls = nil
	result, err = reconcileSessionSyncJobWith(recordingInventoryJob{calls: calls, supported: false},
		InventoryJobOptions{UserMode: true, Program: "/bin/beacon", Load: true})
	if err != nil || result.Skipped == "" || len(*calls) != 0 {
		t.Fatalf("unsupported: %+v %v calls %v", result, err, *calls)
	}
}

// Repair and package upgrades run install with no session sync flag; they must keep the setting.
func TestBuildConfigKeepsSessionSyncUntilChanged(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"log_path":"/tmp/runtime.jsonl","session_sync":{"enabled":true}}`)
	if cfg := buildConfig(InstallOptions{UserMode: true}); !endpointconfig.SessionSyncEnabled(cfg) {
		t.Fatalf("buildConfig dropped session_sync: %+v", cfg.SessionSync)
	}
	off := false
	if cfg := buildConfig(InstallOptions{UserMode: true, SessionSync: &off}); endpointconfig.SessionSyncEnabled(cfg) {
		t.Fatal("--no-session-sync did not turn it off")
	}
	on := true
	testenv.SetHome(t, t.TempDir())
	if cfg := buildConfig(InstallOptions{UserMode: true}); endpointconfig.SessionSyncEnabled(cfg) {
		t.Fatal("a fresh install turned session sync on by default")
	}
	if cfg := buildConfig(InstallOptions{UserMode: true, SessionSync: &on}); !endpointconfig.SessionSyncEnabled(cfg) {
		t.Fatal("--session-sync did not turn it on")
	}
}
