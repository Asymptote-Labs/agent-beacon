package cmd

import (
	"github.com/spf13/cobra"
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
	logger := newHookLogger("subagent", platformFlag, sessionID)
	fields := sessionFields(sessionID, input)
	subagent := map[string]interface{}{
		"id":   getFirstStr(input, "subagent_id", "agent_id", "agentId"),
		"type": getFirstStr(input, "subagent_type", "agent_type", "agentType"),
	}
	if platformFlag == "cursor" {
		subagent = mergeNested(subagent, map[string]interface{}{
			"status":              getFirstStr(input, "status"),
			"summary":             getFirstStr(input, "summary"),
			"description":         getFirstStr(input, "description"),
			"parent_conversation": getFirstStr(input, "parent_conversation_id"),
			"tool_call_id":        getFirstStr(input, "tool_call_id"),
			"model":               getFirstStr(input, "subagent_model"),
			"duration_ms":         input["duration_ms"],
			"message_count":       input["message_count"],
			"tool_call_count":     input["tool_call_count"],
		})
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
		"id":                  subagent["id"],
		"type":                subagent["type"],
		"role":                subagent["role"],
		"status":              subagent["status"],
		"summary":             subagent["summary"],
		"description":         subagent["description"],
		"parent_conversation": subagent["parent_conversation"],
		"tool_call_id":        subagent["tool_call_id"],
		"model":               subagent["model"],
		"duration_ms":         subagent["duration_ms"],
		"message_count":       subagent["message_count"],
		"tool_call_count":     subagent["tool_call_count"],
	}})
	emitHookEvent(logger, action, "session", "info", message, input, fields)
	maybeUploadCursorCloudTelemetry(logger)
	outputJSON(emptyResponse)
}
