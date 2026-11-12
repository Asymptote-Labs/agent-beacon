package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// Swapped by tests.
var telemetryInit = telemetry.Init

// startTelemetry is the root command's PersistentPreRun: it turns telemetry on for the command
// about to run. Scheduled jobs run through here too, so the events they send work there, but no
// command sends an event just for having run.
func startTelemetry(cmd *cobra.Command, _ []string) {
	if !telemetryApplies(cmd) {
		return
	}
	telemetryInit(telemetry.Options{
		Version:     version.GetVersion(),
		InstallMode: telemetryInstallMode(cmd),
		Stderr:      cmd.ErrOrStderr(),
	})
}

// telemetryApplies keeps telemetry out of the commands that are not a person running Beacon:
// shell completion, which runs on every Tab, and the entry points installed git hooks call on every
// commit, which stay off the network like the runtime hooks.
func telemetryApplies(cmd *cobra.Command) bool {
	if strings.HasPrefix(cmd.Name(), "__") || cmd.Name() == "completion" {
		return false
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c == gitHookCmd {
			return false
		}
	}
	return true
}

// telemetryInstallMode is system for a --system command or one run as root, else user.
func telemetryInstallMode(cmd *cobra.Command) string {
	if flag := cmd.Flags().Lookup("system"); flag != nil && flag.Value.String() == "true" {
		return telemetry.InstallModeSystem
	}
	if onboardingIsRoot() {
		return telemetry.InstallModeSystem
	}
	return telemetry.InstallModeUser
}

// exitProcess sends queued telemetry, waiting at most telemetry.FlushTimeout, and exits. Every
// os.Exit in this package goes through it, because an exit skips the flush in Execute.
func exitProcess(code int) {
	telemetry.Close()
	os.Exit(code)
}
