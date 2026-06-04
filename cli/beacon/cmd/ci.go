package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	beaconci "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ci"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

var ciOpts struct {
	baseDir          string
	logPath          string
	workDir          string
	collectorPath    string
	harness          string
	contentRetention string
	grpcPort         int
	httpPort         int
	jsonOutput       bool
	keepArtifacts    bool
	minEvents        int
	forward          string
	forwardEndpoint  string
	requireTelemetry bool
}

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "Run ephemeral AI runtime telemetry collection in CI",
	Long: `Run Beacon telemetry collection for a single CI job without installing a
persistent endpoint service or modifying user harness configuration.`,
}

var ciExecCmd = &cobra.Command{
	Use:          "exec [--harness claude] -- <command> [args...]",
	Short:        "Run a command with Claude Code telemetry captured for CI",
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE:         runCIExec,
}

var ciValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate CI runtime telemetry artifacts",
	SilenceUsage: true,
	RunE:         runCIValidate,
}

func init() {
	rootCmd.AddCommand(ciCmd)
	ciCmd.AddCommand(ciExecCmd)
	ciCmd.AddCommand(ciValidateCmd)
	for _, cmd := range []*cobra.Command{ciExecCmd, ciValidateCmd} {
		cmd.Flags().StringVar(&ciOpts.logPath, "log-path", "", "CI runtime JSONL log path")
		cmd.Flags().StringVar(&ciOpts.harness, "harness", beaconci.DefaultHarness, "CI harness to configure (currently only claude)")
		cmd.Flags().BoolVar(&ciOpts.jsonOutput, "json", false, "Print result as JSON")
		cmd.Flags().IntVar(&ciOpts.minEvents, "min-events", beaconci.DefaultValidationMin, "Minimum matching events required during validation")
	}
	ciExecCmd.Flags().StringVar(&ciOpts.baseDir, "base-dir", "", "CI session base directory (defaults to $RUNNER_TEMP/beacon or a temp directory)")
	ciExecCmd.Flags().StringVar(&ciOpts.workDir, "work-dir", "", "Working directory for the child command")
	ciExecCmd.Flags().StringVar(&ciOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
	ciExecCmd.Flags().IntVar(&ciOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
	ciExecCmd.Flags().IntVar(&ciOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
	ciExecCmd.Flags().StringVar(&ciOpts.contentRetention, "content-retention", string(endpointconfig.ContentRetentionFull), "Content retention mode: metadata, redacted, or full")
	ciExecCmd.Flags().BoolVar(&ciOpts.keepArtifacts, "keep-artifacts", true, "Keep CI runtime log and collector config after exit")
	ciExecCmd.Flags().StringVar(&ciOpts.forward, "forward", "", "Optionally forward events to a customer-managed SIEM: splunk or falcon (token read from the environment)")
	ciExecCmd.Flags().StringVar(&ciOpts.forwardEndpoint, "forward-endpoint", "", "SIEM HEC endpoint URL for --forward (token comes from BEACON_CI_*_HEC_TOKEN)")
	ciExecCmd.Flags().BoolVar(&ciOpts.requireTelemetry, "require-telemetry", true, "Fail the command when telemetry validation fails; set false to warn only")
	for _, name := range []string{"base-dir", "work-dir", "collector", "otlp-grpc-port", "otlp-http-port"} {
		_ = ciExecCmd.Flags().MarkHidden(name)
	}
}

func runCIExec(cmd *cobra.Command, args []string) error {
	runCtx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	session, err := beaconci.Provision(beaconci.Options{
		BaseDir:          ciOpts.baseDir,
		LogPath:          ciOpts.logPath,
		WorkDir:          ciOpts.workDir,
		CollectorPath:    ciOpts.collectorPath,
		GRPCPort:         ciOpts.grpcPort,
		HTTPPort:         ciOpts.httpPort,
		Harness:          ciOpts.harness,
		ContentRetention: endpointconfig.ContentRetention(ciOpts.contentRetention),
		KeepArtifacts:    ciOpts.keepArtifacts,
		Forward:          ciOpts.forward,
		ForwardEndpoint:  ciOpts.forwardEndpoint,
	})
	if err != nil {
		return err
	}
	if err := session.Start(runCtx, os.Stdout, os.Stderr); err != nil {
		return err
	}
	childExit, childErr := session.RunChild(runCtx, args, os.Stdout, os.Stderr)
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stopErr := session.Stop(stopCtx)
	cancel()
	if stopErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: collector stop: %v\n", stopErr)
	}
	if childErr != nil {
		return childErr
	}
	result := beaconci.Validate(beaconci.ValidationOptions{
		LogPath:        session.LogPath,
		MinEvents:      ciOpts.minEvents,
		RequireHarness: ciOpts.harness,
		Since:          session.StartedAtTime(),
	})
	execResult := beaconci.ExecResult{
		Session:         *session,
		ChildExitCode:   childExit,
		Validation:      result,
		ArtifactMessage: fmt.Sprintf("Beacon CI artifacts: log=%s config=%s", session.LogPath, session.ConfigPath),
	}
	if !ciOpts.keepArtifacts && result.Status != "fail" && childExit == 0 && ciOpts.baseDir == "" && ciOpts.logPath == "" {
		if err := os.RemoveAll(session.BaseDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: artifact cleanup: %v\n", err)
		} else {
			execResult.ArtifactMessage = "Beacon CI artifacts cleaned"
		}
	}
	if ciOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(execResult)
	} else {
		printCIValidation(result)
		fmt.Println(execResult.ArtifactMessage)
	}
	if result.Status == "fail" {
		if ciOpts.requireTelemetry {
			fmt.Fprintln(os.Stderr, "Beacon CI telemetry validation failed")
			if childExit != 0 {
				os.Exit(childExit)
			}
			return fmt.Errorf("Beacon CI telemetry validation failed")
		}
		fmt.Fprintln(os.Stderr, "Warning: Beacon CI telemetry validation failed (continuing because --require-telemetry=false)")
	}
	if childExit != 0 {
		os.Exit(childExit)
	}
	return nil
}

func runCIValidate(cmd *cobra.Command, args []string) error {
	logPath := ciOpts.logPath
	if logPath == "" {
		logPath = beaconci.DefaultLogPath()
	}
	result := beaconci.Validate(beaconci.ValidationOptions{
		LogPath:        logPath,
		MinEvents:      ciOpts.minEvents,
		RequireHarness: ciOpts.harness,
	})
	if ciOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	} else {
		printCIValidation(result)
	}
	if result.Status == "fail" {
		return fmt.Errorf("Beacon CI telemetry validation failed")
	}
	return nil
}

func printCIValidation(result beaconci.ValidationResult) {
	fmt.Printf("Beacon CI validation: %s\n", result.Status)
	fmt.Printf("Runtime log: %s\n", result.LogPath)
	for _, stage := range result.Stages {
		fmt.Printf("%s: %s", stage.Name, stage.Status)
		if stage.Target != "" {
			fmt.Printf(" target=%s", stage.Target)
		}
		if stage.Message != "" {
			fmt.Printf(" (%s)", stage.Message)
		}
		fmt.Println()
	}
}
