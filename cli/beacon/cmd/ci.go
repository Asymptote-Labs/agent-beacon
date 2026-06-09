package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	beaconci "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ci"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

var ciOpts struct {
	baseDir           string
	logPath           string
	workDir           string
	collectorPath     string
	harness           string
	statePath         string
	githubEnvPath     string
	contentRetention  string
	grpcPort          int
	httpPort          int
	jsonOutput        bool
	keepArtifacts     bool
	includeCodexSpans bool
	minEvents         int
	forward           string
	forwardEndpoint   string
	uploads           []string
	requireTelemetry  bool
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

var ciStartCmd = &cobra.Command{
	Use:          "start",
	Short:        "Start a detached Beacon CI telemetry session",
	SilenceUsage: true,
	RunE:         runCIStart,
}

var ciFinishCmd = &cobra.Command{
	Use:          "finish",
	Short:        "Stop and validate a detached Beacon CI telemetry session",
	SilenceUsage: true,
	RunE:         runCIFinish,
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
	ciCmd.AddCommand(ciStartCmd)
	ciCmd.AddCommand(ciFinishCmd)
	ciCmd.AddCommand(ciValidateCmd)
	for _, cmd := range []*cobra.Command{ciExecCmd, ciStartCmd, ciFinishCmd, ciValidateCmd} {
		cmd.Flags().StringVar(&ciOpts.logPath, "log-path", "", "CI runtime JSONL log path")
		cmd.Flags().StringVar(&ciOpts.harness, "harness", beaconci.DefaultHarness, "CI harness to configure (claude, codex, or comma-separated)")
		cmd.Flags().BoolVar(&ciOpts.jsonOutput, "json", false, "Print result as JSON")
		cmd.Flags().IntVar(&ciOpts.minEvents, "min-events", beaconci.DefaultValidationMin, "Minimum matching events required during validation")
	}
	for _, cmd := range []*cobra.Command{ciExecCmd, ciStartCmd} {
		cmd.Flags().StringVar(&ciOpts.baseDir, "base-dir", "", "CI session base directory (defaults to $RUNNER_TEMP/beacon or a temp directory)")
		cmd.Flags().StringVar(&ciOpts.collectorPath, "collector", "", "Path to a beacon-otelcol binary")
		cmd.Flags().IntVar(&ciOpts.grpcPort, "otlp-grpc-port", endpointconfig.DefaultGRPCPort, "Local OTLP gRPC port")
		cmd.Flags().IntVar(&ciOpts.httpPort, "otlp-http-port", endpointconfig.DefaultHTTPPort, "Local OTLP HTTP port")
		cmd.Flags().BoolVar(&ciOpts.includeCodexSpans, "include-codex-spans", false, "Include high-volume Codex spans for troubleshooting")
		for _, name := range []string{"base-dir", "collector", "otlp-grpc-port", "otlp-http-port", "include-codex-spans"} {
			_ = cmd.Flags().MarkHidden(name)
		}
	}
	ciExecCmd.Flags().StringVar(&ciOpts.workDir, "work-dir", "", "Working directory for the child command")
	ciExecCmd.Flags().StringVar(&ciOpts.contentRetention, "content-retention", "", "Deprecated no-op; Beacon always captures full content subject to redaction and size limits")
	_ = ciExecCmd.Flags().MarkHidden("content-retention")
	_ = ciExecCmd.Flags().MarkDeprecated("content-retention", "Beacon now always captures full content; this flag is ignored")
	ciExecCmd.Flags().BoolVar(&ciOpts.keepArtifacts, "keep-artifacts", true, "Keep CI runtime log and collector config after exit")
	_ = ciExecCmd.Flags().MarkHidden("work-dir")
	for _, cmd := range []*cobra.Command{ciExecCmd, ciStartCmd, ciFinishCmd} {
		cmd.Flags().StringVar(&ciOpts.forward, "forward", "", "Optionally forward events to a customer-managed SIEM: splunk or falcon (token read from the environment)")
		cmd.Flags().StringVar(&ciOpts.forwardEndpoint, "forward-endpoint", "", "SIEM HEC endpoint URL for --forward (token comes from BEACON_CI_*_HEC_TOKEN)")
	}
	for _, cmd := range []*cobra.Command{ciExecCmd, ciFinishCmd} {
		cmd.Flags().StringArrayVar(&ciOpts.uploads, "upload", nil, "Upload final CI runtime JSONL after validation: s3 or gcs (repeatable)")
		cmd.Flags().BoolVar(&ciOpts.requireTelemetry, "require-telemetry", true, "Fail the command when telemetry validation fails; set false to warn only")
	}
	for _, cmd := range []*cobra.Command{ciStartCmd, ciFinishCmd} {
		cmd.Flags().StringVar(&ciOpts.statePath, "state-path", "", "CI session state path")
		_ = cmd.Flags().MarkHidden("state-path")
	}
	ciStartCmd.Flags().StringVar(&ciOpts.githubEnvPath, "github-env", "", "Path to GITHUB_ENV for exporting telemetry variables")
	_ = ciStartCmd.Flags().MarkHidden("github-env")
}

func runCIExec(cmd *cobra.Command, args []string) error {
	runCtx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	session, err := beaconci.Provision(beaconci.Options{
		BaseDir:           ciOpts.baseDir,
		LogPath:           ciOpts.logPath,
		WorkDir:           ciOpts.workDir,
		CollectorPath:     ciOpts.collectorPath,
		GRPCPort:          ciOpts.grpcPort,
		HTTPPort:          ciOpts.httpPort,
		Harness:           ciOpts.harness,
		KeepArtifacts:     ciOpts.keepArtifacts,
		Forward:           ciOpts.forward,
		ForwardEndpoint:   ciOpts.forwardEndpoint,
		Uploads:           ciOpts.uploads,
		IncludeCodexSpans: ciOpts.includeCodexSpans,
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
	finishResult, finishErr := finishSession(context.Background(), session)
	execResult := beaconci.ExecResult{
		Session:         *session,
		ChildExitCode:   childExit,
		Validation:      finishResult.Validation,
		Uploads:         finishResult.Uploads,
		ArtifactMessage: finishResult.ArtifactMessage,
	}
	if finishErr != nil {
		if ciOpts.jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(execResult)
		}
		if childExit != 0 {
			os.Exit(childExit)
		}
		return finishErr
	}
	if !ciOpts.keepArtifacts && finishResult.Validation.Status != "fail" && childExit == 0 && ciOpts.baseDir == "" && ciOpts.logPath == "" {
		if err := os.RemoveAll(session.BaseDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: artifact cleanup: %v\n", err)
		} else {
			execResult.ArtifactMessage = "Beacon CI artifacts cleaned"
		}
	}
	if ciOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(execResult)
	} else {
		printCIValidation(finishResult.Validation)
		fmt.Println(execResult.ArtifactMessage)
	}
	if childExit != 0 {
		os.Exit(childExit)
	}
	return nil
}

func runCIStart(cmd *cobra.Command, args []string) error {
	runCtx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if !cmd.Flags().Changed("harness") {
		ciOpts.harness = beaconci.DefaultSessionHarness
	}
	session, err := beaconci.Provision(beaconci.Options{
		BaseDir:           ciOpts.baseDir,
		LogPath:           ciOpts.logPath,
		CollectorPath:     ciOpts.collectorPath,
		GRPCPort:          ciOpts.grpcPort,
		HTTPPort:          ciOpts.httpPort,
		Harness:           ciOpts.harness,
		Forward:           ciOpts.forward,
		ForwardEndpoint:   ciOpts.forwardEndpoint,
		IncludeCodexSpans: ciOpts.includeCodexSpans,
	})
	if err != nil {
		return err
	}
	result, err := session.StartDetached(runCtx, ciOpts.statePath, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if err := writeGitHubEnv(ciOpts.githubEnvPath, result.Exports); err != nil {
		return err
	}
	if ciOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Println(result.ArtifactMessage)
	for key, value := range result.Exports {
		if strings.HasPrefix(key, "BEACON_CI_") || key == "CODEX_HOME" {
			fmt.Printf("%s=%s\n", key, value)
		}
	}
	return nil
}

func runCIFinish(cmd *cobra.Command, args []string) error {
	session, err := beaconci.LoadState(ciOpts.statePath)
	if err != nil {
		return err
	}
	session.Forward = ciOpts.forward
	session.ForwardEndpoint = ciOpts.forwardEndpoint
	if len(ciOpts.uploads) > 0 {
		uploads, err := beaconci.ResolveUploadDestinationsForSession(ciOpts.uploads, session)
		if err != nil {
			return err
		}
		session.Uploads = uploads
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stopErr := session.StopDetached(stopCtx)
	cancel()
	if stopErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: collector stop: %v\n", stopErr)
	}
	result, err := finishSession(context.Background(), session)
	if ciOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	} else {
		printCIValidation(result.Validation)
		fmt.Println(result.ArtifactMessage)
	}
	return err
}

func finishSession(ctx context.Context, session *beaconci.Session) (beaconci.FinishResult, error) {
	result := beaconci.Validate(beaconci.ValidationOptions{
		LogPath:        session.LogPath,
		MinEvents:      ciOpts.minEvents,
		RequireHarness: session.Harness,
		Since:          session.StartedAtTime(),
	})
	finishResult := beaconci.FinishResult{
		Session:         *session,
		Validation:      result,
		ArtifactMessage: fmt.Sprintf("Beacon CI artifacts: log=%s config=%s", session.LogPath, session.ConfigPath),
	}
	if (result.Status != "fail" || !ciOpts.requireTelemetry) && len(session.Uploads) > 0 {
		uploadCtx, uploadCancel := context.WithTimeout(ctx, 2*time.Minute)
		uploadResults, uploadErr := session.UploadArtifacts(uploadCtx)
		uploadCancel()
		finishResult.Uploads = uploadResults
		if uploadErr != nil {
			fmt.Fprintf(os.Stderr, "Beacon CI upload failed: %v\n", uploadErr)
			return finishResult, fmt.Errorf("Beacon CI upload failed: %w", uploadErr)
		}
		if len(uploadResults) > 0 {
			finishResult.ArtifactMessage += fmt.Sprintf(" uploads=%s", uploadTargets(uploadResults))
		}
	}
	if result.Status == "fail" {
		if ciOpts.requireTelemetry {
			fmt.Fprintln(os.Stderr, "Beacon CI telemetry validation failed")
			return finishResult, fmt.Errorf("Beacon CI telemetry validation failed")
		}
		fmt.Fprintln(os.Stderr, "Warning: Beacon CI telemetry validation failed (continuing because --require-telemetry=false)")
	}
	return finishResult, nil
}

func uploadTargets(results []beaconci.UploadResult) string {
	targets := make([]string, 0, len(results))
	for _, result := range results {
		if result.Target != "" {
			targets = append(targets, result.Target)
		}
	}
	return strings.Join(targets, ",")
}

func writeGitHubEnv(path string, values map[string]string) error {
	if strings.TrimSpace(path) == "" {
		path = os.Getenv("GITHUB_ENV")
	}
	if strings.TrimSpace(path) == "" || len(values) == 0 {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			delimiter := "BEACON_CI_ENV"
			if _, err := fmt.Fprintf(file, "%s<<%s\n%s\n%s\n", key, delimiter, value, delimiter); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			return err
		}
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
