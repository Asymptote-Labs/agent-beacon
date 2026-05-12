package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the hooks binary version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
