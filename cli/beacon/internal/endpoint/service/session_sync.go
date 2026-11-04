package service

import "time"

const (
	// SessionSyncLabel is the launchd label of the scheduled session-store sync job. It is a
	// LaunchAgent for a user-mode endpoint and a LaunchDaemon for a system-mode one.
	SessionSyncLabel = "com.beacon.endpoint.session-sync"
	// SessionSyncServiceUnit and SessionSyncTimerUnit are the systemd equivalents.
	SessionSyncServiceUnit = "beacon-session-sync.service"
	SessionSyncTimerUnit   = "beacon-session-sync.timer"

	// SessionSyncIntervalEnv overrides the sync interval, in seconds. It exists for tests and
	// smoke checks; a fleet should run the default.
	SessionSyncIntervalEnv     = "BEACON_SESSION_SYNC_INTERVAL_SECONDS"
	defaultSessionSyncInterval = 15 * time.Minute
)

// SessionSyncInterval is how often the scheduled session sync runs.
func SessionSyncInterval() time.Duration {
	return intervalFromEnv(SessionSyncIntervalEnv, defaultSessionSyncInterval)
}

// The session sync does not run at load. An install that turns it on runs its own backfill right
// after loading the job, and a run fired at load would take that sweep's lock first, leaving the
// install to wait for it and then report nothing new; the first scheduled run comes one interval
// later and reads what has changed since.
var sessionSyncSpec = jobSpec{
	label:       SessionSyncLabel,
	serviceUnit: SessionSyncServiceUnit,
	timerUnit:   SessionSyncTimerUnit,
	description: "session-store sync",
	name:        "scheduled session sync job",
	docs:        "https://docs.asymptotelabs.ai/cli/endpoint-install#scheduled-session-sync",
	manualHint:  "Run `beacon endpoint sessions sync` from your own scheduler.",
	interval:    SessionSyncInterval,
	args:        sessionSyncJobArgs,
}

// SessionSyncManager manages the opt-in scheduled job that reads the agent runtimes' own session
// stores (Claude Code transcripts, Codex sessions and the rest) into the runtime log, the same
// sweep an install runs once. See scheduledJob for how the job is installed, loaded and reported.
type SessionSyncManager struct {
	UserMode bool
	// Kind selects the backend; the zero value auto-detects.
	Kind Kind
	// Env is set in the job's environment: the store-locating variables a session-store collector
	// reads, which launchd and systemd do not pass from the installing user's shell.
	Env map[string]string
}

func (m SessionSyncManager) job() scheduledJob {
	return scheduledJob{spec: sessionSyncSpec, userMode: m.UserMode, kind: m.Kind, env: m.Env}
}

func (m SessionSyncManager) Supported() bool           { return m.job().Supported() }
func (m SessionSyncManager) UnsupportedReason() string { return m.job().UnsupportedReason() }
func (m SessionSyncManager) Label() string             { return m.job().Label() }
func (m SessionSyncManager) UnitPath() (string, error) { return m.job().UnitPath() }
func (m SessionSyncManager) UnitPaths() []string       { return m.job().UnitPaths() }
func (m SessionSyncManager) RemoveUnits()              { m.job().RemoveUnits() }
func (m SessionSyncManager) Load() error               { return m.job().Load() }
func (m SessionSyncManager) Unload() error             { return m.job().Unload() }
func (m SessionSyncManager) Status() Status            { return m.job().Status() }
func (m SessionSyncManager) WriteUnit(program, logPath string) (string, error) {
	return m.job().WriteUnit(program, logPath)
}

// sessionSyncJobArgs is the argument vector the scheduler invokes, after the program itself.
func sessionSyncJobArgs(userMode bool, logPath string) []string {
	return modeJobArgs([]string{"endpoint", "sessions", "sync", "--scheduled"}, userMode, logPath)
}
