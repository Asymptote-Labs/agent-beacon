package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Display the version of the Beacon CLI",
	Long:  `Display the version number, git commit, and build date of the Beacon CLI.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("beacon version", version.GetFullVersion())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
