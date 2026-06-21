package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
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

func runEndpointInstall(cmd *cobra.Command, args []string) error {
	otlpHarnesses, hookHarnesses, err := splitEndpointTargets(splitHarnessCSV(endpointOpts.harnesses))
	if err != nil {
		return err
	}
	if endpointOpts.dryRun {
		return printPlannedActions(plannedInstallActions(false))
	}
	result, err := lifecycle.Install(lifecycle.InstallOptions{
		UserMode:              endpointUserMode(),
		LogPath:               endpointOpts.logPath,
		Harnesses:             otlpHarnesses,
		GRPCPort:              endpointOpts.grpcPort,
		HTTPPort:              endpointOpts.httpPort,
		CollectorPath:         endpointOpts.collectorPath,
		StartService:          !endpointOpts.noStart,
		IncludeRuntimeMetrics: endpointOpts.includeRuntimeMetrics,
		IncludeCodexSpans:     endpointOpts.includeCodexSpans,
		PromptRetention:       endpointOpts.promptRetention,
		SplunkHEC:             splunkHECOptions(),
		FalconHEC:             falconHECOptions(),
	})
	if err != nil {
		return err
	}
	fmt.Printf("Endpoint config written to %s\n", result.ConfigPath)
	fmt.Printf("Collector config written to %s\n", result.CollectorConfigPath)
	fmt.Printf("Launch plist written to %s\n", result.PlistPath)
	fmt.Printf("Install manifest written to %s\n", result.ManifestPath)
	fmt.Printf("Runtime log: %s\n", result.LogPath)
	if err := installHookTargetsFromEndpointInstall(hookHarnesses); err != nil {
		return fmt.Errorf("endpoint install completed, but hook installation failed: %w", err)
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
	printIngestStatus("Endpoint ingest", status.ManagedUpload)
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
	otlpHarnesses, hookHarnesses, err := splitEndpointTargets(splitHarnessCSV(endpointOpts.harnesses))
	if err != nil {
		return err
	}
	if endpointOpts.dryRun {
		return printPlannedActions(plannedInstallActions(true))
	}
	result, err := lifecycle.Repair(lifecycle.InstallOptions{
		UserMode:              endpointUserMode(),
		LogPath:               endpointOpts.logPath,
		Harnesses:             otlpHarnesses,
		GRPCPort:              endpointOpts.grpcPort,
		HTTPPort:              endpointOpts.httpPort,
		CollectorPath:         endpointOpts.collectorPath,
		StartService:          !endpointOpts.noStart,
		IncludeRuntimeMetrics: endpointOpts.includeRuntimeMetrics,
		IncludeCodexSpans:     endpointOpts.includeCodexSpans,
		PromptRetention:       endpointOpts.promptRetention,
		SplunkHEC:             splunkHECOptions(),
		FalconHEC:             falconHECOptions(),
	})
	if err != nil {
		return err
	}
	fmt.Printf("Endpoint repaired. Manifest: %s\n", result.ManifestPath)
	if err := installHookTargetsFromEndpointInstall(hookHarnesses); err != nil {
		return fmt.Errorf("endpoint repair completed, but hook installation failed: %w", err)
	}
	return nil
}

func installHookTargetsFromEndpointInstall(targets []string) error {
	if len(targets) == 0 {
		return nil
	}
	cfg := loadOrDefaultConfig()
	if endpointOpts.logPath != "" {
		cfg.LogPath = endpointOpts.logPath
	}
	for _, target := range targets {
		if err := installEndpointHookTarget(target, cfg); err != nil {
			return err
		}
	}
	return nil
}
