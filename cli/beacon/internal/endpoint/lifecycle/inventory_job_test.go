package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// The scheduled job runs without the installing shell's environment, so a per-user install
// records the CODEX_HOME it resolved, as an absolute path, and the job inventories what Codex reads.
// System mode and an unusable CODEX_HOME record nothing, leaving the job on ~/.codex.
func TestInventoryJobEnvRecordsCodexHomeForUserInstalls(t *testing.T) {
	work := t.TempDir()
	codexHome := filepath.Join(work, "codex-home")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("CLINE_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")

	// Relative, so the job, which starts elsewhere, needs the absolute form.
	t.Setenv("CODEX_HOME", "codex-home")
	env := inventoryJobEnv(true)
	got, err := os.Stat(env["CODEX_HOME"])
	if len(env) != 1 || !filepath.IsAbs(env["CODEX_HOME"]) || err != nil || !os.SameFile(got, want) {
		t.Fatalf("user mode env = %v, want CODEX_HOME as the absolute path of %s", env, codexHome)
	}
	if got := inventoryJobEnv(false); got != nil {
		t.Fatalf("system mode env = %v, want none: root's environment is not the console user's", got)
	}
	t.Setenv("CODEX_HOME", filepath.Join(work, "missing"))
	if got := inventoryJobEnv(true); got != nil {
		t.Fatalf("env for a CODEX_HOME that does not exist = %v, want none", got)
	}
	t.Setenv("CODEX_HOME", "")
	if got := inventoryJobEnv(true); got != nil {
		t.Fatalf("env with CODEX_HOME unset = %v, want none", got)
	}
}

// launchd and systemd start the job without the user's shell, so HERMES_HOME and the data-dir
// suffix are recorded in it. HERMES_HOME is recorded as given, before any profile is applied, so a
// later `hermes profile use` still reaches the job.
func TestInventoryJobEnvRecordsHermesHomeForUserInstalls(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	work := t.TempDir()
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("CLINE_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")

	// Relative and not created yet: Hermes creates its home, so it is recorded anyway.
	t.Setenv("HERMES_HOME", "hermes-data")
	want := filepath.Join(cwd, "hermes-data")
	if env := inventoryJobEnv(true); len(env) != 1 || env["HERMES_HOME"] != want {
		t.Fatalf("user mode env = %v, want only HERMES_HOME=%s", env, want)
	}
	if env := inventoryJobEnv(false); env != nil {
		t.Fatalf("system mode env = %v, want none: root's environment is not the console user's", env)
	}

	// A HERMES_HOME under a root with a sticky profile is still recorded as given.
	root := filepath.Join(home, ".hermes")
	if err := os.MkdirAll(filepath.Join(root, "profiles", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "active_profile"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", "~/.hermes")
	if env := inventoryJobEnv(true); env["HERMES_HOME"] != root {
		t.Fatalf("user mode env = %v, want HERMES_HOME=%s, not the active profile", env, root)
	}

	t.Setenv("HERMES_HOME", " ")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "-dev")
	if env := inventoryJobEnv(true); len(env) != 1 || env["HERMES_DATA_DIR_SUFFIX"] != "-dev" {
		t.Fatalf("user mode env = %v, want only HERMES_DATA_DIR_SUFFIX=-dev", env)
	}
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	if env := inventoryJobEnv(true); env != nil {
		t.Fatalf("env with nothing set = %v, want none", env)
	}
}

// Cline uses a trimmed, non-empty CLINE_DIR as is, so the job gets its absolute form whenever it is
// set. It is recorded beside CODEX_HOME, not instead of it.
func TestInventoryJobEnvRecordsClineDirForUserInstalls(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(work, "codex-home")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLINE_DIR", " cline-state ")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")

	env := inventoryJobEnv(true)
	if want := filepath.Join(cwd, "cline-state"); env["CLINE_DIR"] != want {
		t.Fatalf("CLINE_DIR = %q, want %q", env["CLINE_DIR"], want)
	}
	if env["CODEX_HOME"] == "" || len(env) != 2 {
		t.Fatalf("env = %v, want both CODEX_HOME and CLINE_DIR", env)
	}
	if got := inventoryJobEnv(false); got != nil {
		t.Fatalf("system mode env = %v, want none", got)
	}
}

// Gemini CLI uses a non-empty GEMINI_CLI_HOME as is, without requiring it to exist, so the job gets
// its absolute form whenever it is set. It is recorded beside CODEX_HOME, not instead of it.
func TestInventoryJobEnvRecordsGeminiCLIHomeForUserInstalls(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(work, "codex-home")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLINE_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "gemini-home")
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")

	env := inventoryJobEnv(true)
	if want := filepath.Join(cwd, "gemini-home"); env["GEMINI_CLI_HOME"] != want {
		t.Fatalf("GEMINI_CLI_HOME = %q, want %q", env["GEMINI_CLI_HOME"], want)
	}
	if env["CODEX_HOME"] == "" || len(env) != 2 {
		t.Fatalf("env = %v, want both CODEX_HOME and GEMINI_CLI_HOME", env)
	}
	if got := inventoryJobEnv(false); got != nil {
		t.Fatalf("system mode env = %v, want none", got)
	}
}

// Claude Code uses a trimmed, non-empty CLAUDE_CONFIG_DIR as is, so the job gets its absolute form
// whenever it is set. It is recorded beside CODEX_HOME, not instead of it.
func TestInventoryJobEnvRecordsClaudeConfigDirForUserInstalls(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(work, "codex-home")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLINE_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("CLAUDE_CONFIG_DIR", " claude-alt ")

	env := inventoryJobEnv(true)
	if want := filepath.Join(cwd, "claude-alt"); env["CLAUDE_CONFIG_DIR"] != want {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", env["CLAUDE_CONFIG_DIR"], want)
	}
	if env["CODEX_HOME"] == "" || len(env) != 2 {
		t.Fatalf("env = %v, want both CODEX_HOME and CLAUDE_CONFIG_DIR", env)
	}
	if got := inventoryJobEnv(false); got != nil {
		t.Fatalf("system mode env = %v, want none", got)
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

// The package-helper forwarders are system LaunchDaemons. A user-mode uninstall has no business
// with them, and must not reach for /Library.
func TestUserUninstallLeavesScriptForwardersAlone(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	old := removeScriptForwarders
	called := false
	removeScriptForwarders = func(bool) error { called = true; return nil }
	t.Cleanup(func() { removeScriptForwarders = old })
	if err := Uninstall(UninstallOptions{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a user-mode uninstall removed the system S3/GCS/Falcon forwarders")
	}
}

// Repair tears the endpoint down and installs it again, and Install never recreates the S3, GCS or
// Falcon forwarders. If the teardown removed them, `beacon endpoint repair --system` and the Fleet
// and Jamf repair scripts would silently stop a customer's forwarding.
func TestRepairTeardownKeepsScriptForwarders(t *testing.T) {
	opts := repairUninstallOptions(InstallOptions{UserMode: false})
	if !opts.KeepForwarders {
		t.Fatal("repair's uninstall step must keep the package-helper forwarders")
	}
	if !opts.KeepConfig || !opts.KeepLogs || !opts.KeepUpdater {
		t.Fatalf("repair's uninstall step changed what it keeps: %+v", opts)
	}
}

// The scheduled job starts without the installing shell's environment, so every other variable the
// inventory scan follows to a runtime's user directory is recorded too. A relative path is made
// absolute against the install's working directory; an absolute or `~` path, and a name (Oh My
// Pi's config directory and profile), are recorded exactly as set, since the job resolves them
// against the same HOME. A variable set to an empty value is recorded, because OMP_PROFILE and
// OMO's overrides read set-but-empty differently from unset.
func TestInventoryJobEnvRecordsTheOtherInventoryRootVariables(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CODEX_HOME", "HERMES_HOME", "HERMES_DATA_DIR_SUFFIX", "CLINE_DIR", "GEMINI_CLI_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(name, "")
	}
	set := map[string]string{
		"KIRO_HOME":                    " kiro-dir ",
		"KIMI_CODE_HOME":               "/abs/kimi",
		"PI_CODING_AGENT_DIR":          "~/pi-alt",
		"PRIME_AGENT_CODING_AGENT_DIR": "~",
		"OMO_CODING_AGENT_DIR":         "",
		"PI_CONFIG_DIR":                ".omp-alt",
		"OMP_PROFILE":                  "",
		"PI_PROFILE":                   "work",
	}
	for name, value := range set {
		t.Setenv(name, value)
	}
	want := map[string]string{
		"KIRO_HOME":                    filepath.Join(cwd, "kiro-dir"),
		"KIMI_CODE_HOME":               "/abs/kimi",
		"PI_CODING_AGENT_DIR":          "~/pi-alt",
		"PRIME_AGENT_CODING_AGENT_DIR": "~",
		"OMO_CODING_AGENT_DIR":         "",
		"PI_CONFIG_DIR":                ".omp-alt",
		"OMP_PROFILE":                  "",
		"PI_PROFILE":                   "work",
	}
	if runtime.GOOS == "windows" {
		// Not absolute without a drive letter, so it is resolved like any relative path.
		abs, err := filepath.Abs("/abs/kimi")
		if err != nil {
			t.Fatal(err)
		}
		want["KIMI_CODE_HOME"] = abs
	}
	if env := inventoryJobEnv(true); !reflect.DeepEqual(env, want) {
		t.Fatalf("user mode env = %v, want %v", env, want)
	}
	if got := inventoryJobEnv(false); got != nil {
		t.Fatalf("system mode env = %v, want none", got)
	}
}
