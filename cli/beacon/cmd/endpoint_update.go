package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

const releasesPage = "https://github.com/asymptote-labs/agent-beacon/releases"

// insecureSkipGatekeeperEnv is the test-only escape hatch that relaxes
// notarization/staple checks. It is honored only alongside --allow-insecure-test
// and never by the launchd updater job.
const insecureSkipGatekeeperEnv = "BEACON_UPDATE_INSECURE_SKIP_GATEKEEPER"

var endpointUpdateOpts struct {
	check         bool
	allowInsecure bool
	installPrefix string
}

var endpointUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for and apply Beacon endpoint agent updates",
	Long: `Check for a newer signed and notarized Beacon release and, on a system
install, apply it. Run with --check to only report availability without changing
anything.`,
	SilenceUsage: true,
	RunE:         runEndpointUpdate,
}

var topLevelUpdateCmd = &cobra.Command{
	Use:          "update",
	Short:        "Alias for beacon endpoint update",
	SilenceUsage: true,
	RunE:         runEndpointUpdate,
}

func init() {
	endpointCmd.AddCommand(endpointUpdateCmd)
	rootCmd.AddCommand(topLevelUpdateCmd)
	for _, c := range []*cobra.Command{endpointUpdateCmd, topLevelUpdateCmd} {
		c.Flags().BoolVar(&endpointUpdateOpts.check, "check", false, "Only report whether an update is available; do not apply")
		// Test-only seam: apply into a temp prefix without root/Gatekeeper. Hidden
		// from help; never used by the launchd job or normal operation.
		c.Flags().BoolVar(&endpointUpdateOpts.allowInsecure, "allow-insecure-test", false, "Test only: apply into --install-prefix and honor "+insecureSkipGatekeeperEnv)
		c.Flags().StringVar(&endpointUpdateOpts.installPrefix, "install-prefix", "", "Test only: install root instead of /")
		_ = c.Flags().MarkHidden("allow-insecure-test")
		_ = c.Flags().MarkHidden("install-prefix")
	}
}

// configAutoUpdateMode returns the locally configured auto-update mode, if any.
func configAutoUpdateMode() string {
	if cfg, err := endpointconfig.Load(false); err == nil && cfg.AutoUpdate != nil {
		return cfg.AutoUpdate.Mode
	}
	if cfg, err := endpointconfig.Load(true); err == nil && cfg.AutoUpdate != nil {
		return cfg.AutoUpdate.Mode
	}
	return ""
}

func runEndpointUpdate(cmd *cobra.Command, args []string) error {
	if endpointUpdateServiceOpts.scheduled {
		return runScheduledUpdate(cmd.Context())
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	current := version.GetVersion()
	res, err := selfupdate.CheckWithMode(ctx, current, configAutoUpdateMode())
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}

	switch {
	case res.CurrentIsDev:
		fmt.Println("beacon is a dev build; skipping update check.")
		return nil
	case res.UnsupportedCurrentVersion:
		fmt.Printf("beacon version %q cannot be compared to releases; skipping update check.\n", current)
		return nil
	}

	if !res.UpdateAvailable {
		fmt.Printf("beacon %s is up to date.\n", res.CurrentVersion)
		return nil
	}

	// An update is available. Report it, then decide whether to act.
	fmt.Printf("Beacon %s is available (current: %s).\n", res.LatestVersion, res.CurrentVersion)
	if res.BelowMinSupported {
		fmt.Printf("This build is below the minimum supported version; updating is strongly recommended.\n")
	}

	// The test seam applies into a temp prefix and bypasses install-kind gating.
	if endpointUpdateOpts.allowInsecure {
		return applyUpdate(cmd.Context(), current)
	}

	switch res.Install.Kind {
	case selfupdate.InstallHomebrew:
		fmt.Println("Installed via Homebrew. Update with:")
		fmt.Println("  brew upgrade beacon")
		return nil
	case selfupdate.InstallOther:
		fmt.Printf("Automatic update applies only to the system package install.\n")
		fmt.Printf("Download the latest release from %s\n", releasesPage)
		return nil
	}

	// System package install.
	if endpointUpdateOpts.check {
		fmt.Println("Run `sudo beacon endpoint update` to apply, or let the background updater apply it.")
		return nil
	}

	return applyUpdate(cmd.Context(), current)
}

func applyUpdate(parent context.Context, current string) error {
	insecure := endpointUpdateOpts.allowInsecure

	// Real installs need root; the launchd job runs as root, an interactive
	// user must use sudo. The test seam installs into a temp prefix and skips
	// this requirement.
	if !insecure && os.Geteuid() != 0 {
		return fmt.Errorf("applying an update requires root; rerun with sudo or let the background updater apply it")
	}

	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()

	applier := selfupdate.NewApplier(current)
	if insecure {
		applier.AllowInsecureTest = true
		if endpointUpdateOpts.installPrefix != "" {
			applier.InstallPrefix = endpointUpdateOpts.installPrefix
			// Keep staging and telemetry inside the sandbox prefix so the test
			// seam needs no root and does not touch the real system paths.
			applier.StageDir = filepath.Join(endpointUpdateOpts.installPrefix, "stage")
			applier.LogPath = filepath.Join(endpointUpdateOpts.installPrefix, "runtime.jsonl")
		}
		if os.Getenv(insecureSkipGatekeeperEnv) == "1" {
			applier.SkipGatekeeper = true
		}
	}

	res, err := applier.Apply(ctx)
	if err != nil {
		if res.RolledBack {
			fmt.Println("Update failed; rolled back to the previous version.")
		}
		return err
	}
	if !res.Applied {
		fmt.Printf("No update applied: %s\n", res.Message)
		return nil
	}
	fmt.Printf("Updated Beacon to %s.\n", res.ToVersion)
	return nil
}
