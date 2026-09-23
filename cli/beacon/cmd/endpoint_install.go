package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/spf13/cobra"
)

var endpointInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install local endpoint telemetry configuration",
	SilenceUsage: true,
	RunE:         runEndpointInstall,
}

var endpointStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show local endpoint status",
	SilenceUsage: true,
	RunE:         runEndpointStatus,
}

var endpointDiscoverCmd = &cobra.Command{
	Use:          "discover",
	Short:        "Discover supported local AI agent runtimes",
	SilenceUsage: true,
	RunE:         runEndpointDiscover,
}

var endpointUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Remove local endpoint service files",
	SilenceUsage: true,
	RunE:         runEndpointUninstall,
}

var endpointRepairCmd = &cobra.Command{
	Use:          "repair",
	Short:        "Repair local endpoint service and telemetry configuration",
	SilenceUsage: true,
	RunE:         runEndpointRepair,
}

var endpointDashboardCmd = &cobra.Command{
	Use:          "dashboard",
	Short:        "Run the local Beacon endpoint dashboard",
	SilenceUsage: true,
	RunE:         runEndpointDashboard,
}

var dashboardListenAndServe = dashboard.ListenAndServe

func runEndpointDashboard(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	userMode := endpointUserMode()
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, endpointOpts.logPath)
	cfg.UserMode = runtimeLog.EffectiveUserMode
	cfg.LogPath = runtimeLog.EffectiveLogPath
	if endpointOpts.dashboardAddr == "" {
		endpointOpts.dashboardAddr = dashboard.DefaultAddr
	}
	if err := dashboard.ValidateLoopbackAddr(endpointOpts.dashboardAddr); err != nil {
		return err
	}
	url := dashboard.URL(endpointOpts.dashboardAddr)
	fmt.Printf("Beacon endpoint dashboard: %s\n", url)
	fmt.Printf("Runtime log: %s\n", cfg.LogPath)
	if runtimeLog.Warning != "" {
		fmt.Printf("Runtime log source: %s\n", runtimeLog.Warning)
	}
	if endpointOpts.dashboardOpen {
		if err := dashboard.OpenBrowser(url); err != nil {
			return err
		}
	}
	return dashboardListenAndServe(dashboard.Options{
		Addr:     endpointOpts.dashboardAddr,
		LogPath:  endpointOpts.logPath,
		UserMode: userMode,
	})
}

// Seams for tests. Production always runs the lifecycle package.
var (
	endpointLifecycleInstall = lifecycle.Install
	endpointLifecycleRepair  = lifecycle.Repair
)

// endpointInstallOptions builds the lifecycle options shared by `endpoint install` and
// `endpoint repair` from the resolved target selection and flags.
//
// The selection is authoritative for OTLP runtimes: every --harness value (auto, all, an
// explicit list, or "" for collector-only) has been resolved by this point, so a selection with
// no OTLP targets means "configure none". lifecycle.InstallOptions reads a nil Harnesses as
// "unspecified" and substitutes the config default of Claude Code and Codex, which is how
// `--harness omp` came to rewrite ~/.claude/settings.json and ~/.codex/config.toml on every
// install and repair (#640). So the list is always handed over non-nil.
func endpointInstallOptions(selection endpointTargetSelection, serviceKind service.Kind) lifecycle.InstallOptions {
	harnesses := append([]string{}, selection.OTLP...)
	return lifecycle.InstallOptions{
		UserMode:              endpointUserMode(),
		LogPath:               endpointOpts.logPath,
		Harnesses:             harnesses,
		GRPCPort:              endpointOpts.grpcPort,
		HTTPPort:              endpointOpts.httpPort,
		HealthPort:            endpointOpts.healthPort,
		CollectorPath:         endpointOpts.collectorPath,
		StartService:          !endpointOpts.noStart,
		IncludeRuntimeMetrics: endpointOpts.includeRuntimeMetrics,
		IncludeCodexSpans:     endpointOpts.includeCodexSpans,
		SplunkHEC:             splunkHECOptions(),
		FalconHEC:             falconHECOptions(),
		ServiceKind:           serviceKind,
	}
}

func runEndpointInstall(cmd *cobra.Command, args []string) error {
	selection, err := resolveEndpointTargets(endpointOpts.harnesses, harness.DiscoverAll())
	if err != nil {
		return err
	}
	reportEndpointTargetSelection(cmd, selection)
	serviceKind, err := service.ParseKind(endpointOpts.serviceKind)
	if err != nil {
		return err
	}
	if endpointOpts.dryRun {
		return printPlannedActions(plannedInstallActions(false, serviceKind, selection.OTLP, selection.Hooks))
	}
	// Asked once, on an interactive install, before anything is written to disk. Every
	// non-interactive path (package postinstall, MDM, CI) is gated out inside.
	onboarded, err := maybeRunOnboarding(cmd)
	if err != nil {
		return err
	}
	// Confirming Beacon Managed connects this endpoint; --connect does the same for
	// the paths the wizard did not own.
	connectAfterInstall := onboarded.Connect || endpointOpts.connect
	result, err := endpointLifecycleInstall(endpointInstallOptions(selection, serviceKind))
	if err != nil {
		return err
	}
	// Only now is the machine actually installed, so only now is it onboarded. The
	// record used to be written before this call, which meant a failed install left
	// a profile claiming otherwise and the retry silently skipped the wizard.
	if onboarded.Persist != nil {
		if err := onboarded.Persist(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "beacon: %v\n", err)
		}
	}
	fmt.Printf("Endpoint config written to %s\n", result.ConfigPath)
	fmt.Printf("Collector config written to %s\n", result.CollectorConfigPath)
	fmt.Printf("Service definition written to %s\n", result.PlistPath)
	fmt.Printf("Install manifest written to %s\n", result.ManifestPath)
	fmt.Printf("Runtime log: %s\n", result.LogPath)
	switch {
	case result.InventoryJobPath != "":
		fmt.Printf("Scheduled inventory job: %s\n", result.InventoryJobPath)
	case result.InventoryJobDetail != "":
		fmt.Printf("Scheduled inventory job not installed: %s\n", result.InventoryJobDetail)
	}
	printLingerGap(cmd.ErrOrStderr(), result)
	installHookTargetsFromEndpointInstall(cmd.ErrOrStderr(), selection.Hooks)
	if connectAfterInstall {
		// The install is complete and stands on its own; a failed connect is reported
		// with the retry command rather than turning a working install into an error.
		fmt.Fprintln(cmd.OutOrStdout())
		if err := connectEndpoint(cmd, endpointUserMode(), result.LogPath); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "beacon: managed ingest was not connected (%v). Run `beacon endpoint connect` to try again.\n", err)
		} else {
			recordDestinationAsymptote(cmd)
		}
	}
	return nil
}

func runEndpointStatus(cmd *cobra.Command, args []string) error {
	status := lifecycle.GetStatus(endpointUserMode(), endpointOpts.logPath)
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("Beacon Endpoint Agent %s\n", status.Version)
	fmt.Printf("Config: %s\n", status.ConfigPath)
	fmt.Printf("Runtime log: %s\n", status.LogPath)
	if status.RuntimeLog.Warning != "" {
		fmt.Printf("Runtime log source: %s\n", status.RuntimeLog.Warning)
	}
	fmt.Printf("Collector: grpc=%t http=%t", status.Collector.GRPCReady, status.Collector.HTTPReady)
	if status.Collector.Message != "" {
		fmt.Printf(" (%s)", status.Collector.Message)
	}
	fmt.Println()
	fmt.Printf("Service: loaded=%t running=%t", status.Service.Loaded, status.Service.Running)
	if status.Service.Message != "" {
		fmt.Printf(" (%s)", status.Service.Message)
	}
	fmt.Println()
	for _, h := range status.Harnesses {
		if h.Detected {
			fmt.Printf("Harness: %s %s telemetry=%s\n", h.DisplayName, h.Version, h.TelemetryStatus)
		}
	}
	for _, check := range status.Diagnostics {
		if check.Status != "ok" {
			fmt.Printf("Diagnostic: %s %s (%s)\n", check.Name, check.Status, check.Message)
		}
	}
	if status.LastEvent == "" {
		fmt.Println("Last event: none")
	} else {
		fmt.Println("Last event: present")
	}
	fmt.Println(managedIngestStatusLine(status.ManagedIngest))
	fmt.Println(inventoryHeartbeatStatusLine(status.InventoryHeartbeat))
	return nil
}

func runEndpointDiscover(cmd *cobra.Command, args []string) error {
	discovered := harness.DiscoverAll()
	if endpointOpts.jsonOutput {
		if !endpointOpts.allTargets {
			filtered := []harness.Harness{}
			for _, h := range discovered {
				if h.Detected {
					filtered = append(filtered, h)
				}
			}
			return json.NewEncoder(os.Stdout).Encode(filtered)
		}
		return json.NewEncoder(os.Stdout).Encode(discovered)
	}
	for _, h := range discovered {
		if !endpointOpts.allTargets && !h.Detected {
			continue
		}
		state := "not detected"
		if h.Detected {
			state = "detected"
		}
		fmt.Printf("%s: %s, telemetry=%s", h.DisplayName, state, h.TelemetryStatus)
		if h.ExecutablePath != "" {
			fmt.Printf(", path=%s", h.ExecutablePath)
		}
		fmt.Println()
	}
	cfg := loadOrDefaultConfig()
	for _, h := range discovered {
		if h.Detected {
			event := schema.NewEvent(schema.NewEventOptions{
				Action:       "agent.detected",
				Category:     "inventory",
				Severity:     schema.SeverityInfo,
				AgentVersion: version.GetVersion(),
				Harness: schema.HarnessInfo{
					Name:           h.Name,
					Version:        h.Version,
					ExecutablePath: h.ExecutablePath,
					ConfigPath:     h.ConfigPath,
				},
				Message: h.DisplayName + " detected",
			})
			if _, err := writer.AppendEvent(event, writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode}); err != nil {
				return err
			}
		}
	}
	return nil
}

func runEndpointUninstall(cmd *cobra.Command, args []string) error {
	if endpointOpts.dryRun {
		return printPlannedActions(plannedUninstallActions())
	}
	if err := lifecycle.Uninstall(lifecycle.UninstallOptions{UserMode: endpointUserMode(), LogPath: endpointOpts.logPath, KeepLogs: endpointOpts.keepLogs, KeepConfig: endpointOpts.keepConfig}); err != nil {
		return err
	}
	fmt.Println("Endpoint service, config, and managed files removed.")
	return nil
}

func runEndpointRepair(cmd *cobra.Command, args []string) error {
	selection, err := resolveEndpointTargets(endpointOpts.harnesses, harness.DiscoverAll())
	if err != nil {
		return err
	}
	reportEndpointTargetSelection(cmd, selection)
	serviceKind, err := service.ParseKind(endpointOpts.serviceKind)
	if err != nil {
		return err
	}
	if endpointOpts.dryRun {
		return printPlannedActions(plannedInstallActions(true, serviceKind, selection.OTLP, selection.Hooks))
	}
	// Resend only. Repair is a maintenance command and never asks a question, but a
	// signup that failed on a flaky network deserves the retry the docs promise.
	retryPendingOnboarding()
	result, err := endpointLifecycleRepair(endpointInstallOptions(selection, serviceKind))
	if err != nil {
		return err
	}
	fmt.Printf("Endpoint repaired. Manifest: %s\n", result.ManifestPath)
	printLingerGap(cmd.ErrOrStderr(), result)
	installHookTargetsFromEndpointInstall(cmd.ErrOrStderr(), selection.Hooks)
	return nil
}

func reportEndpointTargetSelection(cmd *cobra.Command, selection endpointTargetSelection) {
	if !selection.Automatic {
		return
	}
	out := cmd.OutOrStdout()
	var targets []string
	seen := map[string]bool{}
	for _, target := range append(append([]string(nil), selection.OTLP...), selection.Hooks...) {
		if !seen[target] {
			targets = append(targets, target)
			seen[target] = true
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(out, "No automatically installable runtimes were detected; no runtime integrations will be configured.")
	} else {
		fmt.Fprintf(out, "Automatically configuring detected runtimes: %s\n", strings.Join(targets, ", "))
	}
	for _, skipped := range selection.Skipped {
		fmt.Fprintf(out, "Detected %s but skipped automatic configuration: %s.\n", skipped.Name, skipped.Reason)
	}
}

// printLingerGap says so when the collector is running but will not outlive this login session.
//
// Install used to print an unbroken run of success lines here even when the systemd --user unit
// had no linger, so the user learned about it at their next logout, with collection already
// stopped. The headline is chosen by whether a remediation is known rather than by inspecting
// the detail text: a known-off linger gets the fact and the exact command, and an unverifiable
// one is reported as unverified instead of asserted as broken.
func printLingerGap(out io.Writer, result lifecycle.InstallResult) {
	if !result.LingerApplicable || result.LingerEnabled {
		return
	}
	if result.LingerRemediation == "" {
		fmt.Fprintln(out, "Warning: could not verify that collection survives logout.")
	} else {
		fmt.Fprintln(out, "Warning: the collector is running, but collection will stop when this user logs out.")
	}
	if result.LingerDetail != "" {
		fmt.Fprintf(out, "Detail: %s\n", result.LingerDetail)
	}
	if result.LingerRemediation != "" {
		fmt.Fprintf(out, "To keep collecting after logout, run: %s\n", result.LingerRemediation)
	}
}

// installHookTargetsFromEndpointInstall installs the hook targets that ride an endpoint
// install or repair, and reports the ones it could not write as warnings on out.
//
// By the time this runs the collector, its config, the service unit, and the OTLP settings
// are all in place, so the endpoint works. A hook that could not be written, most often a
// runtime settings file with a shape the merge does not understand, is one command away
// from being retried, and it used to turn that complete install into a non-zero exit that
// read as a broken endpoint. Every target is still attempted, so one runtime's settings
// file cannot stop another runtime's hooks from being installed.
func installHookTargetsFromEndpointInstall(out io.Writer, targets []string) {
	if len(targets) == 0 {
		return
	}
	cfg := loadOrDefaultConfig()
	if endpointOpts.logPath != "" {
		cfg.LogPath = endpointOpts.logPath
	}
	var failed []string
	for _, target := range targets {
		if err := installEndpointHookTarget(target, cfg); err != nil {
			failed = append(failed, target)
			fmt.Fprintf(out, "Warning: %s hooks were not installed: %v\n", target, err)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(out, "The endpoint is installed without them. Retry with: beacon endpoint hooks install --harness %s\n", strings.Join(failed, ","))
	}
}
