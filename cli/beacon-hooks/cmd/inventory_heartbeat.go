package cmd

import (
	"github.com/spf13/cobra"
)

// inventoryHeartbeatCmd is a no-op kept for hooks installed by older Beacon versions.
//
// Codex hooks written before the scheduled inventory job existed invoke
// `beacon-hooks --platform codex inventory-heartbeat` on every session start and prompt, and
// those hook files are only rewritten by a hooks reinstall. Inventory is written by
// `beacon endpoint inventory heartbeat --scheduled` under launchd or systemd now, so this
// command reads its input and answers with the empty response every hook expects.
var inventoryHeartbeatCmd = &cobra.Command{
	Use:    "inventory-heartbeat",
	Short:  "No-op kept for hooks installed by older versions; inventory is a scheduled job now",
	Hidden: true,
	Run:    runInventoryHeartbeat,
}

func init() {
	rootCmd.AddCommand(inventoryHeartbeatCmd)
}

func runInventoryHeartbeat(cmd *cobra.Command, args []string) {
	_, _ = readStdinJSON()
	outputJSON(emptyResponse)
}
