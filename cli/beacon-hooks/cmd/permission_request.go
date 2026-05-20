package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var permissionRequestCmd = &cobra.Command{
	Use:   "permission-request",
	Short: "Observe Devin permission requests for local endpoint telemetry",
	Long:  `PermissionRequest hook - triggered when Devin needs a permission decision.`,
	Run:   runPermissionRequest,
}

func init() {
	rootCmd.AddCommand(permissionRequestCmd)
}

var devinApproveResponse = map[string]interface{}{"decision": "approve"}

func runPermissionRequest(cmd *cobra.Command, args []string) {
	if platformFlag != "devin" {
		outputJSON(emptyResponse)
		return
	}

	input, err := readStdinJSON()
	if err != nil {
		outputJSON(devinApproveResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("permission-request", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("permission-request", platformFlag)
	}

	emitPreToolDecision(logger, input, sessionID, "approval.allowed", "approve", "Permission request approved")
	outputJSON(devinApproveResponse)
}
