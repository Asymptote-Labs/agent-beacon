package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// reconcileInventoryJob is indirected so the command can be tested without a scheduler.
var reconcileInventoryJob = lifecycle.ReconcileInventoryJob

// endpointInventoryInstallDaemonCmd is invoked by the package postinstall (and available to
// operators) to make the scheduled inventory job match the endpoint config: installed and
// loaded unless `inventory_heartbeat.enabled` is false. Loading fires a heartbeat at once.
var endpointInventoryInstallDaemonCmd = &cobra.Command{
	Use:          "install-daemon",
	Short:        "Internal: reconcile the scheduled inventory job with the endpoint config",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runInventoryInstallDaemon,
}

func runInventoryInstallDaemon(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("managing the system inventory job needs elevated privileges; %s", lifecycle.SystemPrivilegeHint())
	}
	result, err := reconcileInventoryJob(lifecycle.InventoryJobOptions{
		UserMode: userMode,
		Program:  lifecycle.DefaultInventoryJobProgram(userMode),
		LogPath:  endpointOpts.logPath,
		Load:     true,
	})
	if err != nil {
		return err
	}
	fmt.Println(inventoryReconcileMessage(result))
	return nil
}

func inventoryReconcileMessage(result lifecycle.InventoryJobResult) string {
	switch {
	case result.Skipped != "":
		return "Scheduled inventory job not installed: " + result.Skipped
	case result.Removed:
		return "Inventory heartbeat is disabled in config; scheduled job removed."
	default:
		return "Scheduled inventory job installed (" + result.UnitPath + ")."
	}
}

func inventoryHeartbeatStatusLine(status lifecycle.InventoryHeartbeatStatus) string {
	if !status.Enabled {
		return "Inventory heartbeat: disabled in config"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Inventory heartbeat: scheduled every %s; job loaded=%t running=%t", status.Interval, status.Job.Loaded, status.Job.Running)
	if !status.Job.Loaded && status.Job.Message != "" {
		fmt.Fprintf(&b, " (%s)", status.Job.Message)
	}
	if status.LastEmittedAt != "" {
		fmt.Fprintf(&b, "; last emitted %s", status.LastEmittedAt)
	} else {
		b.WriteString("; last emitted never")
	}
	return b.String()
}
