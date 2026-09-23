package cmd

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/spf13/cobra"
)

// recordEndpointLifecycle replaces the lifecycle install and repair calls with recorders, so
// a test can run the real `endpoint install` and `endpoint repair` commands and inspect exactly
// what they asked the lifecycle package to configure, without starting a collector.
func recordEndpointLifecycle(t *testing.T) *[]lifecycle.InstallOptions {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	t.Setenv("BEACON_ONBOARDING", "0")
	t.Setenv("BEACON_ONBOARDING_EMAIL", "")
	t.Setenv("BEACON_ONBOARDING_USAGE", "")
	oldOpts := endpointOpts
	oldInstall := endpointLifecycleInstall
	oldRepair := endpointLifecycleRepair
	t.Cleanup(func() {
		endpointOpts = oldOpts
		endpointLifecycleInstall = oldInstall
		endpointLifecycleRepair = oldRepair
	})
	endpointOpts.serviceKind = string(service.KindAuto)
	endpointOpts.dryRun = false
	endpointOpts.connect = false
	endpointOpts.noStart = true
	endpointOpts.logPath = ""
	calls := &[]lifecycle.InstallOptions{}
	record := func(opts lifecycle.InstallOptions) (lifecycle.InstallResult, error) {
		*calls = append(*calls, opts)
		return lifecycle.InstallResult{}, nil
	}
	endpointLifecycleInstall = record
	endpointLifecycleRepair = record
	return calls
}

func runRecordedEndpointCommand(t *testing.T, repair bool, harnessFlag string) lifecycle.InstallOptions {
	t.Helper()
	calls := recordEndpointLifecycle(t)
	endpointOpts.harnesses = harnessFlag
	run := runEndpointInstall
	name := "install"
	if repair {
		run = runEndpointRepair
		name = "repair"
	}
	cmd := &cobra.Command{Use: name}
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := run(cmd, nil); err != nil {
		t.Fatalf("endpoint %s --harness %q: %v", name, harnessFlag, err)
	}
	if len(*calls) != 1 {
		t.Fatalf("endpoint %s --harness %q made %d lifecycle calls, want 1", name, harnessFlag, len(*calls))
	}
	return (*calls)[0]
}

// assertNoOTLPHarnesses checks the lifecycle was told to configure no OTLP runtime.
//
// The distinction between nil and empty is the whole contract: lifecycle.InstallOptions treats
// nil Harnesses as "not specified" and falls back to the config default of Claude Code and Codex,
// which is how a hook-only install came to rewrite ~/.claude/settings.json and ~/.codex/config.toml
// (#640). Only a non-nil empty list means "configure none".
func assertNoOTLPHarnesses(t *testing.T, label string, got []string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: Harnesses is nil, which lifecycle reads as unspecified and replaces with claude,codex", label)
	}
	if len(got) != 0 {
		t.Fatalf("%s: Harnesses = %#v, want none", label, got)
	}
}

// #640: an explicit hook-only selection names only that hook integration. It must not also
// configure Claude Code's and Codex's OpenTelemetry settings behind the operator's back.
func TestEndpointInstallHookOnlyHarnessConfiguresNoOTLPRuntime(t *testing.T) {
	for _, flag := range []string{"omp", "cursor", "omp,cursor", "claude-hooks"} {
		opts := runRecordedEndpointCommand(t, false, flag)
		assertNoOTLPHarnesses(t, "install --harness "+flag, opts.Harnesses)
	}
}

// The same selection re-run through `endpoint repair` (and the MDM repair scripts built on it)
// must not reintroduce the writes on every run.
func TestEndpointRepairHookOnlyHarnessConfiguresNoOTLPRuntime(t *testing.T) {
	opts := runRecordedEndpointCommand(t, true, "omp")
	assertNoOTLPHarnesses(t, "repair --harness omp", opts.Harnesses)
}

// `--harness ""` is the documented collector-only install (splitHarnessCSV keeps it an empty
// list on purpose), so it must configure no runtime at all.
func TestEndpointInstallCollectorOnlyHarnessConfiguresNoOTLPRuntime(t *testing.T) {
	for _, repair := range []bool{false, true} {
		opts := runRecordedEndpointCommand(t, repair, "")
		assertNoOTLPHarnesses(t, "collector-only", opts.Harnesses)
	}
}

// Automatic selection that detects nothing OTLP-capable already reports "no runtime integrations
// will be configured", and the install docs promise the collector is installed without changing
// any runtime configuration. The lifecycle options have to agree with that message.
func TestEndpointInstallOptionsAutoWithoutOTLPRuntimeConfiguresNone(t *testing.T) {
	recordEndpointLifecycle(t)
	for name, discovered := range map[string][]harness.Harness{
		"nothing detected":   nil,
		"hook-only detected": {{Name: "omp", DisplayName: "Oh My Pi", Detected: true}},
		"skip-only detected": {{Name: "vercel_fx", DisplayName: "fx", Detected: true}},
	} {
		selection, err := resolveEndpointTargets(endpointHarnessAuto, discovered)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertNoOTLPHarnesses(t, "auto, "+name, endpointInstallOptions(selection, service.KindAuto).Harnesses)
	}
}

// No regression: selections that name OTLP runtimes still configure exactly those, including the
// default automatic selection on a machine where Claude Code and Codex are installed.
func TestEndpointInstallOptionsKeepSelectedOTLPRuntimes(t *testing.T) {
	recordEndpointLifecycle(t)
	autoDefault := endpointInstallCmd.Flags().Lookup("harness").DefValue
	cases := []struct {
		flag       string
		discovered []harness.Harness
		want       string
	}{
		{flag: autoDefault, discovered: []harness.Harness{
			{Name: "claude_code", DisplayName: "Claude Code", Detected: true},
			{Name: "codex_cli", DisplayName: "Codex CLI", Detected: true},
			{Name: "omp", DisplayName: "Oh My Pi", Detected: true},
		}, want: "claude,codex"},
		{flag: "claude,codex", want: "claude,codex"},
		{flag: "omp,claude", want: "claude"},
		{flag: "cursor,gemini", want: "gemini"},
	}
	for _, tc := range cases {
		selection, err := resolveEndpointTargets(tc.flag, tc.discovered)
		if err != nil {
			t.Fatalf("--harness %q: %v", tc.flag, err)
		}
		got := endpointInstallOptions(selection, service.KindAuto).Harnesses
		if strings.Join(got, ",") != tc.want {
			t.Fatalf("--harness %q: Harnesses = %#v, want %s", tc.flag, got, tc.want)
		}
	}

	all, err := resolveEndpointTargets("all", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(endpointInstallOptions(all, service.KindAuto).Harnesses, ",")
	if !strings.Contains(got, "claude") || !strings.Contains(got, "codex") {
		t.Fatalf("--harness all: Harnesses = %q, want claude and codex among them", got)
	}
}

// The commands themselves, not only the helper, pass an OTLP selection through unchanged.
func TestEndpointInstallAndRepairPassExplicitOTLPSelection(t *testing.T) {
	for _, repair := range []bool{false, true} {
		opts := runRecordedEndpointCommand(t, repair, "codex")
		if got := strings.Join(opts.Harnesses, ","); got != "codex" {
			t.Fatalf("repair=%t --harness codex: Harnesses = %q, want codex", repair, got)
		}
	}
}
