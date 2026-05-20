package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

var sessionStartCmd = &cobra.Command{
	Use:   "session-start",
	Short: "Initialize session and cleanup stale data",
	Long:  `SessionStart hook - triggered when a new coding session begins.`,
	Run:   runSessionStart,
}

func init() {
	rootCmd.AddCommand(sessionStartCmd)
}

func runSessionStart(cmd *cobra.Command, args []string) {
	platformLogger := logging.NewLoggerForPlatform("session-start", platformFlag)

	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	if sessionID == "" {
		if platformFlag == "devin" {
			logger := logging.NewLoggerForPlatform("session-start", platformFlag)
			logger.Info("Session initialized", "platform", platformFlag)
			emitHookEvent(logger, "session.started", "session", "info", "Agent session started", input, sessionFields("", input))
		}
		outputJSON(emptyResponse)
		return
	}

	// Switch to per-session logger now that we have a session ID
	logger := logging.NewSessionLogger("session-start", platformFlag, sessionID)

	if config.RotateLogIfNeededForPlatform(platformFlag) {
		platformLogger.Info("Rotated log file (exceeded size limit)")
	}

	cleaned := state.CleanupStaleForPlatform(platformFlag)
	if cleaned > 0 {
		logger.Info(fmt.Sprintf("Cleaned up %d stale evaluations", cleaned))
	}

	// Persist model name so post-tool can include it in evaluations
	if model, ok := input["model"].(string); ok && model != "" {
		st := state.NewSessionState(sessionID, platformFlag)
		st.SetModel(model)
		logger.Debug("Stored session model", "model", model)
	}

	logger.Info("Session initialized", "session_id", sessionID, "platform", platformFlag)
	emitHookEvent(logger, "session.started", "session", "info", "Agent session started", input, sessionFields(sessionID, input))
	outputJSON(emptyResponse)
}
