package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

var sessionEndCmd = &cobra.Command{
	Use:   "session-end",
	Short: "Cleanup session data when coding session exits",
	Long:  `SessionEnd hook - triggered when the coding session terminates.`,
	Run:   runSessionEnd,
}

func init() {
	rootCmd.AddCommand(sessionEndCmd)
}

func runSessionEnd(cmd *cobra.Command, args []string) {
	platformLogger := logging.NewLoggerForPlatform("session-end", platformFlag)

	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	if sessionID != "" {
		logger := logging.NewSessionLogger("session-end", platformFlag, sessionID)
		logger.Info("Session ended", "session_id", sessionID, "platform", platformFlag)
		emitHookEvent(logger, "session.ended", "session", "info", "Agent session ended", input, sessionFields(sessionID, input))
		maybeReconcileCodexUsage(logger)
		uploadCloudTelemetry(logger, true)

		logFile := config.GetSessionLogFile(platformFlag, sessionID)
		if err := os.Remove(logFile); err != nil && !os.IsNotExist(err) {
			platformLogger.Warn("Failed to remove session log", "session_id", sessionID, "error", err.Error())
		}
	} else if isDevinLikePlatform(platformFlag) {
		platformLogger.Info("Session ended", "platform", platformFlag)
		emitHookEvent(platformLogger, "session.ended", "session", "info", "Agent session ended", input, sessionFields("", input))
		uploadCloudTelemetry(platformLogger, true)
	} else {
		platformLogger.Info("Session ended", "platform", platformFlag)
		maybeReconcileCodexUsage(platformLogger)
		uploadCloudTelemetry(platformLogger, true)
	}

	cleaned := state.CleanupStaleForPlatform(platformFlag)
	if cleaned > 0 {
		platformLogger.Info(fmt.Sprintf("Cleaned up %d stale sessions", cleaned))
	}

	outputJSON(emptyResponse)
}
