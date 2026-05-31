package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type updateChecker interface {
	Check(context.Context) (updatecheck.Result, error)
}

var (
	newUpdateChecker = func(currentVersion string) updateChecker {
		return updatecheck.DefaultChecker(currentVersion)
	}
	updateCheckTimeout = 2 * time.Second
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for Beacon CLI updates",
}

var updateCheckCmd = &cobra.Command{
	Use:          "check",
	Short:        "Check whether a newer Beacon CLI release is available",
	SilenceUsage: true,
	RunE:         runUpdateCheck,
}

func runUpdateCheck(cmd *cobra.Command, args []string) error {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, updateCheckTimeout)
	defer cancel()

	result, err := newUpdateChecker(version.GetVersion()).Check(ctx)
	if err != nil {
		return fmt.Errorf("unable to check for Beacon updates: %w", err)
	}

	out := cmd.OutOrStdout()
	if result.CurrentIsDev {
		fmt.Fprintln(out, "Beacon dev build: update checks require a released version.")
		return nil
	}
	if result.UpdateAvailable {
		fmt.Fprintf(out, "Beacon %s is available. Current version: %s\n", result.LatestVersion, result.CurrentVersion)
		fmt.Fprintln(out, "Upgrade with: brew upgrade beacon")
		return nil
	}
	fmt.Fprintf(out, "Beacon %s is up to date.\n", result.CurrentVersion)
	return nil
}

func init() {
	updateCmd.AddCommand(updateCheckCmd)
	rootCmd.AddCommand(updateCmd)
}
