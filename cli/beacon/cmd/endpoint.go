package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/cowork"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/openclaw"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/vscode"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

var endpointOpts struct {
	userMode              bool
	systemMode            bool
	logPath               string
	harnesses             string
	hookHarnesses         string
	outputDir             string
	jsonOutput            bool
	grpcPort              int
	httpPort              int
	collectorPath         string
	includeRuntimeMetrics bool
	includeCodexSpans     bool
	keepLogs              bool
	keepConfig            bool
	noStart               bool
	dryRun                bool
	fix                   bool
	allTargets            bool
	elasticPackDir        string
	hookLevel             string
	contentRetention      string
	includeEventSummaries bool
	includeRawEvents      bool
}

// inventoryOpts holds the flags for the `endpoint inventory` and inventory heartbeat
// subcommands.
var inventoryOpts struct {
	writeEvent      bool
	mcp             bool
	skills          bool
	hooks           bool
	heartbeatForce  bool
	heartbeatConfig string
	workingDir      string
	trigger         string
	triggerHarness  string
}

// dashboardOpts holds the flags for the `endpoint dashboard` command. It is a per-command
// options struct (rather than another field group on the monolithic endpointOpts) as the
// first step of migrating CLI flags off the shared global.
var dashboardOpts struct {
	addr string
	open bool
}

// coworkOpts holds the flags for the `endpoint claude-cowork` subcommands.
var coworkOpts struct {
	headers            string
	endpoint           string
	resourceAttributes string
	ngrok              bool
	open               bool
	since              string
}

// splunkOpts and falconOpts hold the Splunk and Falcon LogScale HEC destination flags.
var splunkOpts struct {
	hecEndpoint        string
	hecToken           string
	index              string
	source             string
	sourcetype         string
	insecureSkipVerify bool
	caFile             string
}

var falconOpts struct {
	hecEndpoint        string
	hecToken           string
	index              string
	source             string
	sourcetype         string
	insecureSkipVerify bool
	caFile             string
}

// openClawOpts and vscodeOpts hold the flags for the openclaw and vscode integration
// print-config/validate subcommands.
var openClawOpts struct {
	endpoint string
	since    string
}

var vscodeOpts struct {
	endpoint       string
	since          string
	workspace      string
	captureContent bool
}

var endpointCmd = &cobra.Command{
	Use:   "endpoint",
	Short: "Manage the local Beacon endpoint agent",
	Long: `Manage the open-source Beacon endpoint agent for local AI runtime
discovery, telemetry collection, and Wazuh-compatible JSON logs.`,
}

var endpointDoctorCmd = &cobra.Command{
	Use:          "doctor",
	Short:        "Run local endpoint health checks",
	SilenceUsage: true,
	RunE:         runEndpointDoctor,
}

var endpointInventoryCmd = &cobra.Command{
	Use:          "inventory",
	Short:        "Show installed, configured, and observed endpoint inventory",
	SilenceUsage: true,
	RunE:         runEndpointInventory,
}

var endpointInventoryHeartbeatCmd = &cobra.Command{
	Use:          "heartbeat",
	Short:        "Write endpoint inventory heartbeat events",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runEndpointInventoryHeartbeat,
}

var endpointTestEventCmd = &cobra.Command{
	Use:          "test-event",
	Aliases:      []string{"validate-pipeline"},
	Short:        "Write a synthetic endpoint validation event",
	SilenceUsage: true,
	RunE:         runEndpointTestEvent,
}

var endpointBundleDiagnosticsCmd = &cobra.Command{
	Use:          "bundle-diagnostics",
	Short:        "Write a redacted local diagnostics bundle",
	SilenceUsage: true,
	RunE:         runEndpointBundleDiagnostics,
}

var endpointIntegrationsCmd = &cobra.Command{
	Use:   "integrations",
	Short: "Manage admin-configured endpoint integrations",
}

var endpointIntegrationsValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate admin-configured endpoint integrations",
	SilenceUsage: true,
	RunE:         runEndpointIntegrationsValidate,
}

var endpointConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and safely update endpoint configuration",
}

var endpointConfigShowCmd = &cobra.Command{
	Use:          "show",
	Short:        "Print endpoint configuration with secrets redacted",
	SilenceUsage: true,
	RunE:         runEndpointConfigShow,
}

var endpointConfigValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate endpoint configuration",
	SilenceUsage: true,
	RunE:         runEndpointConfigValidate,
}

var topLevelDoctorCmd = &cobra.Command{
	Use:          "doctor",
	Short:        "Alias for beacon endpoint doctor",
	SilenceUsage: true,
	RunE:         runEndpointDoctor,
}

var topLevelStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Alias for beacon endpoint status",
	SilenceUsage: true,
	RunE:         runEndpointStatus,
}

var topLevelInventoryCmd = &cobra.Command{
	Use:          "inventory",
	Short:        "Alias for beacon endpoint inventory",
	SilenceUsage: true,
	RunE:         runEndpointInventory,
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
		endpoint := coworkOpts.endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
		}
		fmt.Print(cowork.PrintConfig(cowork.Config{
			Endpoint:           endpoint,
			Protocol:           "HTTP/protobuf",
			Headers:            coworkOpts.headers,
			ResourceAttributes: coworkOpts.resourceAttributes,
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

var endpointOpenClawCmd = &cobra.Command{
	Use:   "openclaw",
	Short: "Manage OpenClaw Gateway OpenTelemetry integration",
}

var endpointOpenClawPrintConfigCmd = &cobra.Command{
	Use:   "print-config",
	Short: "Print OpenClaw Gateway OTLP setup guidance",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		endpoint := openClawOpts.endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
		}
		fmt.Print(openclaw.PrintConfig(openclaw.Config{
			Endpoint:    endpoint,
			Protocol:    "http/protobuf",
			ServiceName: "openclaw-gateway",
		}))
	},
}

var endpointOpenClawStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show OpenClaw Gateway endpoint integration status",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		status := openclaw.GetStatus(cfg.LogPath)
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(status)
			return
		}
		fmt.Printf("%s: observed=%t", status.DisplayName, status.LastEventObserved)
		if status.LastEventObservedAt != "" {
			fmt.Printf(" last=%s", status.LastEventObservedAt)
		}
		fmt.Println()
		fmt.Println(status.Message)
	},
}

var endpointOpenClawValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate whether OpenClaw OTLP-derived events are arriving",
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runEndpointOpenClawValidate() },
}

var endpointVSCodeCmd = &cobra.Command{
	Use:   "vscode",
	Short: "Manage VS Code Copilot OpenTelemetry integration",
}

var endpointVSCodePrintConfigCmd = &cobra.Command{
	Use:   "print-config",
	Short: "Print VS Code Copilot OpenTelemetry setup guidance",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		endpoint := vscodeOpts.endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
		}
		fmt.Print(vscode.PrintConfig(vscode.Config{
			Endpoint:       endpoint,
			CaptureContent: vscodeOpts.captureContent,
			WorkspacePath:  vscodeOpts.workspace,
		}))
	},
}

var endpointVSCodeSetupCmd = &cobra.Command{
	Use:          "setup",
	Short:        "Configure VS Code Copilot OpenTelemetry for local Beacon collection",
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runEndpointVSCodeSetup() },
}

var endpointVSCodeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show VS Code endpoint integration status",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadOrDefaultConfig()
		endpoint := vscodeOpts.endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
		}
		status := vscode.GetStatusForConfig(cfg.LogPath, endpoint, vscode.Config{
			WorkspacePath: vscodeOpts.workspace,
		})
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(status)
			return
		}
		fmt.Printf("%s: detected=%t telemetry=%s", status.DisplayName, status.Detected, status.TelemetryStatus)
		if status.LastEventObservedAt != "" {
			fmt.Printf(" last=%s", status.LastEventObservedAt)
		}
		fmt.Println()
		fmt.Println(status.Message)
	},
}

var endpointVSCodeValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate whether VS Code events are arriving",
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return runEndpointVSCodeValidate() },
}

func init() {
	rootCmd.AddCommand(endpointCmd)
	rootCmd.AddCommand(topLevelDoctorCmd)
	rootCmd.AddCommand(topLevelStatusCmd)
	rootCmd.AddCommand(topLevelInventoryCmd)

	endpointCmd.AddCommand(endpointInstallCmd)
	endpointCmd.AddCommand(endpointStatusCmd)
	endpointCmd.AddCommand(endpointDoctorCmd)
	endpointCmd.AddCommand(endpointInventoryCmd)
	endpointCmd.AddCommand(endpointDiscoverCmd)
	endpointCmd.AddCommand(endpointTestEventCmd)
	endpointCmd.AddCommand(endpointBundleDiagnosticsCmd)
	endpointCmd.AddCommand(endpointUninstallCmd)
	endpointCmd.AddCommand(endpointRepairCmd)
	endpointCmd.AddCommand(endpointDashboardCmd)
	for _, c := range buildDestinationCommands() {
		endpointCmd.AddCommand(c)
	}
	endpointCmd.AddCommand(endpointIntegrationsCmd)
	endpointCmd.AddCommand(endpointHooksCmd)
	endpointCmd.AddCommand(endpointConfigCmd)
	endpointInventoryCmd.AddCommand(endpointInventoryHeartbeatCmd)
	endpointConfigCmd.AddCommand(endpointConfigShowCmd)
	endpointConfigCmd.AddCommand(endpointConfigValidateCmd)
	endpointIntegrationsCmd.AddCommand(endpointIntegrationsValidateCmd)
	endpointIntegrationsCmd.AddCommand(endpointCoworkCmd)
	endpointIntegrationsCmd.AddCommand(endpointOpenClawCmd)
	endpointIntegrationsCmd.AddCommand(endpointVSCodeCmd)
	endpointHooksCmd.AddCommand(endpointHooksInstallCmd)
	endpointHooksCmd.AddCommand(endpointHooksUninstallCmd)
	endpointHooksCmd.AddCommand(endpointHooksStatusCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkPrintConfigCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkSetupCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkStatusCmd)
	endpointCoworkCmd.AddCommand(endpointCoworkValidateCmd)
	endpointOpenClawCmd.AddCommand(endpointOpenClawPrintConfigCmd)
	endpointOpenClawCmd.AddCommand(endpointOpenClawStatusCmd)
	endpointOpenClawCmd.AddCommand(endpointOpenClawValidateCmd)
	endpointVSCodeCmd.AddCommand(endpointVSCodePrintConfigCmd)
	endpointVSCodeCmd.AddCommand(endpointVSCodeSetupCmd)
	endpointVSCodeCmd.AddCommand(endpointVSCodeStatusCmd)
	endpointVSCodeCmd.AddCommand(endpointVSCodeValidateCmd)

	for _, c := range []*cobra.Command{endpointInstallCmd, endpointStatusCmd, endpointDoctorCmd, endpointInventoryCmd, endpointInventoryHeartbeatCmd, endpointDiscoverCmd, endpointTestEventCmd, endpointBundleDiagnosticsCmd, endpointUninstallCmd, endpointRepairCmd, endpointConfigShowCmd, endpointConfigValidateCmd, endpointIntegrationsValidateCmd, topLevelDoctorCmd, topLevelStatusCmd, topLevelInventoryCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}

	endpointInstallCmd.Flags().StringVar(&endpointOpts.harnesses, "harness", "claude,codex", "Comma-separated harnesses to configure")
	endpointInstallCmd.Flags().IntVar(&endpointOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	endpointInstallCmd.Flags().IntVar(&endpointOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	endpointInstallCmd.Flags().StringVar(&endpointOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.includeRuntimeMetrics, "include-runtime-metrics", false, "Include generic process/runtime OTLP metrics and harness operational metrics (OpenClaw, Copilot CLI) in the runtime JSONL log")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.includeCodexSpans, "include-codex-spans", false, "Include high-volume Codex OTLP spans for troubleshooting")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.noStart, "no-start", false, "Write files without starting the launchd service")
	endpointInstallCmd.Flags().BoolVar(&endpointOpts.dryRun, "dry-run", false, "Print planned actions without changing endpoint files or services")
	endpointInstallCmd.Flags().StringVar(&endpointOpts.contentRetention, "content-retention", "", "Deprecated no-op; Beacon always captures full content subject to redaction and size limits")
	_ = endpointInstallCmd.Flags().MarkHidden("content-retention")
	_ = endpointInstallCmd.Flags().MarkDeprecated("content-retention", "Beacon now always captures full content; this flag is ignored")
	registerSplunkFlags(endpointInstallCmd)
	registerFalconFlags(endpointInstallCmd)
	endpointRepairCmd.Flags().StringVar(&endpointOpts.harnesses, "harness", "claude,codex", "Comma-separated harnesses to configure")
	endpointRepairCmd.Flags().IntVar(&endpointOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	endpointRepairCmd.Flags().IntVar(&endpointOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
	endpointRepairCmd.Flags().BoolVar(&endpointOpts.includeRuntimeMetrics, "include-runtime-metrics", false, "Include generic process/runtime OTLP metrics and harness operational metrics (OpenClaw, Copilot CLI) in the runtime JSONL log")
	endpointRepairCmd.Flags().BoolVar(&endpointOpts.includeCodexSpans, "include-codex-spans", false, "Include high-volume Codex OTLP spans for troubleshooting")
	endpointRepairCmd.Flags().BoolVar(&endpointOpts.noStart, "no-start", false, "Write files without starting the launchd service")
	endpointRepairCmd.Flags().BoolVar(&endpointOpts.dryRun, "dry-run", false, "Print planned actions without changing endpoint files or services")
	endpointRepairCmd.Flags().StringVar(&endpointOpts.contentRetention, "content-retention", "", "Deprecated no-op; Beacon always captures full content subject to redaction and size limits")
	_ = endpointRepairCmd.Flags().MarkHidden("content-retention")
	_ = endpointRepairCmd.Flags().MarkDeprecated("content-retention", "Beacon now always captures full content; this flag is ignored")
	registerSplunkFlags(endpointRepairCmd)
	registerFalconFlags(endpointRepairCmd)
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointDashboardCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	endpointDashboardCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	endpointDashboardCmd.Flags().StringVar(&dashboardOpts.addr, "addr", dashboard.DefaultAddr, "Local dashboard listen address")
	endpointDashboardCmd.Flags().BoolVar(&dashboardOpts.open, "open", false, "Open the dashboard in a browser")

	endpointDiscoverCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print discovery as JSON")
	endpointDiscoverCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Discover all supported runtime targets")
	endpointStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	endpointDoctorCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print doctor results as JSON")
	endpointDoctorCmd.Flags().BoolVar(&endpointOpts.fix, "fix", false, "Apply safe endpoint doctor remediations")
	endpointInventoryCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print inventory as JSON")
	endpointInventoryCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Include all supported targets")
	endpointInventoryCmd.Flags().BoolVar(&inventoryOpts.mcp, "mcp", false, "Show only MCP server inventory and source configs")
	endpointInventoryCmd.Flags().BoolVar(&inventoryOpts.skills, "skills", false, "Show only local agent skill inventory")
	endpointInventoryCmd.Flags().BoolVar(&inventoryOpts.hooks, "hooks", false, "Show only hook integration and hook config inventory")
	endpointInventoryCmd.Flags().BoolVar(&inventoryOpts.writeEvent, "write-event", false, "Append inventory events to the endpoint runtime log")
	endpointInventoryHeartbeatCmd.Flags().BoolVar(&inventoryOpts.heartbeatForce, "force", false, "Write inventory heartbeat even when TTL has not expired")
	endpointInventoryHeartbeatCmd.Flags().StringVar(&inventoryOpts.heartbeatConfig, "config", "", "Endpoint config path")
	endpointInventoryHeartbeatCmd.Flags().StringVar(&inventoryOpts.workingDir, "working-dir", "", "Working directory for project inventory")
	endpointInventoryHeartbeatCmd.Flags().StringVar(&inventoryOpts.trigger, "trigger", "manual", "Inventory trigger source")
	endpointInventoryHeartbeatCmd.Flags().StringVar(&inventoryOpts.triggerHarness, "trigger-harness", "", "Harness that triggered inventory")
	endpointInventoryHeartbeatCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print heartbeat result as JSON")
	topLevelDoctorCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print doctor results as JSON")
	topLevelDoctorCmd.Flags().BoolVar(&endpointOpts.fix, "fix", false, "Apply safe endpoint doctor remediations")
	topLevelStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	topLevelInventoryCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print inventory as JSON")
	topLevelInventoryCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Include all supported targets")
	topLevelInventoryCmd.Flags().BoolVar(&inventoryOpts.mcp, "mcp", false, "Show only MCP server inventory and source configs")
	topLevelInventoryCmd.Flags().BoolVar(&inventoryOpts.skills, "skills", false, "Show only local agent skill inventory")
	topLevelInventoryCmd.Flags().BoolVar(&inventoryOpts.hooks, "hooks", false, "Show only hook integration and hook config inventory")
	topLevelInventoryCmd.Flags().BoolVar(&inventoryOpts.writeEvent, "write-event", false, "Append inventory events to the endpoint runtime log")
	endpointTestEventCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print validation stages as JSON")
	endpointBundleDiagnosticsCmd.Flags().StringVar(&endpointOpts.outputDir, "output", "", "Output directory for diagnostics bundle")
	endpointBundleDiagnosticsCmd.Flags().BoolVar(&endpointOpts.includeEventSummaries, "include-event-summaries", false, "Include redacted event summaries")
	endpointBundleDiagnosticsCmd.Flags().BoolVar(&endpointOpts.includeRawEvents, "include-raw-events", false, "Include raw runtime JSONL events")
	endpointIntegrationsValidateCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Validate all supported admin integrations")
	endpointIntegrationsValidateCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print validation as JSON")
	for _, c := range []*cobra.Command{endpointCoworkPrintConfigCmd, endpointCoworkSetupCmd, endpointCoworkStatusCmd, endpointCoworkValidateCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}
	endpointCoworkPrintConfigCmd.Flags().StringVar(&coworkOpts.headers, "headers", "", "Optional OTLP headers to show in setup guidance")
	endpointCoworkPrintConfigCmd.Flags().StringVar(&coworkOpts.endpoint, "endpoint", "", "Public OTLP HTTPS endpoint to show in setup guidance")
	endpointCoworkPrintConfigCmd.Flags().StringVar(&coworkOpts.resourceAttributes, "resource-attributes", "", "Optional Claude Cowork resource attributes")
	endpointCoworkSetupCmd.Flags().StringVar(&coworkOpts.endpoint, "endpoint", "", "Public OTLP HTTPS endpoint reachable by Claude Cowork")
	endpointCoworkSetupCmd.Flags().StringVar(&coworkOpts.headers, "headers", "", "Optional OTLP headers for the Claude admin settings")
	endpointCoworkSetupCmd.Flags().StringVar(&coworkOpts.resourceAttributes, "resource-attributes", "", "Optional Claude Cowork resource attributes")
	endpointCoworkSetupCmd.Flags().BoolVar(&coworkOpts.ngrok, "ngrok", false, "Create a temporary authenticated ngrok tunnel to the local OTLP HTTP receiver")
	endpointCoworkSetupCmd.Flags().BoolVar(&coworkOpts.open, "open", false, "Open Claude Cowork admin settings in a browser")
	endpointCoworkValidateCmd.Flags().StringVar(&coworkOpts.headers, "headers", "", "Optional OTLP headers to show when validation fails")
	endpointCoworkValidateCmd.Flags().StringVar(&coworkOpts.endpoint, "endpoint", "", "Public OTLP HTTPS endpoint to show when validation fails")
	endpointCoworkValidateCmd.Flags().StringVar(&coworkOpts.resourceAttributes, "resource-attributes", "", "Optional Claude Cowork resource attributes")
	endpointCoworkValidateCmd.Flags().StringVar(&coworkOpts.since, "since", "", "Require a Claude Cowork event within this duration, such as 10m")
	endpointCoworkStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	for _, c := range []*cobra.Command{endpointOpenClawPrintConfigCmd, endpointOpenClawStatusCmd, endpointOpenClawValidateCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	}
	endpointOpenClawPrintConfigCmd.Flags().StringVar(&openClawOpts.endpoint, "endpoint", "", "OTLP HTTP endpoint to show in setup guidance")
	endpointOpenClawValidateCmd.Flags().StringVar(&openClawOpts.endpoint, "endpoint", "", "OTLP HTTP endpoint to show when validation fails")
	endpointOpenClawValidateCmd.Flags().StringVar(&openClawOpts.since, "since", "", "Require an OpenClaw event within this duration, such as 10m")
	endpointOpenClawStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	for _, c := range []*cobra.Command{endpointVSCodePrintConfigCmd, endpointVSCodeSetupCmd, endpointVSCodeStatusCmd, endpointVSCodeValidateCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().StringVar(&vscodeOpts.endpoint, "endpoint", "", "OTLP HTTP endpoint for VS Code Copilot")
		c.Flags().StringVar(&vscodeOpts.workspace, "workspace", "", "Workspace path for .vscode/settings.json")
		c.Flags().BoolVar(&vscodeOpts.captureContent, "capture-content", false, "Enable full Copilot prompt, response, tool argument, and tool result capture")
	}
	endpointVSCodeSetupCmd.Flags().BoolVar(&endpointOpts.dryRun, "dry-run", false, "Print VS Code setup guidance without changing settings")
	endpointVSCodeValidateCmd.Flags().StringVar(&vscodeOpts.since, "since", "", "Require a VS Code event within this duration, such as 10m")
	endpointVSCodeStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	for _, c := range []*cobra.Command{endpointHooksInstallCmd, endpointHooksUninstallCmd, endpointHooksStatusCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().StringVar(&endpointOpts.hookHarnesses, "harness", "cursor", "Comma-separated hook harnesses")
		c.Flags().StringVar(&endpointOpts.hookLevel, "level", "user", "Hook install level: user or project")
	}
	endpointHooksInstallCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Target all supported hook harnesses")
	endpointHooksInstallCmd.Flags().BoolVar(&endpointOpts.dryRun, "dry-run", false, "Print planned hook actions without changing files")
	endpointHooksUninstallCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Target all supported hook harnesses")
	endpointHooksUninstallCmd.Flags().BoolVar(&endpointOpts.dryRun, "dry-run", false, "Print planned hook actions without changing files")
	endpointHooksStatusCmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print status as JSON")
	endpointHooksStatusCmd.Flags().BoolVar(&endpointOpts.allTargets, "all", false, "Show all supported hook harnesses")
	endpointUninstallCmd.Flags().BoolVar(&endpointOpts.keepLogs, "keep-logs", false, "Keep runtime logs during uninstall")
	endpointUninstallCmd.Flags().BoolVar(&endpointOpts.keepConfig, "keep-config", false, "Keep harness telemetry configuration during uninstall")
	endpointUninstallCmd.Flags().BoolVar(&endpointOpts.dryRun, "dry-run", false, "Print planned actions without changing endpoint files or services")
}

func runEndpointOpenClawValidate() error {
	cfg := loadOrDefaultConfig()
	setup := func() {
		endpoint := openClawOpts.endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
		}
		fmt.Print(openclaw.PrintConfig(openclaw.Config{
			Endpoint:    endpoint,
			Protocol:    "http/protobuf",
			ServiceName: "openclaw-gateway",
		}))
	}
	if openClawOpts.since != "" {
		duration, err := time.ParseDuration(openClawOpts.since)
		if err != nil {
			return fmt.Errorf("--since must be a duration such as 10m: %w", err)
		}
		since := time.Now().Add(-duration)
		if !openclaw.HasOpenClawEventSince(cfg.LogPath, since) {
			setup()
			return fmt.Errorf("no OpenClaw OTLP-derived events observed in %s since %s", cfg.LogPath, since.UTC().Format(time.RFC3339))
		}
		fmt.Printf("OpenClaw OTLP-derived events observed in endpoint runtime log since %s.\n", since.UTC().Format(time.RFC3339))
		fmt.Println("Validation confirms at least one OpenClaw event reached Beacon; it does not prove logs, traces, and metrics are each flowing.")
		return nil
	}
	status := openclaw.GetStatus(cfg.LogPath)
	if !status.LastEventObserved {
		setup()
		return fmt.Errorf("no OpenClaw OTLP-derived events observed in %s", cfg.LogPath)
	}
	if status.LastEventObservedAt != "" {
		fmt.Printf("OpenClaw OTLP-derived events observed in endpoint runtime log. Last observed: %s.\n", status.LastEventObservedAt)
	} else {
		fmt.Println("OpenClaw OTLP-derived events observed in endpoint runtime log.")
	}
	fmt.Println("Validation confirms at least one OpenClaw event reached Beacon; it does not prove logs, traces, and metrics are each flowing.")
	return nil
}

func runEndpointVSCodeSetup() error {
	cfg := loadOrDefaultConfig()
	endpoint := vscodeOpts.endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
	}
	setup := vscode.Config{
		Endpoint:       endpoint,
		CaptureContent: vscodeOpts.captureContent,
		WorkspacePath:  vscodeOpts.workspace,
	}
	if endpointOpts.dryRun {
		fmt.Print(vscode.PrintConfig(setup))
		return nil
	}
	path, err := vscode.Setup(setup)
	if err != nil {
		return err
	}
	fmt.Printf("VS Code Copilot OTel settings written to %s\n", path)
	return nil
}

func runEndpointVSCodeValidate() error {
	cfg := loadOrDefaultConfig()
	endpoint := vscodeOpts.endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.HTTPPort)
	}
	setup := func() {
		fmt.Print(vscode.PrintConfig(vscode.Config{
			Endpoint:       endpoint,
			CaptureContent: vscodeOpts.captureContent,
			WorkspacePath:  vscodeOpts.workspace,
		}))
	}
	if vscodeOpts.since != "" {
		duration, err := time.ParseDuration(vscodeOpts.since)
		if err != nil {
			return fmt.Errorf("--since must be a duration such as 10m: %w", err)
		}
		since := time.Now().Add(-duration)
		if !vscode.HasVSCodeEventSince(cfg.LogPath, since) {
			setup()
			return fmt.Errorf("no VS Code events observed in %s since %s", cfg.LogPath, since.UTC().Format(time.RFC3339))
		}
		fmt.Printf("VS Code events observed in endpoint runtime log since %s.\n", since.UTC().Format(time.RFC3339))
		fmt.Println("Validation confirms at least one low-noise VS Code event reached Beacon.")
		return nil
	}
	status := vscode.GetStatusForConfig(cfg.LogPath, endpoint, vscode.Config{
		WorkspacePath: vscodeOpts.workspace,
	})
	if !status.LastEventObserved {
		setup()
		return fmt.Errorf("no VS Code events observed in %s", cfg.LogPath)
	}
	if status.LastEventObservedAt != "" {
		fmt.Printf("VS Code events observed in endpoint runtime log. Last observed: %s.\n", status.LastEventObservedAt)
	} else {
		fmt.Println("VS Code events observed in endpoint runtime log.")
	}
	fmt.Println("Validation confirms at least one low-noise VS Code event reached Beacon.")
	return nil
}

func writeValidationEvent(cfg endpointconfig.Config, destination string) (string, error) {
	return writer.AppendEvent(syntheticEvent(destination), writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode})
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

func loadConfigForMode(userMode bool, logPath string) endpointconfig.Config {
	if cfg, err := endpointconfig.Load(userMode); err == nil {
		if logPath != "" {
			cfg.LogPath = logPath
		}
		return cfg
	}
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

func splitHarnessCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	return splitCSV(value)
}

func registerSplunkFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&splunkOpts.hecEndpoint, "splunk-hec-endpoint", "", "Splunk HEC endpoint URL")
	cmd.Flags().StringVar(&splunkOpts.hecToken, "splunk-hec-token", "", "Splunk HEC token")
	cmd.Flags().StringVar(&splunkOpts.index, "splunk-index", "", "Optional Splunk index")
	cmd.Flags().StringVar(&splunkOpts.source, "splunk-source", endpointconfig.DefaultSplunkSource, "Optional Splunk source")
	cmd.Flags().StringVar(&splunkOpts.sourcetype, "splunk-sourcetype", endpointconfig.DefaultSplunkSourcetype, "Optional Splunk sourcetype")
	cmd.Flags().BoolVar(&splunkOpts.insecureSkipVerify, "splunk-insecure-skip-verify", false, "Skip Splunk HEC TLS certificate verification")
	cmd.Flags().StringVar(&splunkOpts.caFile, "splunk-ca-file", "", "Optional CA certificate path for Splunk HEC TLS verification")
}

func registerFalconFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&falconOpts.hecEndpoint, "falcon-hec-endpoint", "", "Falcon LogScale HEC endpoint URL")
	cmd.Flags().StringVar(&falconOpts.hecToken, "falcon-hec-token", "", "Falcon LogScale ingest token")
	cmd.Flags().StringVar(&falconOpts.index, "falcon-index", "", "Optional Falcon LogScale repository for multi-repository tokens")
	cmd.Flags().StringVar(&falconOpts.source, "falcon-source", endpointconfig.DefaultFalconSource, "Optional Falcon LogScale source")
	cmd.Flags().StringVar(&falconOpts.sourcetype, "falcon-sourcetype", endpointconfig.DefaultFalconSourcetype, "Optional Falcon LogScale parser or sourcetype")
	cmd.Flags().BoolVar(&falconOpts.insecureSkipVerify, "falcon-insecure-skip-verify", false, "Skip Falcon LogScale HEC TLS certificate verification")
	cmd.Flags().StringVar(&falconOpts.caFile, "falcon-ca-file", "", "Optional CA certificate path for Falcon LogScale HEC TLS verification")
}

func splunkHECOptions() *endpointconfig.SplunkHEC {
	if splunkOpts.hecEndpoint == "" &&
		splunkOpts.hecToken == "" &&
		splunkOpts.index == "" &&
		splunkOpts.source == endpointconfig.DefaultSplunkSource &&
		splunkOpts.sourcetype == endpointconfig.DefaultSplunkSourcetype &&
		!splunkOpts.insecureSkipVerify &&
		splunkOpts.caFile == "" {
		return nil
	}
	return &endpointconfig.SplunkHEC{
		Endpoint:           splunkOpts.hecEndpoint,
		Token:              splunkOpts.hecToken,
		Index:              splunkOpts.index,
		Source:             splunkOpts.source,
		Sourcetype:         splunkOpts.sourcetype,
		InsecureSkipVerify: splunkOpts.insecureSkipVerify,
		CAFile:             splunkOpts.caFile,
	}
}

func falconHECOptions() *endpointconfig.FalconHEC {
	if falconOpts.hecEndpoint == "" &&
		falconOpts.hecToken == "" &&
		falconOpts.index == "" &&
		falconOpts.source == endpointconfig.DefaultFalconSource &&
		falconOpts.sourcetype == endpointconfig.DefaultFalconSourcetype &&
		!falconOpts.insecureSkipVerify &&
		falconOpts.caFile == "" {
		return nil
	}
	return &endpointconfig.FalconHEC{
		Endpoint:           falconOpts.hecEndpoint,
		Token:              falconOpts.hecToken,
		Index:              falconOpts.index,
		Source:             falconOpts.source,
		Sourcetype:         falconOpts.sourcetype,
		InsecureSkipVerify: falconOpts.insecureSkipVerify,
		CAFile:             falconOpts.caFile,
	}
}
