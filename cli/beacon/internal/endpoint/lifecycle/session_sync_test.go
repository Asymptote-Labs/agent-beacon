package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
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

// A user-mode job carries the store-locating variables set at install, including the ones only a
// session-store collector reads.
func TestSessionSyncJobEnvCarriesCollectorVariables(t *testing.T) {
	for _, name := range append(append([]string{}, sessionSyncJobPathEnv...), sessionSyncJobNameEnv...) {
		t.Setenv(name, "")
	}
	abs := filepath.Join(t.TempDir(), "copilot")
	t.Setenv("COPILOT_HOME", abs)
	t.Setenv("HARNESS2_RUNS_DIR", "relative-runs")
	t.Setenv("OPENCODE_DB", "custom.db")
	env := sessionSyncJobEnv(true)
	if env["COPILOT_HOME"] != abs || env["OPENCODE_DB"] != "custom.db" || !filepath.IsAbs(env["HARNESS2_RUNS_DIR"]) {
		t.Fatalf("env = %v", env)
	}
	if sessionSyncJobEnv(false) != nil {
		t.Fatal("system mode records root's environment")
	}
}

// Every variable a session-store collector reads to find its store is one the job carries, so a
// store moved by one is read on schedule as well as at install. APPDATA and LOCALAPPDATA are
// Windows-only, where no job is installed.
func TestSessionSyncJobEnvCoversEveryCollectorVariable(t *testing.T) {
	carried := map[string]bool{
		"CODEX_HOME": true, "HERMES_HOME": true, "HERMES_DATA_DIR_SUFFIX": true, "CLINE_DIR": true,
		"GEMINI_CLI_HOME": true, "CLAUDE_CONFIG_DIR": true, "APPDATA": true, "LOCALAPPDATA": true,
	}
	for _, list := range [][]string{inventoryJobPathEnv, inventoryJobNameEnv, sessionSyncJobPathEnv, sessionSyncJobNameEnv} {
		for _, name := range list {
			carried[name] = true
		}
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "*session", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no collector sources found: %v", err)
	}
	read := regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Z0-9_]+)"\)`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range read.FindAllStringSubmatch(string(data), -1) {
			if !carried[m[1]] {
				t.Errorf("%s reads %s, which the scheduled session sync job does not carry; add it to sessionSyncJobPathEnv or sessionSyncJobNameEnv", file, m[1])
			}
		}
	}
}

// A repair that turned the setting off and then failed puts back the job that was scheduled
// before; one that never touched a scheduled job leaves it alone.
func TestRollbackRestoresSessionSyncJobTurnedOff(t *testing.T) {
	calls := &[]string{}
	tx := &installRollback{snapshots: map[string]fileSnapshot{},
		SessionSyncJob: recordingInventoryJob{calls: calls, supported: true}, SessionSyncRemoved: true, SessionSyncWasLoaded: true}
	tx.Rollback(Manifest{})
	if !reflect.DeepEqual(*calls, []string{"load"}) {
		t.Fatalf("calls = %v, want the previously scheduled job loaded again", *calls)
	}

	*calls = nil
	tx = &installRollback{snapshots: map[string]fileSnapshot{},
		SessionSyncJob: recordingInventoryJob{calls: calls, supported: true}, SessionSyncRemoved: true}
	tx.Rollback(Manifest{})
	if len(*calls) != 0 {
		t.Fatalf("calls = %v, want nothing for a job that was not scheduled before", *calls)
	}
}

// Turning the sync on or off changes session_sync and nothing else. A repair would rebuild the
// config from its flags, resetting harnesses and ports and dropping a HEC destination.
func TestSetSessionSyncChangesOnlyItsOwnKey(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	const body = `{"user_mode":true,"log_path":"/l/runtime.jsonl","harnesses":["claude","cursor"],` +
		`"collector":{"grpc_port":5317,"http_port":5318},` +
		`"destinations":{"splunk_hec":{"enabled":true,"endpoint":"https://splunk.example:8088","token":"hec-token"}},` +
		`"not_yet_known":{"kept":true}}`
	writeInventoryConfig(t, true, body)
	calls := &[]string{}
	old := newSessionSyncJob
	newSessionSyncJob = func(bool, service.Kind) inventoryJobController {
		return recordingInventoryJob{calls: calls, supported: true, unitPath: "/fake/s.plist"}
	}
	t.Cleanup(func() { newSessionSyncJob = old })

	result, err := SetSessionSync(InventoryJobOptions{UserMode: true, Program: "/bin/beacon", Load: true}, true)
	if err != nil || !result.Enabled || !result.Loaded {
		t.Fatalf("enable: %+v, %v", result, err)
	}
	// The job takes the config's log path when none is given.
	if want := []string{"write /bin/beacon /l/runtime.jsonl", "load"}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	path := endpointconfig.ConfigPath(true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(body), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after["session_sync"], map[string]any{"enabled": true}) {
		t.Fatalf("session_sync = %v", after["session_sync"])
	}
	delete(after, "session_sync")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("other keys changed:\nbefore %v\nafter  %v", before, after)
	}
	// It holds a HEC token, so the rewrite keeps it private.
	if testenv.HasPOSIXFileModes() {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}

	*calls = nil
	if result, err = SetSessionSync(InventoryJobOptions{UserMode: true, Program: "/bin/beacon", Load: true}, false); err != nil || !result.Removed {
		t.Fatalf("disable: %+v, %v", result, err)
	}
	if want := []string{"unload", "remove"}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestSetSessionSyncNeedsAnInstalledEndpoint(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	if _, err := SetSessionSync(InventoryJobOptions{UserMode: true, Program: "/bin/beacon"}, true); err == nil || !strings.Contains(err.Error(), "install it first") {
		t.Fatalf("err = %v, want a pointer to install", err)
	}
}
