package lifecycle

import (
	"encoding/json"
	"os"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

var (
	// newSessionSyncJob and removeSessionSyncJob are the seams tests replace so Install,
	// Uninstall and Repair can be exercised without touching launchd or systemd.
	newSessionSyncJob = func(userMode bool, kind service.Kind) inventoryJobController {
		// The session-store collectors resolve their stores from the same variables the
		// inventory scan does (CLAUDE_CONFIG_DIR, CODEX_HOME and the rest), so the job carries
		// the same environment.
		return service.SessionSyncManager{UserMode: userMode, Kind: kind, Env: inventoryJobEnv(userMode)}
	}
	removeSessionSyncJob = func(userMode bool) {
		job := newSessionSyncJob(userMode, service.KindAuto)
		_ = job.Unload()
		job.RemoveUnits()
	}
)

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
