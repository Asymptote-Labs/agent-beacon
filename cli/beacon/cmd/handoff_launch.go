package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
)

var handoffLaunchSpec string

var handoffLaunchCmd = &cobra.Command{
	Use:          "_launch",
	Short:        "Run a private handoff launch specification",
	Hidden:       true,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		code, err := handoff.RunLaunchSpec(handoffLaunchSpec, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if code != 0 {
			if code < 0 {
				code = 1
			}
			handoffExit(code)
		}
		return nil
	},
}

func init() {
	handoffCmd.AddCommand(handoffLaunchCmd)
	handoffLaunchCmd.Flags().StringVar(&handoffLaunchSpec, "spec", "", "Private launch specification")
	_ = handoffLaunchCmd.MarkFlagRequired("spec")
}
