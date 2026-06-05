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
	subagent := map[string]interface{}{
		"id":   getFirstStr(input, "agent_id", "agentId"),
		"type": getFirstStr(input, "agent_type", "agentType"),
	}
	if platformFlag == "hermes" {
		if extra := hermesExtra(input); extra != nil {
			subagent = mergeNested(subagent, map[string]interface{}{
				"role":        firstToolString(extra, "child_role"),
				"status":      firstToolString(extra, "child_status"),
				"summary":     firstToolString(extra, "child_summary"),
				"duration_ms": extra["duration_ms"],
			})
		}
	}
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"subagent": map[string]interface{}{
		"id":          subagent["id"],
		"type":        subagent["type"],
		"role":        subagent["role"],
		"status":      subagent["status"],
		"summary":     subagent["summary"],
		"duration_ms": subagent["duration_ms"],
	}})
	emitHookEvent(logger, action, "session", "info", message, input, fields)
	outputJSON(emptyResponse)
}
