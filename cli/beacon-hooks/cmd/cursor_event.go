package cmd

import (
	"github.com/spf13/cobra"
)

var cursorEventCmd = &cobra.Command{
	Use:   "cursor-event",
	Short: "Record Cursor-specific lifecycle telemetry",
	Run:   runCursorEvent,
}

func init() {
	rootCmd.AddCommand(cursorEventCmd)
}

func runCursorEvent(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("cursor-event", platformFlag, sessionID)
	switch getFirstStr(input, "hook_event_name", "hookEventName") {
	case "preCompact":
		emitHookEvent(logger, "session.compacting", "session", "info", "Context compaction observed", input, sessionFields(sessionID, input))
	default:
		emitHookEvent(logger, "session.event", "session", "info", "Cursor lifecycle event observed", input, sessionFields(sessionID, input))
	}
	maybeUploadCursorCloudTelemetry(logger)
	outputJSON(emptyResponse)
}
