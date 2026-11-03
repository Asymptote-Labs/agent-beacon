package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

var (
	// newSessionSyncJob and removeSessionSyncJob are the seams tests replace so Install,
	// Uninstall and Repair can be exercised without touching launchd or systemd.
	newSessionSyncJob = func(userMode bool, kind service.Kind) inventoryJobController {
		return service.SessionSyncManager{UserMode: userMode, Kind: kind, Env: sessionSyncJobEnv(userMode)}
	}
	removeSessionSyncJob = func(userMode bool) {
		job := newSessionSyncJob(userMode, service.KindAuto)
		_ = job.Unload()
		job.RemoveUnits()
	}
)

// sessionSyncJobPathEnv are the directory variables a session-store collector reads beyond the
// ones the inventory scan does. launchd and systemd do not pass a shell's exports to the job, so a
// user-mode job records each one set at install, as the inventory job does, or a store moved with
// it would be read at install and never again.
var sessionSyncJobPathEnv = []string{
	"COPILOT_HOME",      // GitHub Copilot CLI
	"XDG_DATA_HOME",     // OpenCode and Devin on Linux
	"HARNESS2_RUNS_DIR", // Harness²
}

// sessionSyncJobNameEnv are read as names, not paths, and recorded exactly as set: OPENCODE_DB is
// resolved against OpenCode's data directory when relative.
var sessionSyncJobNameEnv = []string{"OPENCODE_DB"}

// sessionSyncJobEnv is the inventory job's environment plus the variables only a session-store
// collector reads. System mode records nothing, like inventoryJobEnv: the process's environment is
// root's, not the console user's.
func sessionSyncJobEnv(userMode bool) map[string]string {
	if !userMode {
		return nil
	}
	env := inventoryJobEnv(true)
	if env == nil {
		env = map[string]string{}
	}
	for _, name := range sessionSyncJobPathEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = inventoryJobPathValue(value)
		}
	}
	for _, name := range sessionSyncJobNameEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// SessionSyncJobResult reports what ReconcileSessionSyncJob did.
type SessionSyncJobResult struct {
	Enabled  bool   `json:"enabled"`
	UnitPath string `json:"unit_path,omitempty"`
	Loaded   bool   `json:"loaded"`
	Removed  bool   `json:"removed"`
	// Skipped names why no job was installed on a host without a scheduler.
	Skipped string `json:"skipped,omitempty"`
}

// ReconcileSessionSyncJob makes the scheduled session sync job match the endpoint config for the
// mode: installed and loaded when `session_sync.enabled` is true, removed otherwise (the default).
// It runs on every install, repair and package upgrade, so turning the setting on or off with
// `--session-sync`/`--no-session-sync` takes effect in the same command.
func ReconcileSessionSyncJob(opts InventoryJobOptions) (SessionSyncJobResult, error) {
	return reconcileSessionSyncJobWith(newSessionSyncJob(opts.UserMode, opts.Kind), opts)
}

func reconcileSessionSyncJobWith(job inventoryJobController, opts InventoryJobOptions) (SessionSyncJobResult, error) {
	result := SessionSyncJobResult{Enabled: sessionSyncEnabledFromConfigFile(endpointconfig.ConfigPath(opts.UserMode))}
	if !result.Enabled {
		// Removing is safe with or without a scheduler, and it is what turning the setting off
		// has to do wherever a job was installed before.
		if job.Supported() {
			_ = job.Unload()
		}
		job.RemoveUnits()
		result.Removed = true
		return result, nil
	}
	if !job.Supported() {
		result.Skipped = job.UnsupportedReason()
		return result, nil
	}
	path, err := job.WriteUnit(opts.Program, opts.LogPath)
	if err != nil {
		return result, err
	}
	result.UnitPath = path
	if opts.Load {
		if err := job.Load(); err != nil {
			return result, err
		}
		result.Loaded = true
	}
	return result, nil
}

// SetSessionSync turns the scheduled session sync on or off on an installed endpoint. A repair
// rebuilds the config from its flags, so one run without the flags an endpoint was set up with
// resets its harnesses and ports and drops its HEC destinations; this records only
// `session_sync.enabled` in config.json, leaves every other key as it is, and reconciles the job.
func SetSessionSync(opts InventoryJobOptions, enabled bool) (SessionSyncJobResult, error) {
	path := endpointconfig.ConfigPath(opts.UserMode)
	if err := setConfigFileKey(opts.UserMode, path, "session_sync", endpointconfig.SessionSync{Enabled: enabled}); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SessionSyncJobResult{}, fmt.Errorf("no endpoint is installed here (%s): install it first; `beacon endpoint install --session-sync` turns the sync on with it", path)
		}
		return SessionSyncJobResult{}, err
	}
	if opts.LogPath == "" {
		opts.LogPath = loadOrDefault(opts.UserMode, "").LogPath
	}
	return ReconcileSessionSyncJob(opts)
}

// sessionSyncEnabledFromConfigFile reads `session_sync.enabled` from the config on disk. A
// missing or unreadable config means off: the job is opt-in.
func sessionSyncEnabledFromConfigFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var partial struct {
		SessionSync *endpointconfig.SessionSync `json:"session_sync"`
	}
	if err := json.Unmarshal(data, &partial); err != nil || partial.SessionSync == nil {
		return false
	}
	return partial.SessionSync.Enabled
}

// SessionSyncStatus is the status view of the scheduled session sync job.
type SessionSyncStatus struct {
	Enabled  bool           `json:"enabled"`
	Interval string         `json:"interval"`
	Job      service.Status `json:"job"`
}

func sessionSyncStatus(requestedUserMode bool, logPath string) SessionSyncStatus {
	cfg := loadOrDefault(requestedUserMode, logPath)
	return SessionSyncStatus{
		Enabled:  endpointconfig.SessionSyncEnabled(cfg),
		Interval: service.SessionSyncInterval().String(),
		Job:      newSessionSyncJob(requestedUserMode, service.KindAuto).Status(),
	}
}
