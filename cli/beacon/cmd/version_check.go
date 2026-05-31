package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type versionChecker interface {
	Check(context.Context) (updatecheck.Result, error)
}

var (
	newVersionChecker = func(currentVersion string) versionChecker {
		return updatecheck.DefaultChecker(currentVersion)
	}
	versionCheckTimeout = 2 * time.Second
)

var versionCheckCmd = &cobra.Command{
	Use:          "check",
	Short:        "Check whether a newer Beacon CLI release is available",
	SilenceUsage: true,
	RunE:         runVersionCheck,
}

func runVersionCheck(cmd *cobra.Command, args []string) error {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, versionCheckTimeout)
	defer cancel()

	result, err := newVersionChecker(version.GetVersion()).Check(ctx)
	if err != nil {
		return fmt.Errorf("unable to check for Beacon updates: %w", err)
	}

	out := cmd.OutOrStdout()
	if result.CurrentIsDev {
		fmt.Fprintln(out, "Beacon dev build: update checks require a released version.")
		return nil
	}
	if result.UnsupportedCurrentVersion {
		fmt.Fprintf(out, "Beacon version %q cannot be compared to released versions.\n", result.CurrentVersion)
		return nil
	}
	if result.UpdateAvailable {
		fmt.Fprintf(out, "Beacon %s is available. Current version: %s\n", result.LatestVersion, result.CurrentVersion)
		fmt.Fprintln(out, "Upgrade with Homebrew: brew upgrade beacon")
		if result.ReleaseURL != "" {
			fmt.Fprintf(out, "Download: %s\n", result.ReleaseURL)
		}
		return nil
	}
	fmt.Fprintf(out, "Beacon %s is up to date.\n", result.CurrentVersion)
	return nil
}

func init() {
	versionCmd.AddCommand(versionCheckCmd)
}
