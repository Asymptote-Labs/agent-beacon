package cmd

import (
	"slices"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpserver"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
)

// recordTelemetryInit swaps the telemetry start for one that records its options.
func recordTelemetryInit(t *testing.T) *[]telemetry.Options {
	t.Helper()
	var calls []telemetry.Options
	old := telemetryInit
	telemetryInit = func(opts telemetry.Options) { calls = append(calls, opts) }
	t.Cleanup(func() { telemetryInit = old })
	return &calls
}

func stubRoot(t *testing.T, root bool) {
	t.Helper()
	old := onboardingIsRoot
	onboardingIsRoot = func() bool { return root }
	t.Cleanup(func() { onboardingIsRoot = old })
}

func TestACommandStartsTelemetryBeforeItRuns(t *testing.T) {
	stubRoot(t, false)
	calls := recordTelemetryInit(t)
	if _, err := runBeaconCLI(t, "version"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("telemetry started %d times, want 1", len(*calls))
	}
	opts := (*calls)[0]
	if opts.InstallMode != telemetry.InstallModeUser || opts.Stderr == nil || opts.Version == "" {
		t.Fatalf("options = %+v", opts)
	}
}

func TestTelemetryInstallMode(t *testing.T) {
	command := func(system bool) *cobra.Command {
		c := &cobra.Command{Use: "x"}
		c.Flags().Bool("system", false, "")
		if system {
			_ = c.Flags().Set("system", "true")
		}
		return c
	}
	stubRoot(t, false)
	if got := telemetryInstallMode(command(false)); got != telemetry.InstallModeUser {
		t.Errorf("user command = %q", got)
	}
	if got := telemetryInstallMode(command(true)); got != telemetry.InstallModeSystem {
		t.Errorf("--system command = %q", got)
	}
	if got := telemetryInstallMode(&cobra.Command{Use: "y"}); got != telemetry.InstallModeUser {
		t.Errorf("command without --system = %q", got)
	}
	stubRoot(t, true)
	if got := telemetryInstallMode(command(false)); got != telemetry.InstallModeSystem {
		t.Errorf("command run as root = %q", got)
	}
}

func TestTelemetryStaysOutOfCompletionAndGitHooks(t *testing.T) {
	if telemetryApplies(gitHookPostCommitCmd) || telemetryApplies(gitHookPostRewriteCmd) {
		t.Error("telemetry starts in a git hook entry point")
	}
	if telemetryApplies(completionCmd) {
		t.Error("telemetry starts in shell completion")
	}
	if telemetryApplies(&cobra.Command{Use: cobra.ShellCompRequestCmd}) {
		t.Error("telemetry starts in __complete")
	}
	if !telemetryApplies(endpointInstallCmd) {
		t.Error("telemetry does not start for endpoint install")
	}
}

// The first-run notice is for whoever ran the command, a person or an agent, terminal or not; never
// for a hidden command, a --scheduled job or an internal child, which run on their own.
func TestTelemetryNoticeIsForWhoeverRanTheCommand(t *testing.T) {
	t.Setenv(telemetry.EnvInternalChild, "")
	t.Setenv("SUDO_USER", "")
	if !telemetryShowsNotice(endpointInstallCmd) {
		t.Error("an install, run by a person or an agent, may not show the notice")
	}
	for _, hidden := range []*cobra.Command{endpointInventoryHeartbeatCmd, endpointInventoryResolvePackagesCmd, endpointUserConfigApplyNativeCmd} {
		if telemetryShowsNotice(hidden) {
			t.Errorf("%s may show the notice", hidden.CommandPath())
		}
	}

	scheduled := endpointSessionsSyncCmd.Flags().Lookup("scheduled")
	if scheduled == nil {
		t.Fatal("sessions sync has no --scheduled flag")
	}
	if err := scheduled.Value.Set("true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scheduled.Value.Set("false") })
	if telemetryShowsNotice(endpointSessionsSyncCmd) {
		t.Error("a --scheduled job may show the notice")
	}
	_ = scheduled.Value.Set("false")
	if !telemetryShowsNotice(endpointSessionsSyncCmd) {
		t.Error("a manual sessions sync may not show the notice")
	}

	t.Setenv(telemetry.EnvInternalChild, "1")
	if telemetryShowsNotice(endpointInstallCmd) {
		t.Error("an internal child may show the notice")
	}
}

func stubStderrTerminal(t *testing.T, terminal bool) {
	t.Helper()
	old := telemetryStderrIsTerminal
	telemetryStderrIsTerminal = func() bool { return terminal }
	t.Cleanup(func() { telemetryStderrIsTerminal = old })
}

func TestTelemetryNoticeSkipsSudoScriptsWithoutATerminal(t *testing.T) {
	t.Setenv(telemetry.EnvInternalChild, "")
	t.Setenv("SUDO_USER", "root")

	// A Jamf or fleet script running `sudo -u "$CONSOLE_USER" beacon endpoint hooks install`.
	stubStderrTerminal(t, false)
	if telemetryShowsNotice(endpointHooksInstallCmd) {
		t.Error("a script's sudo run as the console user may show the notice into its log")
	}

	// A person typing sudo -u at a terminal reads it.
	stubStderrTerminal(t, true)
	if !telemetryShowsNotice(endpointHooksInstallCmd) {
		t.Error("a sudo run at a terminal may not show the notice")
	}

	// Without sudo, an agent's or script's run shows it on stderr.
	t.Setenv("SUDO_USER", "")
	stubStderrTerminal(t, false)
	if !telemetryShowsNotice(endpointInstallCmd) {
		t.Error("an agent's install without a terminal may not show the notice")
	}
}

func TestTelemetryNoticeSkipsTheStdioMCPServer(t *testing.T) {
	t.Setenv(telemetry.EnvInternalChild, "")
	t.Setenv("SUDO_USER", "")
	old := mcpOpts.transport
	t.Cleanup(func() { mcpOpts.transport = old })

	for _, transport := range []string{"", mcpserver.TransportStdio} {
		mcpOpts.transport = transport
		if telemetryShowsNotice(mcpServeCmd) {
			t.Errorf("mcp serve on transport %q, started by a runtime, may show the notice", transport)
		}
	}
	mcpOpts.transport = mcpserver.TransportHTTP
	if !telemetryShowsNotice(mcpServeCmd) {
		t.Error("mcp serve --transport http, run by a person, may not show the notice")
	}
	if !telemetryShowsNotice(mcpDoctorCmd) {
		t.Error("mcp doctor may not show the notice")
	}
}

// stubStoredSignIn makes telemetry on or off, the stored account status, and records what
// identifyStoredSignIn reads and identifies.
func stubStoredSignIn(t *testing.T, enabled bool, status account.Status) (inspected *int, ids *[]identified) {
	t.Helper()
	inspected, ids = new(int), &[]identified{}
	oldEnabled, oldIdentify, oldInspect := telemetryEnabled, telemetryIdentifyIfAnonymous, onboardingAccountInspect
	telemetryEnabled = func() bool { return enabled }
	telemetryIdentifyIfAnonymous = func(email, userID string) { *ids = append(*ids, identified{email, userID}) }
	onboardingAccountInspect = func(time.Time) account.Status {
		*inspected++
		return status
	}
	t.Cleanup(func() {
		telemetryEnabled, telemetryIdentifyIfAnonymous, onboardingAccountInspect = oldEnabled, oldIdentify, oldInspect
	})
	return inspected, ids
}

// A person who signed in with an earlier CLI is identified by the first command that runs with
// telemetry on, whatever the install path, before the command sends anything.
func TestEveryCommandIdentifiesAStoredSignIn(t *testing.T) {
	stubRoot(t, false)
	recordTelemetryInit(t)
	user := account.User{ID: "usr_1", Email: "person@example.com"}
	for _, tc := range []struct {
		name          string
		enabled       bool
		status        account.Status
		wantInspected int
		want          []identified
	}{
		{name: "signed in", enabled: true, status: account.Status{SignedIn: true, User: user}, wantInspected: 1, want: []identified{{"person@example.com", "usr_1"}}},
		{name: "expired", enabled: true, status: account.Status{SignedIn: true, Expired: true, User: user}, wantInspected: 1},
		{name: "signed out", enabled: true, status: account.Status{}, wantInspected: 1},
		{name: "telemetry off", enabled: false, status: account.Status{SignedIn: true, User: user}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspected, ids := stubStoredSignIn(t, tc.enabled, tc.status)
			if _, err := runBeaconCLI(t, "version"); err != nil {
				t.Fatal(err)
			}
			if *inspected != tc.wantInspected || !slices.Equal(*ids, tc.want) {
				t.Fatalf("inspected %d times, identified %+v; want %d, %+v", *inspected, *ids, tc.wantInspected, tc.want)
			}
		})
	}
}

// Telemetry is not started for shell completion or the git hooks, so they read no session either.
func TestCommandsWithoutTelemetryDoNotReadTheSession(t *testing.T) {
	stubRoot(t, false)
	recordTelemetryInit(t)
	inspected, ids := stubStoredSignIn(t, true, account.Status{SignedIn: true, User: account.User{ID: "usr_1", Email: "person@example.com"}})
	startTelemetry(&cobra.Command{Use: "__complete"}, nil)
	if *inspected != 0 || len(*ids) != 0 {
		t.Fatalf("inspected %d times, identified %+v", *inspected, *ids)
	}
}

// login and logout replace or remove the stored session, so the session they start with does not
// name the machine: a sign-in as someone else stays the first identify.
func TestLoginAndLogoutDoNotIdentifyTheStoredSignIn(t *testing.T) {
	stubRoot(t, false)
	recordTelemetryInit(t)
	inspected, ids := stubStoredSignIn(t, true, account.Status{SignedIn: true, User: account.User{ID: "usr_1", Email: "person@example.com"}})
	for _, cmd := range []*cobra.Command{loginCmd, logoutCmd} {
		startTelemetry(cmd, nil)
	}
	if *inspected != 0 || len(*ids) != 0 {
		t.Fatalf("inspected %d times, identified %+v", *inspected, *ids)
	}
}
