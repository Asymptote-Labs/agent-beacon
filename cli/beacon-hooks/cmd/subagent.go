package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var subagentStartCmd = &cobra.Command{
	Use:   "subagent-start",
	Short: "Record subagent start telemetry",
	Run: func(cmd *cobra.Command, args []string) {
		runSubagentLifecycle("subagent.started", "Subagent started")
	},
}

var subagentStopCmd = &cobra.Command{
	Use:   "subagent-stop",
	Short: "Record subagent stop telemetry",
	Run: func(cmd *cobra.Command, args []string) {
		runSubagentLifecycle("subagent.stopped", "Subagent stopped")
	},
}

func init() {
	rootCmd.AddCommand(subagentStartCmd)
	rootCmd.AddCommand(subagentStopCmd)
}

func runSubagentLifecycle(action, message string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("subagent", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("subagent", platformFlag)
	}
	fields := sessionFields(sessionID, input)
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"subagent": map[string]interface{}{
		"id":   getFirstStr(input, "agent_id", "agentId"),
		"type": getFirstStr(input, "agent_type", "agentType"),
	}})
	emitHookEvent(logger, action, "session", "info", message, input, fields)
	outputJSON(emptyResponse)
}
