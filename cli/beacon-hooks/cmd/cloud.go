package cmd

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/cloudshuttle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var cloudResetCmd = &cobra.Command{
	Use:   "cloud-reset",
	Short: "Reset cloud telemetry state at session start",
	Run:   runCloudReset,
}

var cloudUploadCmd = &cobra.Command{
	Use:   "cloud-upload",
	Short: "Upload cloud telemetry runtime JSONL",
	Run:   runCloudUpload,
}

var cloudWatchCmd = &cobra.Command{
	Use:   "cloud-watch",
	Short: "Periodically upload changed cloud telemetry",
	Run:   runCloudWatch,
}

var codexPromptSubmitCmd = &cobra.Command{
	Use:   "codex-prompt-submit",
	Short: "Record Codex cloud prompt submission and upload telemetry",
	Run:   runCodexPromptSubmit,
}

func init() {
	rootCmd.AddCommand(cloudResetCmd)
	rootCmd.AddCommand(cloudUploadCmd)
	rootCmd.AddCommand(cloudWatchCmd)
	rootCmd.AddCommand(codexPromptSubmitCmd)
}

func runCloudReset(cmd *cobra.Command, args []string) {
	logger := logging.NewLoggerForPlatform("cloud-reset", platformFlag)
	if input, ok := readOptionalStdinJSON(); ok {
		seedCloudRunID(input)
	}
	if err := cloudshuttle.ResetFromEnv(); err != nil {
		logger.Warn("Failed to reset cloud telemetry log", "error", err.Error())
	}
	outputJSON(emptyResponse)
}

func runCloudUpload(cmd *cobra.Command, args []string) {
	logger := logging.NewLoggerForPlatform("cloud-upload", platformFlag)
	if input, ok := readOptionalStdinJSON(); ok {
		seedCloudRunID(input)
	}
	uploadCloudTelemetry(logger, true)
	outputJSON(emptyResponse)
}

func runCloudWatch(cmd *cobra.Command, args []string) {
	logger := logging.NewLoggerForPlatform("cloud-watch", platformFlag)
	if err := cloudshuttle.Watch(context.Background(), 15*time.Second); err != nil {
		logger.Warn("Cloud telemetry watcher stopped", "error", err.Error())
	}
}

func runCodexPromptSubmit(cmd *cobra.Command, args []string) {
	input, ok := readOptionalStdinJSON()
	if !ok {
		outputJSON(emptyResponse)
		return
	}
	seedCloudRunID(input)
	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("codex-prompt-submit", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("codex-prompt-submit", platformFlag)
	}
	fields := sessionFields(sessionID, input)
	if prompt := getFirstStr(input, "prompt", "user_prompt", "prompt_text", "text", "input"); prompt != "" {
		fields["prompt"] = map[string]interface{}{"text": prompt}
	}
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Codex prompt submitted", input, fields)
	uploadCloudTelemetry(logger, true)
	outputJSON(emptyResponse)
}

func seedCloudRunID(input map[string]interface{}) {
	sessionID := resolveSessionID(input, platformFlag)
	if sessionID == "" {
		return
	}
	if os.Getenv("BEACON_RUN_ID") == "" {
		_ = os.Setenv("BEACON_RUN_ID", sessionID)
	}
	if os.Getenv("BEACON_CODEX_SESSION_ID") == "" {
		_ = os.Setenv("BEACON_CODEX_SESSION_ID", sessionID)
	}
}

func readOptionalStdinJSON() (map[string]interface{}, bool) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return nil, false
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return nil, false
	}
	input, err := readStdinJSON()
	if err != nil {
		return nil, false
	}
	return input, true
}
