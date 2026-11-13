package cmd

import (
	"testing"

	"github.com/spf13/cobra"

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
