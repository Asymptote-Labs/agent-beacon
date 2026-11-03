package service

import (
	"strings"
	"time"
)

const (
	// InventoryLabel is the launchd label of the scheduled inventory heartbeat job. It is a
	// LaunchAgent for a user-mode endpoint and a LaunchDaemon for a system-mode one.
	InventoryLabel = "com.beacon.endpoint.inventory"
	// InventoryServiceUnit and InventoryTimerUnit are the systemd equivalents. systemd splits
	// "what to run" from "when to run it", so a scheduled job needs both.
	InventoryServiceUnit = "beacon-inventory.service"
	InventoryTimerUnit   = "beacon-inventory.timer"

	// InventoryIntervalEnv overrides the heartbeat interval, in seconds. It exists for tests and
	// smoke checks; a fleet should run the default.
	InventoryIntervalEnv     = "BEACON_INVENTORY_INTERVAL_SECONDS"
	defaultInventoryInterval = 6 * time.Hour
)

// InventoryInterval is how often the scheduled heartbeat runs. The plist, the systemd timer and
// the status line all read it here so they can never disagree.
func InventoryInterval() time.Duration {
	return intervalFromEnv(InventoryIntervalEnv, defaultInventoryInterval)
}

var inventorySpec = jobSpec{
	label:       InventoryLabel,
	serviceUnit: InventoryServiceUnit,
	timerUnit:   InventoryTimerUnit,
	description: "inventory heartbeat",
	name:        "scheduled inventory job",
	docs:        "https://docs.asymptotelabs.ai/cli/endpoint-inventory",
	manualHint:  "Run `beacon endpoint inventory heartbeat` from your own scheduler.",
	interval:    InventoryInterval,
	args:        inventoryJobArgs,
	runAtLoad:   true,
}

// InventoryManager manages the scheduled job that writes inventory heartbeats, in both user and
// system modes, because a per-user Beacon install inventories that user's runtimes. See
// scheduledJob for how the job is installed, loaded and reported.
type InventoryManager struct {
	UserMode bool
	// Kind selects the backend; the zero value auto-detects.
	Kind Kind
	// Env is set in the job's environment. launchd and systemd start the job without the
	// installing user's shell environment, so anything the inventory scan reads from it has to
	// be written into the unit.
	Env map[string]string
}

func (m InventoryManager) job() scheduledJob {
	return scheduledJob{spec: inventorySpec, userMode: m.UserMode, kind: m.Kind, env: m.Env}
}

func (m InventoryManager) Supported() bool           { return m.job().Supported() }
func (m InventoryManager) UnsupportedReason() string { return m.job().UnsupportedReason() }
func (m InventoryManager) Label() string             { return m.job().Label() }
func (m InventoryManager) UnitPath() (string, error) { return m.job().UnitPath() }
func (m InventoryManager) UnitPaths() []string       { return m.job().UnitPaths() }
func (m InventoryManager) RemoveUnits()              { m.job().RemoveUnits() }
func (m InventoryManager) Load() error               { return m.job().Load() }
func (m InventoryManager) Unload() error             { return m.job().Unload() }
func (m InventoryManager) Status() Status            { return m.job().Status() }
func (m InventoryManager) WriteUnit(program, logPath string) (string, error) {
	return m.job().WriteUnit(program, logPath)
}

// inventoryJobArgs is the argument vector the scheduler invokes, after the program itself.
func inventoryJobArgs(userMode bool, logPath string) []string {
	return modeJobArgs([]string{"endpoint", "inventory", "heartbeat", "--scheduled"}, userMode, logPath)
}

// modeJobArgs appends the mode and log-path flags every scheduled job takes.
func modeJobArgs(args []string, userMode bool, logPath string) []string {
	if userMode {
		args = append(args, "--user")
	} else {
		args = append(args, "--system")
	}
	if strings.TrimSpace(logPath) != "" {
		args = append(args, "--log-path", logPath)
	}
	return args
}

// The renderers under their inventory names, which the inventory tests pin.
func inventoryServiceUnit(program string, args []string, userMode bool, env map[string]string) string {
	return jobServiceUnit(inventorySpec, program, args, userMode, env)
}

func inventoryTimerUnit(interval time.Duration) string { return jobTimerUnit(inventorySpec, interval) }

func inventoryPlist(label, program string, args []string, interval time.Duration, stdoutPath, stderrPath string, env map[string]string) string {
	return jobPlist(label, program, args, interval, inventorySpec.runAtLoad, stdoutPath, stderrPath, env)
}
