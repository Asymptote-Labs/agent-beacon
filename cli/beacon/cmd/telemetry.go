package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpserver"
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
	telemetryInit(telemetry.Options{
		Version:     version.GetVersion(),
		InstallMode: telemetryInstallMode(cmd),
		Stderr:      cmd.ErrOrStderr(),
		ShowNotice:  telemetryShowsNotice(cmd),
	})
	// login and logout replace or remove the stored session, so it would name the wrong person:
	// a sign-in as someone else must stay the first identify.
	if cmd != loginCmd && cmd != logoutCmd {
		identifyStoredSignIn()
	}
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

// telemetryShowsNotice reports whether whoever ran this command reads its stderr, which is when the
// first-run notice may be shown. That includes a coding agent or a script, which run most installs
// without a terminal; the notice goes to stderr only, so stdout stays machine-readable. It excludes
// what runs on its own, whose stderr lands in a log nobody reads, because the notice is recorded as
// shown and would never be printed again:
//   - hidden commands (the inventory job, the console-user runs of the package scripts,
//     `version refresh`) and a --scheduled run (the session sync and updater jobs);
//   - a beacon child another beacon process started with telemetry.EnvInternalChild, whose output it
//     captures, and the MDM scripts that set it when they run beacon as the console user;
//   - a run without a terminal that sudo started as someone else (SUDO_USER is set), which is how
//     MDM and fleet scripts we do not ship reach the console user; a person typing sudo -u at a
//     terminal still sees it;
//   - `beacon mcp serve` on stdio, which a coding agent's runtime starts and whose stderr it keeps.
var telemetryShowsNotice = func(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Hidden {
			return false
		}
	}
	if flag := cmd.Flags().Lookup("scheduled"); flag != nil && flag.Value.String() == "true" {
		return false
	}
	if os.Getenv(telemetry.EnvInternalChild) != "" {
		return false
	}
	if os.Getenv("SUDO_USER") != "" && !telemetryStderrIsTerminal() {
		return false
	}
	if cmd == mcpServeCmd && normalizedTransport() == mcpserver.TransportStdio {
		return false
	}
	return true
}

// Swapped by tests.
var telemetryStderrIsTerminal = func() bool { return isTerminal(os.Stderr) }

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
