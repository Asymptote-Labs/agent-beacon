package cmd

import (
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// Swapped by tests.
var (
	telemetryInit                = telemetry.Init
	telemetryEnabled             = telemetry.Enabled
	telemetryIdentifyIfAnonymous = telemetry.IdentifyIfAnonymous
)

// startTelemetry is the root command's PersistentPreRun: it turns telemetry on for the command
// about to run. Scheduled jobs run through here too, so the events they send work there, but no
// command sends an event just for having run.
func startTelemetry(cmd *cobra.Command, _ []string) {
	if !telemetryApplies(cmd) {
		return
	}
	stderr := cmd.ErrOrStderr()
	telemetryInit(telemetry.Options{
		Version:     version.GetVersion(),
		InstallMode: telemetryInstallMode(cmd),
		Stderr:      stderr,
		Interactive: telemetryInteractive(cmd, stderr),
	})
	identifyStoredSignIn()
}

// identifyStoredSignIn ties an anonymous machine to the account already signed in on it, so a
// person who signed in with an earlier CLI is known to every path that never shows the wizard
// (agent, script and CI installs, the scheduled jobs' trace events). It runs once per machine:
// telemetry remembers the person, and a machine a sign-in or an install email already named is
// left as it is. With telemetry off the session file is not even read.
func identifyStoredSignIn() {
	if !telemetryEnabled() {
		return
	}
	status := onboardingAccountInspect(onboardingClock())
	if status.SignedIn && !status.Expired && status.User.Email != "" {
		telemetryIdentifyIfAnonymous(status.User.Email, status.User.ID)
	}
}

// telemetryInteractive reports whether a person is reading this command's stderr, which is when the
// first-run notice may be shown. Hidden commands are internal children and scheduled jobs (the
// inventory job, the console-user runs of the package scripts, `version refresh`), whose stderr
// goes to a log or a buffer nobody reads even when it happens to be a terminal.
var telemetryInteractive = func(cmd *cobra.Command, stderr io.Writer) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Hidden {
			return false
		}
	}
	file, ok := stderr.(*os.File)
	return ok && isTerminal(file) && !strings.EqualFold(os.Getenv("TERM"), "dumb")
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
