package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/cowork"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/wazuh"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var endpointOpts struct {
	userMode         bool
	logPath          string
	harnesses        string
	hookHarnesses    string
	outputDir        string
	jsonOutput       bool
	grpcPort         int
	httpPort         int
	collectorPath    string
	keepLogs         bool
	keepConfig       bool
	noStart          bool
	coworkHeaders    string
	hookLevel        string
	contentRetention string
	dashboardAddr    string
	dashboardOpen    bool
}

var endpointCmd = &cobra.Command{
	Use:   "endpoint",
	Short: "Manage the local Beacon endpoint agent",
	Long: `Manage the open-source Beacon endpoint agent for local AI runtime
discovery, telemetry collection, and Wazuh-compatible JSON logs.`,
}

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

var endpointWazuhCmd = &cobra.Command{
	Use:   "wazuh",
	Short: "Manage Wazuh integration content",
}

var endpointIntegrationsCmd = &cobra.Command{
	Use:   "integrations",
	Short: "Manage admin-configured endpoint integrations",
}

var endpointHooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage endpoint hook integrations",
}

var endpointHooksInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install endpoint hooks for supported harnesses",
	SilenceUsage: true,
	RunE:         runEndpointHooksInstall,
}

var endpointHooksUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Uninstall endpoint hooks for supported harnesses",
	SilenceUsage: true,
	RunE:         runEndpointHooksUninstall,
}

var endpointHooksStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show endpoint hook integration status",
	SilenceUsage: true,
	RunE:         runEndpointHooksStatus,
}

var endpointCoworkCmd = &cobra.Command{
	Use:   "claude-cowork",
	Short: "Manage Claude Cowork OpenTelemetry integration",
}

var endpointCoworkPrintConfigCmd = &cobra.Command{
	Use:   "print-config",
	Short: "Print Claude Cowork OTLP setup guidance",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		fmt.Print(cowork.PrintConfig(cowork.Config{
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort),
			Protocol: "HTTP/protobuf",
			Headers:  endpointOpts.coworkHeaders,
		}))
	},
}

var endpointCoworkStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Claude Cowork endpoint integration status",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		status := cowork.GetStatus(cfg.LogPath)
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(status)
			return
		}
		fmt.Printf("%s: detected=%t observed=%t\n", status.DisplayName, status.Detected, status.LastEventObserved)
		fmt.Println(status.Message)
	},
}

var endpointCoworkValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate whether Claude Cowork events are arriving",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadOrDefaultConfig()
		status := cowork.GetStatus(cfg.LogPath)
		if !status.LastEventObserved {
			fmt.Print(cowork.PrintConfig(cowork.Config{
				Endpoint: fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort),
				Protocol: "HTTP/protobuf",
				Headers:  endpointOpts.coworkHeaders,
			}))
			return fmt.Errorf("no Claude Cowork events observed in %s", cfg.LogPath)
		}
		fmt.Println("Claude Cowork events observed in endpoint runtime log.")
		return nil
	},
}

var endpointWazuhPrintConfigCmd = &cobra.Command{
	Use:   "print-config",
	Short: "Print a Wazuh localfile snippet",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		fmt.Print(wazuh.LocalfileSnippet(cfg.LogPath))
	},
}

var endpointWazuhInstallPackCmd = &cobra.Command{
	Use:          "install-pack",
	Short:        "Write Wazuh rules and config snippets to a directory",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if endpointOpts.outputDir == "" {
			return fmt.Errorf("--output is required")
		}
		cfg := loadOrDefaultConfig()
		if err := wazuh.InstallPack(endpointOpts.outputDir, cfg.LogPath); err != nil {
			return err
		}
		fmt.Printf("Wazuh content pack written to %s\n", endpointOpts.outputDir)
		return nil
	},
}

var endpointWazuhValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Write and describe a Wazuh validation event",
	SilenceUsage: true,
	RunE:         runEndpointWazuhValidate,
}

func init() {
	rootCmd.AddCommand(endpointCmd)

	endpointCmd.AddCommand(endpointInstallCmd)
	endpointCmd.AddCommand(endpointStatusCmd)
	endpointCmd.AddCommand(endpointDiscoverCmd)
	endpointCmd.AddCommand(endpointUninstallCmd)
	endpointCmd.AddCommand(endpointRepairCmd)
	endpointCmd.AddCommand(endpointDashboardCmd)
	endpointCmd.AddCommand(endpointWazuhCmd)
	endpointCmd.AddCommand(endpointIntegrationsCmd)
	endpointCmd.AddCommand(endpointHooksCmd)
	endpointWazuhCmd.AddCommand(endpointWazuhPrintConfigCmd)
	endpointWazuhCmd.AddCommand(endpointWazuhInstallPackCmd)
	endpointWazuhCmd.AddCommand(endpointWazuhValidateCmd)
	endpointIntegrationsCmd.AddCommand(endpointCoworkCmd)
	endpointHooksCmd.AddCommand(endpointHooksInstallCmd)
	endpointHooksCmd.AddCommand(endpointHooksUninstallCmd)
	endpointHooksCmd.AddCommand(endpointHooksStatusCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkPrintConfigCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkStatusCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkValidateCmd)

	for _, c := range []*cobra.Command{endpointInstallCmd, endpointStatusCmd, endpointDiscoverCmd, endpointUninstallCmd, endpointRepairCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}

	endpointInstallCmd.Flags().StringVar(&endpointOpts.harnesses, "harness", "claude,codex", "Comma-separated harnesses to configure")
	endpointInstallCmd.Flags().IntVar(&endpointOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	endpointInstallCmd.Flags().IntVar(&endpointOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	endpointInstallCmd.Flags().StringVar(&endpointOpts.collectorPath, "collector", "", "Path to an otelcol or otelcol-contrib binary")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.noStart, "no-start", false, "Write files without starting the launchd service")
	endpointInstallCmd.Flags().StringVar(&endpointOpts.contentRetention, "content-retention", "metadata", "Content retention mode: metadata, redacted, or full")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.harnesses, "harness", "claude,codex", "Comma-separated harnesses to configure")
	endpointRepairCmd.Flags().IntVar(&endpointOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	endpointRepairCmd.Flags().IntVar(&endpointOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.contentRetention, "content-retention", "metadata", "Content retention mode: metadata, redacted, or full")
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
	endpointDashboardCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointDashboardCmd.Flags().StringVar(&endpointOpts.dashboardAddr, "addr", dashboard.DefaultAddr, "Local dashboard listen address")
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.dashboardOpen, "open", false, "Open the dashboard in a browser")

	endpointDiscoverCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print discovery as JSON")
	endpointStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	endpointWazuhPrintConfigCmd.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
	endpointWazuhPrintConfigCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointWazuhInstallPackCmd.Flags().StringVar(&endpointOpts.outputDir, "output", "", "Output directory for Wazuh content pack")
	endpointWazuhInstallPackCmd.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
	endpointWazuhInstallPackCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointWazuhValidateCmd.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
	endpointWazuhValidateCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	for _, c := range []*cobra.Command{endpointCoworkPrintConfigCmd, endpointCoworkStatusCmd, endpointCoworkValidateCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}
	endpointCoworkPrintConfigCmd.Flags().StringVar(&endpointOpts.coworkHeaders, "headers", "", "Optional OTLP headers to show in setup guidance")
	endpointCoworkValidateCmd.Flags().StringVar(&endpointOpts.coworkHeaders, "headers", "", "Optional OTLP headers to show when validation fails")
	endpointCoworkStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	for _, c := range []*cobra.Command{endpointHooksInstallCmd, endpointHooksUninstallCmd, endpointHooksStatusCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", false, "Use per-user endpoint paths instead of system paths")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().StringVar(&endpointOpts.hookHarnesses, "harness", "cursor", "Comma-separated hook harnesses")
		c.Flags().StringVar(&endpointOpts.hookLevel, "level", "user", "Hook install level: user or project")
	}
	endpointHooksStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	endpointUninstallCmd.Flags().BoolVar(&endpointOpts.keepLogs, "keep-logs", false, "Keep runtime logs during uninstall")
	endpointUninstallCmd.Flags().BoolVar(&endpointOpts.keepConfig, "keep-config", false, "Keep harness telemetry configuration during uninstall")
}

func runEndpointHooksInstall(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	for _, name := range splitCSV(endpointOpts.hookHarnesses) {
		switch strings.TrimSpace(name) {
		case "cursor":
			status, err := endpointhooks.InstallCursor(endpointhooks.CursorOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Cursor hooks installed: %s\n", status.HooksJSONPath)
		case "":
		default:
			return fmt.Errorf("unsupported hook harness %q", name)
		}
	}
	return nil
}

func runEndpointHooksUninstall(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	for _, name := range splitCSV(endpointOpts.hookHarnesses) {
		switch strings.TrimSpace(name) {
		case "cursor":
			status, err := endpointhooks.UninstallCursor(endpointhooks.CursorOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
			if err != nil {
				return err
			}
			fmt.Println(status.Message)
		case "":
		default:
			return fmt.Errorf("unsupported hook harness %q", name)
		}
	}
	return nil
}

func runEndpointHooksStatus(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	status := endpointhooks.CursorHookStatus(endpointhooks.CursorOptions{
		Level:    endpointhooks.Level(endpointOpts.hookLevel),
		LogPath:  cfg.LogPath,
		UserMode: cfg.UserMode,
	})
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("Cursor hooks: installed=%t path=%s\n", status.Installed, status.HooksJSONPath)
	fmt.Println(status.Message)
	return nil
}

func runEndpointWazuhValidate(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	path, err := writeValidationEvent(cfg, "wazuh")
	if err != nil {
		return err
	}
	fmt.Printf("Validation event written to %s\n", path)
	fmt.Println("Expected Wazuh fields: vendor=beacon product=endpoint-agent event.kind=agent_runtime")
	fmt.Println("Wazuh localfile snippet:")
	fmt.Print(wazuh.LocalfileSnippet(cfg.LogPath))
	fmt.Println("Expected base rule: 100500")
	return nil
}

func runEndpointDashboard(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	if endpointOpts.dashboardAddr == "" {
		endpointOpts.dashboardAddr = dashboard.DefaultAddr
	}
	if err := dashboard.ValidateLoopbackAddr(endpointOpts.dashboardAddr); err != nil {
		return err
	}
	url := dashboard.URL(endpointOpts.dashboardAddr)
	fmt.Printf("Beacon endpoint dashboard: %s\n", url)
	fmt.Printf("Runtime log: %s\n", cfg.LogPath)
	if endpointOpts.dashboardOpen {
		if err := dashboard.OpenBrowser(url); err != nil {
			return err
		}
	}
	return dashboard.ListenAndServe(dashboard.Options{
		Addr:     endpointOpts.dashboardAddr,
		LogPath:  cfg.LogPath,
		UserMode: cfg.UserMode,
	})
}

func runEndpointInstall(cmd *cobra.Command, args []string) error {
	result, err := lifecycle.Install(lifecycle.InstallOptions{
		UserMode:         endpointOpts.userMode,
		LogPath:          endpointOpts.logPath,
		Harnesses:        splitCSV(endpointOpts.harnesses),
		GRPCPort:         endpointOpts.grpcPort,
		HTTPPort:         endpointOpts.httpPort,
		CollectorPath:    endpointOpts.collectorPath,
		StartService:     !endpointOpts.noStart,
		ContentRetention: endpointconfig.ContentRetention(endpointOpts.contentRetention),
	})
	if err != nil {
		return err
	}
	fmt.Printf("Endpoint config written to %s\n", result.ConfigPath)
	fmt.Printf("Collector config written to %s\n", result.CollectorConfigPath)
	fmt.Printf("Launch plist written to %s\n", result.PlistPath)
	fmt.Printf("Install manifest written to %s\n", result.ManifestPath)
	fmt.Printf("Runtime log: %s\n", result.LogPath)
	return nil
}

func runEndpointStatus(cmd *cobra.Command, args []string) error {
	status := lifecycle.GetStatus(endpointOpts.userMode, endpointOpts.logPath)
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("Beacon Endpoint Agent %s\n", status.Version)
	fmt.Printf("Config: %s\n", status.ConfigPath)
	fmt.Printf("Runtime log: %s\n", status.LogPath)
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
	return nil
}

func runEndpointDiscover(cmd *cobra.Command, args []string) error {
	discovered := harness.DiscoverAll()
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(discovered)
	}
	for _, h := range discovered {
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
			_, _ = writer.AppendEvent(event, writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode})
		}
	}
	return nil
}

func writeValidationEvent(cfg endpointconfig.Config, destination string) (string, error) {
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       "agent.detected",
		Category:     "validation",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: "test_harness", Version: "test"},
		Message:      "Beacon endpoint Wazuh validation event",
	})
	event.Destination = &schema.DestinationInfo{Type: destination, Mode: "localfile", Status: "configured"}
	return writer.AppendEvent(event, writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode})
}

func runEndpointUninstall(cmd *cobra.Command, args []string) error {
	if err := lifecycle.Uninstall(lifecycle.UninstallOptions{UserMode: endpointOpts.userMode, LogPath: endpointOpts.logPath, KeepLogs: endpointOpts.keepLogs, KeepConfig: endpointOpts.keepConfig}); err != nil {
		return err
	}
	fmt.Println("Endpoint service, config, and managed files removed.")
	return nil
}

func runEndpointRepair(cmd *cobra.Command, args []string) error {
	result, err := lifecycle.Repair(lifecycle.InstallOptions{
		UserMode:         endpointOpts.userMode,
		LogPath:          endpointOpts.logPath,
		Harnesses:        splitCSV(endpointOpts.harnesses),
		GRPCPort:         endpointOpts.grpcPort,
		HTTPPort:         endpointOpts.httpPort,
		CollectorPath:    endpointOpts.collectorPath,
		StartService:     !endpointOpts.noStart,
		ContentRetention: endpointconfig.ContentRetention(endpointOpts.contentRetention),
	})
	if err != nil {
		return err
	}
	fmt.Printf("Endpoint repaired. Manifest: %s\n", result.ManifestPath)
	return nil
}

func loadOrDefaultConfig() endpointconfig.Config {
	if cfg, err := endpointconfig.Load(endpointOpts.userMode); err == nil {
		if endpointOpts.logPath != "" {
			cfg.LogPath = endpointOpts.logPath
		}
		return cfg
	}
	logPath := endpointOpts.logPath
	if logPath == "" {
		logPath = writer.DefaultPath(endpointOpts.userMode)
	}
	return endpointconfig.Default(endpointOpts.userMode, logPath)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
