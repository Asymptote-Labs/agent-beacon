package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// `beacon endpoint sessions` holds the scheduled session-store sync: the same sweep an install
// runs once (every session-store collector, the last 30 days, a size budget, cursors so each run
// reads only what is new), run by the `com.beacon.endpoint.session-sync` job when
// `session_sync.enabled` is on. See docs/cli/endpoint-install.mdx#scheduled-session-sync.
var endpointSessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Read agent runtimes' local session stores into the runtime log",
}

var endpointSessionsSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Read new records from every agent runtime's session store into the runtime log",
	Long: "Runs every session-store collector once (Claude Code, Codex CLI, Cursor, OpenCode and the rest), " +
		"reading only what each has not read yet, from sessions active in the last 30 days and within a size budget. " +
		"A system endpoint reads the logged-in console user's stores, confined to files that user owns. " +
		"The scheduled session sync job runs this every 15 minutes when it is turned on with " +
		"`beacon endpoint install|repair --session-sync`.",
	SilenceUsage: true,
	RunE:         runEndpointSessionsSync,
}

var endpointSessionsInstallDaemonCmd = &cobra.Command{
	Use:          "install-daemon",
	Short:        "Internal: reconcile the scheduled session sync job with the endpoint config",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runSessionsInstallDaemon,
}

var endpointSessionsEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Turn the scheduled session sync on for the installed endpoint",
	Long: "Records `session_sync.enabled` in the endpoint config and installs and loads the scheduled session sync job. " +
		"Every other setting is left as it is: unlike `beacon endpoint repair --session-sync`, it does not rebuild the config, " +
		"collector or hooks, so it cannot reset harnesses or ports or drop a HEC destination.",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runSessionsSet(cmd, true) },
}

var endpointSessionsDisableCmd = &cobra.Command{
	Use:          "disable",
	Short:        "Turn the scheduled session sync off and remove its job",
	Long:         "Records `session_sync.enabled: false` in the endpoint config and unloads and removes the scheduled session sync job, leaving every other setting as it is.",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runSessionsSet(cmd, false) },
}

var endpointSessionsOpts struct {
	scheduled bool
}

func init() {
	endpointCmd.AddCommand(endpointSessionsCmd)
	endpointSessionsCmd.AddCommand(endpointSessionsSyncCmd)
	endpointSessionsCmd.AddCommand(endpointSessionsInstallDaemonCmd)
	endpointSessionsCmd.AddCommand(endpointSessionsEnableCmd)
	endpointSessionsCmd.AddCommand(endpointSessionsDisableCmd)
	for _, c := range []*cobra.Command{endpointSessionsSyncCmd, endpointSessionsInstallDaemonCmd, endpointSessionsEnableCmd, endpointSessionsDisableCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}
	endpointSessionsSyncCmd.Flags().BoolVar(&endpointSessionsOpts.scheduled, "scheduled", false, "Run as the scheduled job (set by the job's unit)")
	_ = endpointSessionsSyncCmd.Flags().MarkHidden("scheduled")
}

// runSessionSweep is the seam tests replace. The scheduled job does not wait for a sweep that is
// already running (the next run picks up what that one leaves); a manual run waits like install.
var runSessionSweep = func(logPath string, userMode, scheduled bool) endpointBackfillResult {
	if scheduled {
		return runEndpointBackfillLocked(logPath, userMode, 0, sweepScheduledSync)
	}
	return runEndpointBackfillLocked(logPath, userMode, endpointSweepLockWait, sweepManualSync)
}

func runEndpointSessionsSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("syncing session stores into the system log needs elevated privileges; %s", lifecycle.SystemPrivilegeHint())
	}
	cfg := loadConfigForMode(userMode, endpointOpts.logPath)
	if endpointSessionsOpts.scheduled {
		// The setting governs the job and what it does: a job that outlived the setting (config
		// edited to off without a repair, or an unload that failed) writes nothing. A manual run is
		// not gated, as with any runtime's own sync command.
		if !endpointconfig.SessionSyncEnabled(cfg) {
			fmt.Fprintln(cmd.OutOrStdout(), "Session sync is off in the endpoint config; nothing read. `beacon endpoint repair` removes this job.")
			return nil
		}
	}
	result := runSessionSweep(cfg.LogPath, userMode, endpointSessionsOpts.scheduled)
	reportSessionSweep(cmd.OutOrStdout(), "Session sync", result)
	// A runtime that failed is reported above and the rest still ran. The scheduled job exits
	// cleanly so the scheduler keeps it on its interval; a manual run says it failed.
	if !endpointSessionsOpts.scheduled {
		for _, runtime := range result.Runtimes {
			if runtime.Err != nil {
				return fmt.Errorf("%s: %w", runtime.Name, runtime.Err)
			}
		}
	}
	return nil
}

// reconcileSessionSyncJob is indirected so the command can be tested without a scheduler.
var reconcileSessionSyncJob = lifecycle.ReconcileSessionSyncJob

func runSessionsInstallDaemon(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("managing the system session sync job needs elevated privileges; %s", lifecycle.SystemPrivilegeHint())
	}
	result, err := reconcileSessionSyncJob(lifecycle.InventoryJobOptions{
		UserMode: userMode,
		Program:  lifecycle.DefaultInventoryJobProgram(userMode),
		LogPath:  endpointOpts.logPath,
		Load:     true,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), sessionSyncReconcileMessage(result))
	return nil
}

// setSessionSync is indirected so the commands can be tested without a scheduler.
var setSessionSync = lifecycle.SetSessionSync

func runSessionsSet(cmd *cobra.Command, enabled bool) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("changing the system session sync needs elevated privileges; %s", lifecycle.SystemPrivilegeHint())
	}
	result, err := setSessionSync(lifecycle.InventoryJobOptions{
		UserMode: userMode,
		Program:  lifecycle.DefaultInventoryJobProgram(userMode),
		LogPath:  endpointOpts.logPath,
		Load:     true,
	}, enabled)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), sessionSyncReconcileMessage(result))
	return nil
}

func sessionSyncReconcileMessage(result lifecycle.SessionSyncJobResult) string {
	switch {
	case !result.Enabled:
		return "Session sync is off (turn it on with `beacon endpoint sessions enable`); no scheduled job installed."
	case result.Skipped != "":
		return "Scheduled session sync job not installed: " + result.Skipped
	default:
		return "Scheduled session sync job installed (" + result.UnitPath + ")."
	}
}

func sessionSyncStatusLine(status lifecycle.SessionSyncStatus) string {
	if !status.Enabled {
		return "Session sync: off"
	}
	line := fmt.Sprintf("Session sync: every %s; job loaded=%t", status.Interval, status.Job.Loaded)
	if !status.Job.Loaded && status.Job.Message != "" {
		line += " (" + status.Job.Message + ")"
	}
	return line
}

// sessionSyncOption turns --session-sync / --no-session-sync into the install option: nil when
// neither was given, so repair and package upgrades keep the recorded setting.
func sessionSyncOption() (*bool, error) {
	switch {
	case endpointOpts.sessionSync && endpointOpts.noSessionSync:
		return nil, fmt.Errorf("--session-sync and --no-session-sync cannot be combined")
	case endpointOpts.sessionSync:
		on := true
		return &on, nil
	case endpointOpts.noSessionSync:
		off := false
		return &off, nil
	}
	return nil, nil
}
