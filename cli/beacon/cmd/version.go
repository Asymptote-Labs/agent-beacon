package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var versionCmd = &cobra.Command{
	Use:          "version",
	Short:        "Display the version of the Beacon CLI",
	Long:         `Display the version number, git commit, and build date of the Beacon CLI.`,
	SilenceUsage: true,
	RunE:         runVersion,
}

func runVersion(cmd *cobra.Command, args []string) error {
	fmt.Fprintln(cmd.OutOrStdout(), "beacon version", version.GetFullVersion())
	if versionCheck {
		return runVersionCheck(cmd, args)
	}
	return nil
}

func init() {
	versionCmd.Flags().BoolVar(&versionCheck, "check", false, "Check whether a newer Beacon CLI release is available")
	rootCmd.AddCommand(versionCmd)
}
