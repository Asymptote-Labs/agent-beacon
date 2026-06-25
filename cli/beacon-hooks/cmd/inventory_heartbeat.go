package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var inventoryHeartbeatCmd = &cobra.Command{
	Use:   "inventory-heartbeat",
	Short: "Emit endpoint inventory heartbeat telemetry",
	Long:  `Inventory heartbeat hook - triggered by agent lifecycle hooks to refresh local configuration inventory.`,
	Run:   runInventoryHeartbeat,
}

func init() {
	rootCmd.AddCommand(inventoryHeartbeatCmd)
}

func runInventoryHeartbeat(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}

	logger := logging.NewLoggerForPlatform("inventory-heartbeat", platformFlag)
	maybeEmitInventoryHeartbeat(logger, input)
	outputJSON(emptyResponse)
}
