package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type versionChecker interface {
	Check(context.Context) (updatecheck.Result, error)
}

var versionCheckCmd = &cobra.Command{
	Use:          "check",
	Short:        "Check whether a newer Beacon CLI release is available",
	SilenceUsage: true,
	RunE:         runVersionCheck,
}

var (
	versionCheck = false

	newVersionChecker = func(currentVersion string) versionChecker {
		return updatecheck.DefaultChecker(currentVersion)
	}
	versionCheckTimeout = 2 * time.Second
)

// notUpdatedFromPublicReleases is what the update commands print in a build that is not updated
// from Beacon's public releases (updatecheck.PublicReleases is false).
const notUpdatedFromPublicReleases = "This build of Beacon is not updated from Beacon's public releases."

// versionRefreshCmd is the background half of the startup notice: bare `beacon` starts it
// detached when the cache is stale, and it only updates the cache. It prints nothing, so a
// failure leaves the previous cached release in place until the next TTL.
var versionRefreshCmd = &cobra.Command{
	Use:          "refresh",
	Short:        "Refresh the cached update check",
	Hidden:       true,
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if updatecheck.Disabled(os.Getenv) || !updatecheck.CanCheckVersion(version.GetVersion()) {
			return nil
		}
		path := updateCachePath()
		if !updatecheck.LoadCache(path).Stale(rootNow()) {
			return nil
		}
		_, _ = checkAndRecord(cmd)
		return nil
	},
}

func init() {
	versionCmd.AddCommand(versionCheckCmd)
	versionCmd.AddCommand(versionRefreshCmd)
}

// checkAndRecord runs one release lookup and folds it into the update-check cache, so the
// explicit check and the background refresh leave the startup notice with the same answer.
func checkAndRecord(cmd *cobra.Command) (updatecheck.Result, error) {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, versionCheckTimeout)
	defer cancel()

	current := version.GetVersion()
	result, err := newVersionChecker(current).Check(ctx)
	if updatecheck.CanCheckVersion(current) {
		path := updateCachePath()
		_ = updatecheck.SaveCache(path, updatecheck.LoadCache(path).Record(result, err, rootNow()))
	}
	return result, err
}

func runVersionCheck(cmd *cobra.Command, args []string) error {
	if !updatecheck.PublicReleases {
		fmt.Fprintln(cmd.OutOrStdout(), notUpdatedFromPublicReleases)
		return nil
	}
	result, err := checkAndRecord(cmd)
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
		fmt.Fprintln(out, "If installed with Homebrew: brew update && brew upgrade beacon")
		fmt.Fprintf(out, "Download: %s\n", releaseDownloadURL(result))
		return nil
	}
	fmt.Fprintf(out, "Beacon %s is up to date.\n", result.CurrentVersion)
	return nil
}
