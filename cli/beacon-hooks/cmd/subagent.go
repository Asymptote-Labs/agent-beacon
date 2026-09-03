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
	// Muse Code names the child session on the subagent event, and the parent nowhere.
	//
	// Its SubagentStart carries `subagent_id` and `child_session_id`, and -- measured rather than
	// assumed -- the event's own `session_id` is the CHILD's, not the parent's. So session.id on a
	// Muse subagent event identifies the subagent's session, and no field in the payload recovers
	// which session spawned it. Recording child_session_id explicitly is what makes that legible
	// instead of leaving a reader to assume session.id means the parent, as it does on every other
	// runtime here.
	//
	// The gap is left as a gap. Muse's own session log nests a child under its parent's directory
	// and carries parent_session_id, so the correlation exists on disk -- but reading it is a poll
	// path, not this one, and inventing a parent id from a hook payload that does not carry one
	// would be a fabricated join key in a security log.
	if platformFlag == "muse" {
		subagent = mergeNested(subagent, map[string]interface{}{
			"child_session_id": getFirstStr(input, "child_session_id", "childSessionId"),
		})
	}
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"subagent": map[string]interface{}{
		"child_session_id":    subagent["child_session_id"],
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
