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

	emitPreToolDecision(logger, input, sessionID, "approval.requested", "requested", "Permission request observed")
	outputJSON(emptyResponse)
}
