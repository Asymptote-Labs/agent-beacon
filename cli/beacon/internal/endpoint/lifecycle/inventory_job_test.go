package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// recordingInventoryJob stands in for service.InventoryManager and records what lifecycle asked of it.
type recordingInventoryJob struct {
	calls     *[]string
	supported bool
	unitPath  string
	loadErr   error
	// scheduled makes Status report an already-loaded job, the reinstall case.
	scheduled bool
}

func (r recordingInventoryJob) Supported() bool           { return r.supported }
func (r recordingInventoryJob) UnsupportedReason() string { return "no scheduler in this test" }
func (r recordingInventoryJob) UnitPath() (string, error) { return r.unitPath, nil }
func (r recordingInventoryJob) UnitPaths() []string {
	return []string{r.unitPath, r.unitPath + ".service"}
}
func (r recordingInventoryJob) WriteUnit(program, logPath string) (string, error) {
	*r.calls = append(*r.calls, "write "+program+" "+logPath)
	return r.unitPath, nil
}
func (r recordingInventoryJob) Load() error {
	*r.calls = append(*r.calls, "load")
	return r.loadErr
}
func (r recordingInventoryJob) Unload() error          { *r.calls = append(*r.calls, "unload"); return nil }
func (r recordingInventoryJob) RemoveUnits()           { *r.calls = append(*r.calls, "remove") }
func (r recordingInventoryJob) Status() service.Status { return service.Status{Loaded: r.scheduled} }

func installFakeInventoryJob(t *testing.T, supported bool) *[]string {
	return installFakeInventoryJobWithLoadError(t, supported, nil)
}

func installFakeInventoryJobWithLoadError(t *testing.T, supported bool, loadErr error) *[]string {
	t.Helper()
	calls := &[]string{}
	old := newInventoryJob
	newInventoryJob = func(userMode bool, kind service.Kind) inventoryJobController {
		return recordingInventoryJob{calls: calls, supported: supported, unitPath: "/fake/" + InventoryUnitName(userMode), loadErr: loadErr}
	}
	t.Cleanup(func() { newInventoryJob = old })
	return calls
}

func InventoryUnitName(userMode bool) string {
	if userMode {
		return "user.plist"
	}
	return "system.plist"
}

func writeInventoryConfig(t *testing.T, userMode bool, body string) {
	t.Helper()
	path := endpointconfig.ConfigPath(userMode)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// No config at all is the fresh-install case, and it must mean enabled: the job is on by default.
func TestReconcileInventoryJobWritesAndLoadsWhenEnabled(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	calls := installFakeInventoryJob(t, true)
	result, err := ReconcileInventoryJob(InventoryJobOptions{UserMode: true, Program: "/usr/local/bin/beacon", LogPath: "/tmp/runtime.jsonl", Load: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enabled || !result.Loaded || result.Removed || result.Skipped != "" || result.UnitPath != "/fake/user.plist" {
		t.Fatalf("result = %+v", result)
	}
	if got := strings.Join(*calls, ","); got != "write /usr/local/bin/beacon /tmp/runtime.jsonl,load" {
		t.Fatalf("calls = %q", got)
	}
}

func TestReconcileInventoryJobUnloadsAndRemovesWhenDisabled(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"inventory_heartbeat":{"enabled":false}}`)
	calls := installFakeInventoryJob(t, true)
	result, err := ReconcileInventoryJob(InventoryJobOptions{UserMode: true, Program: "/usr/local/bin/beacon", Load: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Enabled || !result.Removed || result.Loaded || result.UnitPath != "" {
		t.Fatalf("result = %+v", result)
	}
	if got := strings.Join(*calls, ","); got != "unload,remove" {
		t.Fatalf("calls = %q", got)
	}
}

func TestReconcileInventoryJobSkipsUnsupportedWithoutError(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	calls := installFakeInventoryJob(t, false)
	result, err := ReconcileInventoryJob(InventoryJobOptions{UserMode: true, Program: "/usr/local/bin/beacon", Load: true})
	if err != nil {
		t.Fatalf("an unsupported host must not error: %v", err)
	}
	if result.Skipped == "" || result.Loaded || result.UnitPath != "" || len(*calls) != 0 {
		t.Fatalf("result = %+v calls = %q", result, *calls)
	}
}

func TestReconcileInventoryJobWritesWithoutLoadingWhenAsked(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	calls := installFakeInventoryJob(t, true)
	result, err := ReconcileInventoryJob(InventoryJobOptions{UserMode: true, Program: "/usr/local/bin/beacon", Load: false})
	if err != nil {
		t.Fatal(err)
	}
	if result.Loaded || result.UnitPath == "" || strings.Join(*calls, ",") != "write /usr/local/bin/beacon " {
		t.Fatalf("result = %+v calls = %q", result, *calls)
	}
}

func TestInventoryEnabledFromConfigFileDefaultsToTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if !inventoryEnabledFromConfigFile(path) {
		t.Fatal("missing config must mean enabled")
	}
	for body, want := range map[string]bool{
		`{}`:                         true,
		`{"inventory_heartbeat":{}}`: true,
		`{"inventory_heartbeat":{"enabled":true}}`:                      true,
		`{"inventory_heartbeat":{"enabled":false}}`:                     false,
		`{"inventory_heartbeat":{"enabled":false,"ttl_seconds":86400}}`: false,
		`not json`: true,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := inventoryEnabledFromConfigFile(path); got != want {
			t.Fatalf("%s: enabled = %t, want %t", body, got, want)
		}
	}
}

// A package upgrade re-runs install, which rebuilds config.json from defaults. An operator's
// explicit `enabled: false` has to survive that, or the job would silently come back.
func TestBuildConfigPreservesInventoryBlock(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"log_path":"/tmp/runtime.jsonl","inventory_heartbeat":{"enabled":false,"runtimes":["cursor"]}}`)
	cfg := buildConfig(InstallOptions{UserMode: true})
	if cfg.Inventory == nil || cfg.Inventory.Enabled == nil || *cfg.Inventory.Enabled || len(cfg.Inventory.Runtimes) != 1 {
		t.Fatalf("buildConfig dropped the inventory block: %+v", cfg.Inventory)
	}
}

func TestInstallWritesTheInventoryJobWithoutLoadingItOnNoStart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := installFakeInventoryJob(t, true)
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	result, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.InventoryJobPath != "/fake/user.plist" || result.InventoryJobDetail != "" {
		t.Fatalf("result inventory fields = %q / %q", result.InventoryJobPath, result.InventoryJobDetail)
	}
	if got := strings.Join(*calls, ","); !strings.HasPrefix(got, "write ") || strings.Contains(got, "load") {
		t.Fatalf("--no-start must write the unit and not load it: %q", got)
	}
	if !strings.HasSuffix((*calls)[0], " "+logPath) {
		t.Fatalf("the unit must pin the install's runtime log: %q", (*calls)[0])
	}
	manifest, err := ReadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 3 {
		t.Fatalf("the inventory unit is owned by the reconcile, not the manifest: %#v", manifest.Files)
	}
}

func TestInstallReportsAHostWithoutASchedulerAndStillSucceeds(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := installFakeInventoryJob(t, false)
	result, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"),
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
	})
	if err != nil {
		t.Fatalf("a missing scheduler must not fail the install: %v", err)
	}
	if result.InventoryJobPath != "" || result.InventoryJobDetail == "" || len(*calls) != 0 {
		t.Fatalf("result = %+v calls = %q", result, *calls)
	}
}

func TestUninstallRemovesTheInventoryJobForTheMode(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	old := removeInventoryJob
	var removedFor []bool
	removeInventoryJob = func(userMode bool) { removedFor = append(removedFor, userMode) }
	t.Cleanup(func() { removeInventoryJob = old })
	if err := Uninstall(UninstallOptions{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	if len(removedFor) != 1 || !removedFor[0] {
		t.Fatalf("uninstall should remove the user-mode inventory job once, got %v", removedFor)
	}
}

// A job that was loaded by an install that then fails must be unloaded by the rollback, or the
// scheduler keeps a unit registered against config that no longer exists.
func TestRollbackUnloadsALoadedInventoryJob(t *testing.T) {
	calls := &[]string{}
	job := recordingInventoryJob{calls: calls, supported: true, unitPath: filepath.Join(t.TempDir(), "x.plist")}
	tx := newInstallRollback(service.Manager{UserMode: true, Kind: service.KindSupervised})
	tx.InventoryJob = job
	tx.InventoryLoaded = true
	tx.Rollback(Manifest{})
	if strings.Join(*calls, ",") != "unload" {
		t.Fatalf("rollback calls = %q, want unload", *calls)
	}
	*calls = nil
	tx.InventoryLoaded = false
	tx.Rollback(Manifest{})
	if len(*calls) != 0 {
		t.Fatalf("rollback must not unload a job it never loaded: %q", *calls)
	}
	// A job that was already scheduled before the install is brought back, not torn down: the
	// failed reinstall must not end inventory on a healthy endpoint.
	*calls = nil
	tx.InventoryLoaded = true
	tx.InventoryWasLoaded = true
	tx.Rollback(Manifest{})
	if strings.Join(*calls, ",") != "load" {
		t.Fatalf("rollback of a reinstall should re-load the previously scheduled job, got %q", *calls)
	}
}

func TestReconcileSurfacesARefusedLoad(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	installFakeInventoryJobWithLoadError(t, true, errors.New("enable --now refused"))
	result, err := ReconcileInventoryJob(InventoryJobOptions{UserMode: true, Program: "/usr/local/bin/beacon", Load: true})
	if err == nil || result.Loaded {
		t.Fatalf("a refused load must surface: result=%+v err=%v", result, err)
	}
}

func TestStableProgramPathPrefersTheLinkedHomebrewBinary(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "beacon", "1.3.11", "bin", "beacon")
	linked := filepath.Join(prefix, "bin", "beacon")
	for _, p := range []string{keg, linked} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := stableProgramPath(keg); got != linked {
		t.Fatalf("stableProgramPath(keg) = %q, want the linked %q", got, linked)
	}
	if got := stableProgramPath("/opt/beacon/bin/beacon"); got != "/opt/beacon/bin/beacon" {
		t.Fatalf("non-Homebrew path must pass through, got %q", got)
	}
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if got := stableProgramPath(keg); got != keg {
		t.Fatalf("without a linked binary the keg path stays, got %q", got)
	}
}
