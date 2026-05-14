package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var rootCmd = &cobra.Command{
	Use:   "beacon",
	Short: "Beacon Endpoint Agent - local AI runtime telemetry",
	Long: `Beacon Endpoint Agent discovers local AI agent runtimes, configures
local telemetry, and writes Wazuh-compatible JSON logs without requiring an
Beacon-hosted backend.`,
	Version: version.GetVersion(),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Beacon Endpoint Agent")
		fmt.Println()
		fmt.Println("Start with:")
		fmt.Println("  beacon endpoint install")
		fmt.Println("  beacon endpoint status")
		fmt.Println("  beacon endpoint wazuh print-config")
		fmt.Println()
		cmd.Usage()
	},
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Set version template
	rootCmd.SetVersionTemplate(`{{printf "beacon version %s\n" .Version}}`)

	// Add version flag shorthand
	rootCmd.Flags().BoolP("version", "v", false, "Print the version number")
}
