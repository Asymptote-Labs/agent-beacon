package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/elastic"
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
	userMode                 bool
	systemMode               bool
	logPath                  string
	harnesses                string
	hookHarnesses            string
	outputDir                string
	jsonOutput               bool
	grpcPort                 int
	httpPort                 int
	collectorPath            string
	includeRuntimeMetrics    bool
	keepLogs                 bool
	keepConfig               bool
	noStart                  bool
	coworkHeaders            string
	coworkEndpoint           string
	coworkResourceAttributes string
	coworkNgrok              bool
	coworkOpen               bool
	coworkSince              string
	elasticPackDir           string
	hookLevel                string
	contentRetention         string
	splunkHECEndpoint        string
	splunkHECToken           string
	splunkIndex              string
	splunkSource             string
	splunkSourcetype         string
	splunkInsecureSkipVerify bool
	splunkCAFile             string
	dashboardAddr            string
	dashboardOpen            bool
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

var endpointElasticCmd = &cobra.Command{
	Use:   "elastic",
	Short: "Manage Elasticsearch integration content",
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
		endpoint := endpointOpts.coworkEndpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
		}
		fmt.Print(cowork.PrintConfig(cowork.Config{
			Endpoint:           endpoint,
			Protocol:           "HTTP/protobuf",
			Headers:            endpointOpts.coworkHeaders,
			ResourceAttributes: endpointOpts.coworkResourceAttributes,
		}))
	},
}

var endpointCoworkSetupCmd = &cobra.Command{
	Use:          "setup",
	Short:        "Print or create Claude Cowork OTLP admin settings",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEndpointCoworkSetup(cmd.Context())
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
		fmt.Printf("%s: detected=%t observed=%t", status.DisplayName, status.Detected, status.LastEventObserved)
		if status.LastEventObservedAt != "" {
			fmt.Printf(" last=%s", status.LastEventObservedAt)
		}
		fmt.Println()
		fmt.Println(status.Message)
	},
}

var endpointCoworkValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate whether Claude Cowork events are arriving",
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runEndpointCoworkValidate() },
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

var endpointElasticPrintConfigCmd = &cobra.Command{
	Use:   "print-config",
	Short: "Print a Filebeat input for Beacon endpoint events",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		fmt.Print(elastic.InputSnippet(cfg.LogPath))
	},
}

var endpointElasticInstallPackCmd = &cobra.Command{
	Use:          "install-pack",
	Short:        "Write Elasticsearch templates, pipeline, and Filebeat content to a directory",
	SilenceUsage: true,
	RunE:         runEndpointElasticInstallPack,
}

var endpointElasticUpCmd = &cobra.Command{
	Use:          "up",
	Short:        "Start a local Elasticsearch, Kibana, and Filebeat stack",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEndpointElasticUp(cmd.Context())
	},
}

var endpointElasticDownCmd = &cobra.Command{
	Use:          "down",
	Short:        "Stop the local Elasticsearch stack",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEndpointElasticDown(cmd.Context())
	},
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
	endpointCmd.AddCommand(endpointElasticCmd)
	endpointCmd.AddCommand(endpointIntegrationsCmd)
	endpointCmd.AddCommand(endpointHooksCmd)
	endpointWazuhCmd.AddCommand(endpointWazuhPrintConfigCmd)
	endpointWazuhCmd.AddCommand(endpointWazuhInstallPackCmd)
	endpointWazuhCmd.AddCommand(endpointWazuhValidateCmd)
	endpointElasticCmd.AddCommand(endpointElasticPrintConfigCmd)
	endpointElasticCmd.AddCommand(endpointElasticInstallPackCmd)
	endpointElasticCmd.AddCommand(endpointElasticUpCmd)
	endpointElasticCmd.AddCommand(endpointElasticDownCmd)
	endpointIntegrationsCmd.AddCommand(endpointCoworkCmd)
	endpointHooksCmd.AddCommand(endpointHooksInstallCmd)
	endpointHooksCmd.AddCommand(endpointHooksUninstallCmd)
	endpointHooksCmd.AddCommand(endpointHooksStatusCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkPrintConfigCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkSetupCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkStatusCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkValidateCmd)

	for _, c := range []*cobra.Command{endpointInstallCmd, endpointStatusCmd, endpointDiscoverCmd, endpointUninstallCmd, endpointRepairCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}

	endpointInstallCmd.Flags().StringVar(&endpointOpts.harnesses, "harness", "claude,codex", "Comma-separated harnesses to configure")
	endpointInstallCmd.Flags().IntVar(&endpointOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	endpointInstallCmd.Flags().IntVar(&endpointOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	endpointInstallCmd.Flags().StringVar(&endpointOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.includeRuntimeMetrics, "include-runtime-metrics", false, "Include generic process/runtime OTLP metrics in the runtime JSONL log")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.noStart, "no-start", false, "Write files without starting the launchd service")
	endpointInstallCmd.Flags().StringVar(&endpointOpts.contentRetention, "content-retention", "full", "Content retention mode: metadata, redacted, or full")
	registerSplunkFlags(endpointInstallCmd)
	endpointRepairCmd.Flags().StringVar(&endpointOpts.harnesses, "harness", "claude,codex", "Comma-separated harnesses to configure")
	endpointRepairCmd.Flags().IntVar(&endpointOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	endpointRepairCmd.Flags().IntVar(&endpointOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
	endpointRepairCmd.Flags().BoolVar(&endpointOpts.includeRuntimeMetrics, "include-runtime-metrics", false, "Include generic process/runtime OTLP metrics in the runtime JSONL log")
	endpointRepairCmd.Flags().BoolVar(&endpointOpts.noStart, "no-start", false, "Write files without starting the launchd service")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.contentRetention, "content-retention", "full", "Content retention mode: metadata, redacted, or full")
	registerSplunkFlags(endpointRepairCmd)
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	endpointDashboardCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointDashboardCmd.Flags().StringVar(&endpointOpts.dashboardAddr, "addr", dashboard.DefaultAddr, "Local dashboard listen address")
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.dashboardOpen, "open", false, "Open the dashboard in a browser")

	endpointDiscoverCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print discovery as JSON")
	endpointStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	endpointWazuhPrintConfigCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointWazuhPrintConfigCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	endpointWazuhPrintConfigCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointWazuhInstallPackCmd.Flags().StringVar(&endpointOpts.outputDir, "output", "", "Output directory for Wazuh content pack")
	endpointWazuhInstallPackCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointWazuhInstallPackCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	endpointWazuhInstallPackCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointWazuhValidateCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointWazuhValidateCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	endpointWazuhValidateCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	for _, c := range []*cobra.Command{endpointElasticPrintConfigCmd, endpointElasticInstallPackCmd, endpointElasticUpCmd, endpointElasticDownCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}
	endpointElasticInstallPackCmd.Flags().StringVar(&endpointOpts.outputDir, "output", "", "Output directory for Elasticsearch content pack")
	endpointElasticUpCmd.Flags().StringVar(&endpointOpts.elasticPackDir, "pack-dir", elastic.DefaultOutputDir, "Elasticsearch pack directory")
	endpointElasticDownCmd.Flags().StringVar(&endpointOpts.elasticPackDir, "pack-dir", elastic.DefaultOutputDir, "Elasticsearch pack directory")
	for _, c := range []*cobra.Command{endpointCoworkPrintConfigCmd, endpointCoworkSetupCmd, endpointCoworkStatusCmd, endpointCoworkValidateCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}
	endpointCoworkPrintConfigCmd.Flags().StringVar(&endpointOpts.coworkHeaders, "headers", "", "Optional OTLP headers to show in setup guidance")
	endpointCoworkPrintConfigCmd.Flags().StringVar(&endpointOpts.coworkEndpoint, "endpoint", "", "Public OTLP HTTPS endpoint to show in setup guidance")
	endpointCoworkPrintConfigCmd.Flags().StringVar(&endpointOpts.coworkResourceAttributes, "resource-attributes", "", "Optional Claude Cowork resource attributes")
	endpointCoworkSetupCmd.Flags().StringVar(&endpointOpts.coworkEndpoint, "endpoint", "", "Public OTLP HTTPS endpoint reachable by Claude Cowork")
	endpointCoworkSetupCmd.Flags().StringVar(&endpointOpts.coworkHeaders, "headers", "", "Optional OTLP headers for the Claude admin settings")
	endpointCoworkSetupCmd.Flags().StringVar(&endpointOpts.coworkResourceAttributes, "resource-attributes", "", "Optional Claude Cowork resource attributes")
	endpointCoworkSetupCmd.Flags().BoolVar(&endpointOpts.coworkNgrok, "ngrok", false, "Create a temporary authenticated ngrok tunnel to the local OTLP HTTP receiver")
	endpointCoworkSetupCmd.Flags().BoolVar(&endpointOpts.coworkOpen, "open", false, "Open Claude Cowork admin settings in a browser")
	endpointCoworkValidateCmd.Flags().StringVar(&endpointOpts.coworkHeaders, "headers", "", "Optional OTLP headers to show when validation fails")
	endpointCoworkValidateCmd.Flags().StringVar(&endpointOpts.coworkEndpoint, "endpoint", "", "Public OTLP HTTPS endpoint to show when validation fails")
	endpointCoworkValidateCmd.Flags().StringVar(&endpointOpts.coworkResourceAttributes, "resource-attributes", "", "Optional Claude Cowork resource attributes")
	endpointCoworkValidateCmd.Flags().StringVar(&endpointOpts.coworkSince, "since", "", "Require a Claude Cowork event within this duration, such as 10m")
	endpointCoworkStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	for _, c := range []*cobra.Command{endpointHooksInstallCmd, endpointHooksUninstallCmd, endpointHooksStatusCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
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
		case "factory", "droid":
			status, err := endpointhooks.InstallFactory(endpointhooks.FactoryOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Factory hooks installed: %s\n", status.SettingsPath)
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
		case "factory", "droid":
			status, err := endpointhooks.UninstallFactory(endpointhooks.FactoryOptions{
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
	statuses := map[string]interface{}{}
	for _, name := range splitCSV(endpointOpts.hookHarnesses) {
		switch strings.TrimSpace(name) {
		case "cursor":
			statuses["cursor"] = endpointhooks.CursorHookStatus(endpointhooks.CursorOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "factory", "droid":
			statuses["factory"] = endpointhooks.FactoryHookStatus(endpointhooks.FactoryOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "":
		default:
			return fmt.Errorf("unsupported hook harness %q", name)
		}
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(statuses)
	}
	for _, name := range splitCSV(endpointOpts.hookHarnesses) {
		switch strings.TrimSpace(name) {
		case "cursor":
			status := statuses["cursor"].(endpointhooks.CursorStatus)
			fmt.Printf("Cursor hooks: installed=%t path=%s\n", status.Installed, status.HooksJSONPath)
			fmt.Println(status.Message)
		case "factory", "droid":
			status := statuses["factory"].(endpointhooks.FactoryStatus)
			fmt.Printf("Factory hooks: installed=%t path=%s\n", status.Installed, status.SettingsPath)
			fmt.Println(status.Message)
		}
	}
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

func runEndpointElasticInstallPack(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	outputDir := endpointOpts.outputDir
	if outputDir == "" {
		outputDir = elastic.DefaultOutputDir
	}
	if err := elastic.InstallPack(outputDir, cfg.LogPath); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch content pack written to %s\n", outputDir)
	return nil
}

func runEndpointElasticUp(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("beacon endpoint elastic up is currently macOS-only")
	}
	cfg := loadOrDefaultConfig()
	logPath, err := filepath.Abs(cfg.LogPath)
	if err != nil {
		return err
	}
	packDir := endpointOpts.elasticPackDir
	if packDir == "" {
		packDir = elastic.DefaultOutputDir
	}
	if err := ensureElasticPack(packDir, logPath); err != nil {
		return err
	}
	if err := ensureLogFile(logPath); err != nil {
		return err
	}
	env := os.Environ()
	env = append(env, "BEACON_LOG_DIR="+filepath.Dir(logPath))
	if err := runDockerCompose(ctx, packDir, env, "up", "-d"); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch ready at http://localhost:%s\n", envDefault("BEACON_ELASTIC_ES_PORT", "9200"))
	fmt.Printf("Kibana ready at http://localhost:%s\n", envDefault("BEACON_ELASTIC_KIBANA_PORT", "5601"))
	fmt.Printf("Filebeat tailing %s\n", logPath)
	return nil
}

func runEndpointElasticDown(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("beacon endpoint elastic down is currently macOS-only")
	}
	packDir := endpointOpts.elasticPackDir
	if packDir == "" {
		packDir = elastic.DefaultOutputDir
	}
	if _, err := os.Stat(filepath.Join(packDir, "docker-compose.yml")); os.IsNotExist(err) {
		fmt.Printf("No Elasticsearch stack found for %s\n", packDir)
		return nil
	} else if err != nil {
		return err
	}
	logPath, err := filepath.Abs(loadOrDefaultConfig().LogPath)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "BEACON_LOG_DIR="+filepath.Dir(logPath))
	if err := runDockerCompose(ctx, packDir, env, "down", "--remove-orphans"); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch stack stopped for %s\n", packDir)
	return nil
}

func ensureElasticPack(packDir, logPath string) error {
	if _, err := os.Stat(filepath.Join(packDir, "docker-compose.yml")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := elastic.InstallPack(packDir, logPath); err != nil {
		return err
	}
	fmt.Printf("Elasticsearch content pack written to %s\n", packDir)
	return nil
}

func ensureLogFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	return file.Close()
}

func runDockerCompose(ctx context.Context, dir string, env []string, args ...string) error {
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		return fmt.Errorf("docker-compose.yml not found in %s: %w", dir, err)
	}
	fullArgs := append([]string{"compose"}, args...)
	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func envDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

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
	return dashboard.ListenAndServe(dashboard.Options{
		Addr:     endpointOpts.dashboardAddr,
		LogPath:  cfg.LogPath,
		UserMode: cfg.UserMode,
	})
}

func runEndpointInstall(cmd *cobra.Command, args []string) error {
	result, err := lifecycle.Install(lifecycle.InstallOptions{
		UserMode:              endpointUserMode(),
		LogPath:               endpointOpts.logPath,
		Harnesses:             splitCSV(endpointOpts.harnesses),
		GRPCPort:              endpointOpts.grpcPort,
		HTTPPort:              endpointOpts.httpPort,
		CollectorPath:         endpointOpts.collectorPath,
		StartService:          !endpointOpts.noStart,
		ContentRetention:      endpointconfig.ContentRetention(endpointOpts.contentRetention),
		IncludeRuntimeMetrics: endpointOpts.includeRuntimeMetrics,
		SplunkHEC:             splunkHECOptions(),
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
	if err := lifecycle.Uninstall(lifecycle.UninstallOptions{UserMode: endpointUserMode(), LogPath: endpointOpts.logPath, KeepLogs: endpointOpts.keepLogs, KeepConfig: endpointOpts.keepConfig}); err != nil {
		return err
	}
	fmt.Println("Endpoint service, config, and managed files removed.")
	return nil
}

func runEndpointRepair(cmd *cobra.Command, args []string) error {
	result, err := lifecycle.Repair(lifecycle.InstallOptions{
		UserMode:              endpointUserMode(),
		LogPath:               endpointOpts.logPath,
		Harnesses:             splitCSV(endpointOpts.harnesses),
		GRPCPort:              endpointOpts.grpcPort,
		HTTPPort:              endpointOpts.httpPort,
		CollectorPath:         endpointOpts.collectorPath,
		StartService:          !endpointOpts.noStart,
		ContentRetention:      endpointconfig.ContentRetention(endpointOpts.contentRetention),
		IncludeRuntimeMetrics: endpointOpts.includeRuntimeMetrics,
		SplunkHEC:             splunkHECOptions(),
	})
	if err != nil {
		return err
	}
	fmt.Printf("Endpoint repaired. Manifest: %s\n", result.ManifestPath)
	return nil
}

func loadOrDefaultConfig() endpointconfig.Config {
	userMode := endpointUserMode()
	if cfg, err := endpointconfig.Load(userMode); err == nil {
		if endpointOpts.logPath != "" {
			cfg.LogPath = endpointOpts.logPath
		}
		return cfg
	}
	logPath := endpointOpts.logPath
	if logPath == "" {
		logPath = writer.DefaultPath(userMode)
	}
	return endpointconfig.Default(userMode, logPath)
}

func endpointUserMode() bool {
	return endpointOpts.userMode && !endpointOpts.systemMode
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

func registerSplunkFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&endpointOpts.splunkHECEndpoint, "splunk-hec-endpoint", "", "Splunk HEC endpoint URL")
	cmd.Flags().StringVar(&endpointOpts.splunkHECToken, "splunk-hec-token", "", "Splunk HEC token")
	cmd.Flags().StringVar(&endpointOpts.splunkIndex, "splunk-index", "", "Optional Splunk index")
	cmd.Flags().StringVar(&endpointOpts.splunkSource, "splunk-source", endpointconfig.DefaultSplunkSource, "Optional Splunk source")
	cmd.Flags().StringVar(&endpointOpts.splunkSourcetype, "splunk-sourcetype", endpointconfig.DefaultSplunkSourcetype, "Optional Splunk sourcetype")
	cmd.Flags().BoolVar(&endpointOpts.splunkInsecureSkipVerify, "splunk-insecure-skip-verify", false, "Skip Splunk HEC TLS certificate verification")
	cmd.Flags().StringVar(&endpointOpts.splunkCAFile, "splunk-ca-file", "", "Optional CA certificate path for Splunk HEC TLS verification")
}

func splunkHECOptions() *endpointconfig.SplunkHEC {
	if endpointOpts.splunkHECEndpoint == "" &&
		endpointOpts.splunkHECToken == "" &&
		endpointOpts.splunkIndex == "" &&
		endpointOpts.splunkSource == endpointconfig.DefaultSplunkSource &&
		endpointOpts.splunkSourcetype == endpointconfig.DefaultSplunkSourcetype &&
		!endpointOpts.splunkInsecureSkipVerify &&
		endpointOpts.splunkCAFile == "" {
		return nil
	}
	return &endpointconfig.SplunkHEC{
		Endpoint:           endpointOpts.splunkHECEndpoint,
		Token:              endpointOpts.splunkHECToken,
		Index:              endpointOpts.splunkIndex,
		Source:             endpointOpts.splunkSource,
		Sourcetype:         endpointOpts.splunkSourcetype,
		InsecureSkipVerify: endpointOpts.splunkInsecureSkipVerify,
		CAFile:             endpointOpts.splunkCAFile,
	}
}
