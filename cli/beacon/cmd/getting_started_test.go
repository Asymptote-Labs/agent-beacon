package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	endpointcollector "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/spf13/cobra"
)

func TestGettingStartedNonInteractivePrintsActionablePlan(t *testing.T) {
	restore := stubGettingStarted(t)
	defer restore()
	gettingStartedIsTTY = func() bool { return false }

	cmd, stdout, stderr := gettingStartedTestCommand("")
	if err := runGettingStarted(cmd, nil); err != nil {
		t.Fatalf("runGettingStarted: %v", err)
	}
	got := stdout.String()
	for _, want := range []string{
		"Collector service is running",
		"OTLP receivers are ready",
		"opencode",
		"beacon endpoint hooks install --harness opencode",
		"beacon endpoint dashboard --open",
		"sudo loginctl enable-linger beacon-user",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestMaybeOfferGettingStartedRunsOnDefaultYes(t *testing.T) {
	restore := stubGettingStarted(t)
	defer restore()
	gettingStartedIsTTY = func() bool { return true }
	gettingStartedOpts.noHooks = true
	gettingStartedOpts.noDashboard = true

	cmd, stdout, _ := gettingStartedTestCommand("\n")
	if err := maybeOfferGettingStarted(cmd); err != nil {
		t.Fatalf("maybeOfferGettingStarted: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Run guided setup now? [Y/n]") || !strings.Contains(got, "Beacon getting started") {
		t.Fatalf("default yes did not launch guided setup: %s", got)
	}
}

func TestGettingStartedAcceptsDefaultsInstallsHooksAndStartsDashboard(t *testing.T) {
	restore := stubGettingStarted(t)
	defer restore()
	gettingStartedIsTTY = func() bool { return true }
	var installed []string
	gettingStartedInstall = func(target string) error {
		installed = append(installed, target)
		return nil
	}
	dashboardStarted := false
	gettingStartedDashboard = func(cmd *cobra.Command) error {
		dashboardStarted = true
		return nil
	}

	cmd, stdout, _ := gettingStartedTestCommand("\n\n")
	if err := runGettingStarted(cmd, nil); err != nil {
		t.Fatalf("runGettingStarted: %v", err)
	}
	if len(installed) != 1 || installed[0] != "opencode" {
		t.Fatalf("installed = %#v, want opencode", installed)
	}
	if !dashboardStarted {
		t.Fatal("dashboard was not started after accepting the default")
	}
	if !strings.Contains(stdout.String(), "Install recommended runtime hooks now? [Y/n]") ||
		!strings.Contains(stdout.String(), "Open the local dashboard now? [Y/n]") {
		t.Fatalf("interactive prompts missing:\n%s", stdout.String())
	}
}

func TestGettingStartedReportsHookFailureWithExactFix(t *testing.T) {
	restore := stubGettingStarted(t)
	defer restore()
	gettingStartedOpts.yes = true
	gettingStartedOpts.noDashboard = true
	gettingStartedInstall = func(target string) error { return errors.New("config is read-only") }

	cmd, _, stderr := gettingStartedTestCommand("")
	err := runGettingStarted(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unresolved problem") {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{"config is read-only", "beacon endpoint hooks install --harness opencode"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q: %s", want, stderr.String())
		}
	}
}

func TestMaybeOfferGettingStartedStaysNonInteractiveWithoutTTY(t *testing.T) {
	restore := stubGettingStarted(t)
	defer restore()
	gettingStartedIsTTY = func() bool { return false }

	cmd, stdout, _ := gettingStartedTestCommand("")
	if err := maybeOfferGettingStarted(cmd); err != nil {
		t.Fatalf("maybeOfferGettingStarted: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Next: beacon getting-started") || strings.Contains(got, "Run guided setup now?") {
		t.Fatalf("unexpected noninteractive output: %s", got)
	}
}

func stubGettingStarted(t *testing.T) func() {
	t.Helper()
	originalDiscover := gettingStartedDiscover
	originalStatus := gettingStartedStatus
	originalInstall := gettingStartedInstall
	originalDashboard := gettingStartedDashboard
	originalTTY := gettingStartedIsTTY
	originalOpts := gettingStartedOpts
	originalEndpointOpts := endpointOpts
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	gettingStartedOpts = gettingStartedOptions{}
	gettingStartedDiscover = func() []harness.Harness {
		return []harness.Harness{
			{Name: "codex_cli", DisplayName: "Codex CLI", Detected: true, TelemetryStatus: harness.TelemetryEnabled},
			{Name: "opencode", DisplayName: "opencode", Detected: true, TelemetryStatus: harness.TelemetryMissing},
		}
	}
	gettingStartedStatus = func(bool, string) lifecycle.Status {
		return lifecycle.Status{
			LogPath:   "/tmp/runtime.jsonl",
			LastEvent: "present",
			Service:   service.Status{Loaded: true, Running: true},
			Collector: endpointcollector.Status{GRPCReady: true, HTTPReady: true},
			Diagnostics: []diagnostics.Check{{
				Name:    "systemd_user_linger",
				Status:  diagnostics.StatusWarn,
				Message: "linger is disabled",
				Action:  "sudo loginctl enable-linger beacon-user",
			}},
		}
	}
	gettingStartedInstall = func(string) error { return nil }
	gettingStartedDashboard = func(*cobra.Command) error { return nil }
	gettingStartedIsTTY = func() bool { return false }
	return func() {
		gettingStartedDiscover = originalDiscover
		gettingStartedStatus = originalStatus
		gettingStartedInstall = originalInstall
		gettingStartedDashboard = originalDashboard
		gettingStartedIsTTY = originalTTY
		gettingStartedOpts = originalOpts
		endpointOpts = originalEndpointOpts
	}
}

func gettingStartedTestCommand(input string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "test"}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, stdout, stderr
}
