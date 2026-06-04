package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	stateFile        string
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

var ciStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a background collector and export Claude telemetry env for later steps",
	Long: `Start an ephemeral collector in the background and export the Claude Code
OpenTelemetry environment variables so subsequent CI steps (including third-party
actions that invoke Claude) emit to it. Stop and validate the session later with
'beacon ci stop'. In GitHub Actions the telemetry variables are appended to
$GITHUB_ENV automatically.`,
	SilenceUsage: true,
	RunE:         runCIStart,
}

var ciStopCmd = &cobra.Command{
	Use:          "stop",
	Short:        "Stop a background collector started by 'beacon ci start' and validate",
	SilenceUsage: true,
	RunE:         runCIStop,
}

func init() {
	rootCmd.AddCommand(ciCmd)
	ciCmd.AddCommand(ciExecCmd)
	ciCmd.AddCommand(ciValidateCmd)
	ciCmd.AddCommand(ciStartCmd)
	ciCmd.AddCommand(ciStopCmd)
	for _, cmd := range []*cobra.Command{ciExecCmd, ciValidateCmd, ciStartCmd, ciStopCmd} {
		cmd.Flags().StringVar(&ciOpts.logPath, "log-path", "", "CI runtime JSONL log path")
		cmd.Flags().StringVar(&ciOpts.harness, "harness", beaconci.DefaultHarness, "CI harness to configure (currently only claude)")
		cmd.Flags().BoolVar(&ciOpts.jsonOutput, "json", false, "Print result as JSON")
	}
	for _, cmd := range []*cobra.Command{ciExecCmd, ciValidateCmd, ciStopCmd} {
		cmd.Flags().IntVar(&ciOpts.minEvents, "min-events", beaconci.DefaultValidationMin, "Minimum matching events required during validation")
	}
	// base-dir is used by exec, start, and stop to locate CI session artifacts.
	for _, cmd := range []*cobra.Command{ciExecCmd, ciStartCmd, ciStopCmd} {
		cmd.Flags().StringVar(&ciOpts.baseDir, "base-dir", "", "CI session base directory (defaults to $RUNNER_TEMP/beacon or a temp directory)")
	}
	// Collector provisioning flags shared by exec (foreground) and start (detached).
	for _, cmd := range []*cobra.Command{ciExecCmd, ciStartCmd} {
		cmd.Flags().StringVar(&ciOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
		cmd.Flags().IntVar(&ciOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
		cmd.Flags().IntVar(&ciOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
		cmd.Flags().StringVar(&ciOpts.contentRetention, "content-retention", string(endpointconfig.ContentRetentionFull), "Content retention mode: metadata, redacted, or full")
		cmd.Flags().StringVar(&ciOpts.forward, "forward", "", "Optionally forward events to a customer-managed SIEM: splunk or falcon (token read from the environment)")
		cmd.Flags().StringVar(&ciOpts.forwardEndpoint, "forward-endpoint", "", "SIEM HEC endpoint URL for --forward (token comes from BEACON_CI_*_HEC_TOKEN)")
	}
	ciExecCmd.Flags().StringVar(&ciOpts.workDir, "work-dir", "", "Working directory for the child command")
	for _, cmd := range []*cobra.Command{ciExecCmd, ciStopCmd} {
		cmd.Flags().BoolVar(&ciOpts.keepArtifacts, "keep-artifacts", true, "Keep CI runtime log and collector config after exit")
		cmd.Flags().BoolVar(&ciOpts.requireTelemetry, "require-telemetry", true, "Fail the command when telemetry validation fails; set false to warn only")
	}
	for _, cmd := range []*cobra.Command{ciStartCmd, ciStopCmd} {
		cmd.Flags().StringVar(&ciOpts.stateFile, "state-file", "", "Sidecar session state file (defaults to <base-dir>/session.json)")
	}
	for _, name := range []string{"base-dir", "work-dir", "collector", "otlp-grpc-port", "otlp-http-port"} {
		_ = ciExecCmd.Flags().MarkHidden(name)
	}
	for _, name := range []string{"base-dir", "collector", "otlp-grpc-port", "otlp-http-port"} {
		_ = ciStartCmd.Flags().MarkHidden(name)
	}
	_ = ciStopCmd.Flags().MarkHidden("base-dir")
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

func runCIStart(cmd *cobra.Command, args []string) error {
	retention := endpointconfig.ContentRetention(ciOpts.contentRetention)
	session, err := beaconci.Provision(beaconci.Options{
		BaseDir:          ciOpts.baseDir,
		LogPath:          ciOpts.logPath,
		CollectorPath:    ciOpts.collectorPath,
		GRPCPort:         ciOpts.grpcPort,
		HTTPPort:         ciOpts.httpPort,
		Harness:          ciOpts.harness,
		ContentRetention: retention,
		Forward:          ciOpts.forward,
		ForwardEndpoint:  ciOpts.forwardEndpoint,
	})
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(session.BaseDir, "collector.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if _, err := session.StartDetached(logFile); err != nil {
		return err
	}

	cleanup := func() { _ = session.StopDetached(5 * time.Second) }
	go func() {
		if _, ok := <-sigCh; ok {
			cleanup()
			os.Exit(1)
		}
	}()

	statePath := ciOpts.stateFile
	if statePath == "" {
		statePath = filepath.Join(session.BaseDir, beaconci.StateFileName)
	}
	if err := session.WriteState(statePath); err != nil {
		cleanup()
		return err
	}
	if err := emitClaudeTelemetryEnv(session.GRPCEndpoint, retention); err != nil {
		cleanup()
		return err
	}
	if ciOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(session)
	}
	fmt.Printf("Beacon CI collector started (pid=%d)\n", session.PID)
	fmt.Printf("OTLP endpoint: %s\n", session.GRPCEndpoint)
	fmt.Printf("Runtime log: %s\n", session.LogPath)
	fmt.Printf("State file: %s\n", statePath)
	if session.Forward != "" {
		fmt.Printf("Forwarding: %s -> %s\n", session.Forward, session.ForwardEndpoint)
	}
	return nil
}

func runCIStop(cmd *cobra.Command, args []string) error {
	statePath := ciOpts.stateFile
	if statePath == "" {
		if ciOpts.baseDir != "" {
			statePath = filepath.Join(ciOpts.baseDir, beaconci.StateFileName)
		} else if ciOpts.logPath != "" {
			statePath = filepath.Join(filepath.Dir(ciOpts.logPath), beaconci.StateFileName)
		} else {
			statePath = beaconci.DefaultStatePath()
		}
	}
	session, err := beaconci.LoadSession(statePath)
	if err != nil {
		return fmt.Errorf("load CI session state (%s): %w", statePath, err)
	}
	if err := session.StopDetached(10 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: collector stop: %v\n", err)
	}
	harness := ciOpts.harness
	if harness == "" {
		harness = session.Harness
	}
	result := beaconci.Validate(beaconci.ValidationOptions{
		LogPath:        session.LogPath,
		MinEvents:      ciOpts.minEvents,
		RequireHarness: harness,
		Since:          session.StartedAtTime(),
	})
	if !ciOpts.keepArtifacts && result.Status != "fail" {
		if err := os.RemoveAll(session.BaseDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: artifact cleanup: %v\n", err)
		}
	}
	if ciOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	} else {
		printCIValidation(result)
	}
	if result.Status == "fail" {
		if ciOpts.requireTelemetry {
			return fmt.Errorf("Beacon CI telemetry validation failed")
		}
		fmt.Fprintln(os.Stderr, "Warning: Beacon CI telemetry validation failed (continuing because --require-telemetry=false)")
	}
	return nil
}

// emitClaudeTelemetryEnv appends the Claude Code OTLP variables to $GITHUB_ENV
// (when running in GitHub Actions) so subsequent steps export to the collector,
// and always prints them for visibility. No secret is emitted.
func emitClaudeTelemetryEnv(endpoint string, retention endpointconfig.ContentRetention) error {
	vars := beaconci.ClaudeTelemetryVars(endpoint, retention)
	if ghEnv := strings.TrimSpace(os.Getenv("GITHUB_ENV")); ghEnv != "" {
		f, err := os.OpenFile(ghEnv, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		for _, kv := range vars {
			if _, err := fmt.Fprintf(f, "%s=%s\n", kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	for _, kv := range vars {
		fmt.Printf("%s=%s\n", kv[0], kv[1])
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
