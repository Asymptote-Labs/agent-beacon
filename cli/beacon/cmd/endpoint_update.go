package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var endpointUpdateOpts struct {
	check bool
}

var detectUpdateInstall = selfupdate.DetectInstall

var endpointUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for Beacon endpoint agent updates",
	Long: `Check for a newer signed and notarized Beacon endpoint package without
downloading or applying it. Phase 1 is check-only: it can surface update status
and write local system-log events, but it never mutates the install.`,
	SilenceUsage: true,
	RunE:         runEndpointUpdate,
}

func init() {
	// `update` lives under `endpoint` only; a top-level `update` command is
	// intentionally not exposed (the public command-tree smoke test forbids it).
	endpointCmd.AddCommand(endpointUpdateCmd)
	endpointUpdateCmd.Flags().BoolVar(&endpointUpdateOpts.check, "check", false, "Only report whether an update is available; do not apply")
}

// configAutoUpdateMode returns the locally configured auto-update mode.
//
// The system config is authoritative for the system install the self-updater
// governs: if it loads at all, its value wins (empty means "use the default"),
// and we never fall through to a user-mode config an operator could set under
// ~/.beacon — which, being visible under sudo, would otherwise override the
// system/managed intent. The user config is consulted only when there is no
// system config (i.e. a user-mode install).
func configAutoUpdateMode() string {
	return configAutoUpdateModeFrom(
		func() (endpointconfig.Config, error) { return endpointconfig.Load(false) },
		func() (endpointconfig.Config, error) { return endpointconfig.Load(true) },
	)
}

func configAutoUpdateModeFrom(loadSystem, loadUser func() (endpointconfig.Config, error)) string {
	if cfg, err := loadSystem(); err == nil {
		if cfg.AutoUpdate != nil {
			return cfg.AutoUpdate.Mode
		}
		return ""
	} else if !os.IsNotExist(err) {
		return ""
	}
	if cfg, err := loadUser(); err == nil && cfg.AutoUpdate != nil {
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
		_ = selfupdate.EmitCheckEvent(selfupdate.CheckEventOptions{
			Result:       res,
			Action:       selfupdate.EventCheckFailed,
			Reason:       err.Error(),
			LogPath:      endpointSystemLogPath(),
			AgentVersion: current,
		})
		return fmt.Errorf("update check failed: %w", err)
	}
	action, reason := selfupdate.CheckOutcome(res)
	if err := selfupdate.EmitCheckEvent(selfupdate.CheckEventOptions{
		Result:       res,
		Action:       action,
		Reason:       reason,
		LogPath:      endpointSystemLogPath(),
		AgentVersion: current,
	}); err != nil {
		return fmt.Errorf("write update check event: %w", err)
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

	fmt.Printf("Beacon %s is available (current: %s).\n", res.LatestVersion, res.CurrentVersion)
	if res.BelowMinSupported {
		fmt.Printf("This build is below the minimum supported version; updating is strongly recommended.\n")
	}

	switch res.Install.Kind {
	case selfupdate.InstallHomebrew:
		fmt.Println("Installed via Homebrew. Update with:")
		fmt.Println("  brew upgrade beacon")
		return nil
	case selfupdate.InstallOther:
		fmt.Printf("Endpoint package checks apply only to the system package install.\n")
		return nil
	}

	if !res.HasArtifact {
		fmt.Printf("No signed endpoint package artifact is available for %s.\n", res.ArchKey)
		return nil
	}

	fmt.Println("Check-only mode: no package was downloaded or applied.")
	return nil
}
