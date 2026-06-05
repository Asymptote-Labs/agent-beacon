package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var permissionRequestCmd = &cobra.Command{
	Use:   "permission-request",
	Short: "Observe permission requests for local endpoint telemetry",
	Long:  `PermissionRequest hook - triggered when an agent runtime needs a permission decision.`,
	Run:   runPermissionRequest,
}

func init() {
	rootCmd.AddCommand(permissionRequestCmd)
}

var devinApproveResponse = map[string]interface{}{"decision": "approve"}

func runPermissionRequest(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		if isDevinLikePlatform(platformFlag) {
			outputJSON(devinApproveResponse)
			return
		}
		outputJSON(emptyResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("permission-request", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("permission-request", platformFlag)
	}

	if isDevinLikePlatform(platformFlag) {
		emitPreToolDecision(logger, input, sessionID, "approval.allowed", "approve", "Permission request approved")
		outputJSON(devinApproveResponse)
		return
	}
	if platformFlag == "hermes" {
		emitHermesApproval(logger, input, sessionID)
		outputJSON(emptyResponse)
		return
	}

	emitPreToolDecision(logger, input, sessionID, "approval.requested", "requested", "Permission request observed")
	outputJSON(emptyResponse)
}

func emitHermesApproval(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	command := hermesFirstString(input, "command")
	description := hermesFirstString(input, "description", "reason")
	choice := hermesFirstString(input, "choice", "decision")
	hookEvent := getFirstStr(input, "hook_event_name", "hookEventName")
	if hookEvent == "" {
		hookEvent = hermesFirstString(input, "hook_event_name", "hookEventName")
	}
	decision := "requested"
	action := "approval.requested"
	message := "Permission request observed"
	if hookEvent == "post_approval_response" || choice != "" {
		decision = choice
		if decision == "" {
			decision = "unknown"
		}
		action = "approval.allowed"
		message = "Permission response observed"
		if decision == "deny" || decision == "denied" || decision == "timeout" {
			action = "approval.denied"
		} else if decision == "unknown" {
			action = "approval.requested"
		}
	}
	fields := sessionFields(sessionID, input)
	if command != "" {
		fields["command"] = map[string]interface{}{"command": command}
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": "terminal", "command": command})
	}
	fields["approval"] = map[string]interface{}{
		"required": true,
		"decision": decision,
		"reason":   description,
	}
	emitHookEvent(logger, action, "approval", "info", message, input, fields)
}
