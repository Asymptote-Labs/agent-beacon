package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
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
