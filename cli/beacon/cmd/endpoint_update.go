package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var endpointUpdateOpts struct {
	check         bool
	apply         bool
	allowInsecure bool
	installPrefix string
}

var detectUpdateInstall = selfupdate.DetectInstall

var endpointUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for Beacon endpoint agent updates",
	Long: `Check for a newer signed and notarized Beacon endpoint package without
downloading or applying it by default. Use --apply explicitly to download,
verify, install, health-check, and roll back if needed.`,
	SilenceUsage: true,
	RunE:         runEndpointUpdate,
}

func init() {
	// `update` lives under `endpoint` only; a top-level `update` command is
	// intentionally not exposed (the public command-tree smoke test forbids it).
	endpointCmd.AddCommand(endpointUpdateCmd)
	endpointUpdateCmd.Flags().BoolVar(&endpointUpdateOpts.check, "check", false, "Only report whether an update is available; do not apply")
	endpointUpdateCmd.Flags().BoolVar(&endpointUpdateOpts.apply, "apply", false, "Download, verify, and apply an available endpoint package update")
	endpointUpdateCmd.Flags().BoolVar(&endpointUpdateOpts.allowInsecure, "allow-insecure-test", false, "Test only: apply into --install-prefix and skip package notarization checks")
	endpointUpdateCmd.Flags().StringVar(&endpointUpdateOpts.installPrefix, "install-prefix", "", "Test only: install root instead of /")
	_ = endpointUpdateCmd.Flags().MarkHidden("allow-insecure-test")
	_ = endpointUpdateCmd.Flags().MarkHidden("install-prefix")
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
		func() (string, error) { return autoUpdateModeFromPath(endpointconfig.ConfigPath(false)) },
		func() (string, error) { return autoUpdateModeFromPath(endpointconfig.ConfigPath(true)) },
	)
}

func configAutoUpdateModeFrom(loadSystem, loadUser func() (string, error)) string {
	if mode, err := loadSystem(); err == nil {
		return mode
	} else if !os.IsNotExist(err) {
		return ""
	}
	if mode, err := loadUser(); err == nil {
		return mode
	}
	return ""
}

func autoUpdateModeFromPath(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var partial struct {
		AutoUpdate *endpointconfig.AutoUpdate `json:"auto_update"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		return "", err
	}
	if partial.AutoUpdate == nil {
		return "", nil
	}
	return partial.AutoUpdate.Mode, nil
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
		_ = emitManualCheckEvent(selfupdate.CheckEventOptions{
			Result:       res,
			Action:       selfupdate.EventCheckFailed,
			Reason:       err.Error(),
			LogPath:      endpointSystemLogPath(),
			AgentVersion: current,
		})
		return fmt.Errorf("update check failed: %w", err)
	}
	action, reason := selfupdate.CheckOutcome(res)
	if err := emitManualCheckEvent(selfupdate.CheckEventOptions{
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

	if err := maybeReturnAfterUpdateReport(res); err != nil || !endpointUpdateOpts.apply || endpointUpdateOpts.check {
		return err
	}

	return applyUpdate(cmd.Context(), current)
}

func maybeReturnAfterUpdateReport(res selfupdate.CheckResult) error {
	if endpointUpdateOpts.check && endpointUpdateOpts.apply {
		fmt.Println("--check was set; no package was downloaded or applied.")
		return nil
	}

	if !endpointUpdateOpts.apply {
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

	if !endpointUpdateOpts.allowInsecure {
		switch res.Install.Kind {
		case selfupdate.InstallHomebrew:
			fmt.Println("Installed via Homebrew. Update with:")
			fmt.Println("  brew upgrade beacon")
			return fmt.Errorf("manual apply is not supported for Homebrew installs")
		case selfupdate.InstallOther:
			return fmt.Errorf("manual apply is supported only for the system package install (detected: %s)", res.Install.Kind)
		}
		if !res.HasArtifact {
			return fmt.Errorf("no signed endpoint package artifact is available for %s", res.ArchKey)
		}
	}
	return nil
}

func emitManualCheckEvent(opts selfupdate.CheckEventOptions) error {
	err := selfupdate.EmitCheckEvent(opts)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrPermission) || os.IsPermission(err) {
		fmt.Fprintf(os.Stderr, "warning: could not write update check event to system log: %v\n", err)
		return nil
	}
	return err
}

func applyUpdate(parent context.Context, current string) error {
	insecure := endpointUpdateOpts.allowInsecure
	if !insecure {
		if os.Geteuid() != 0 {
			return fmt.Errorf("applying an update requires root; rerun with sudo")
		}
		if install := detectUpdateInstall(); !install.SupportsSeamlessUpdate() {
			return fmt.Errorf("automatic apply is only supported for the system package install (detected: %s)", install.Kind)
		}
	} else if endpointUpdateOpts.installPrefix == "" {
		return fmt.Errorf("--allow-insecure-test requires --install-prefix")
	}

	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()

	applier := selfupdate.NewApplier(current)
	applier.LogPath = endpointSystemLogPath()
	if insecure {
		applier.AllowInsecureTest = true
		applier.SkipGatekeeper = true
		if endpointUpdateOpts.installPrefix != "" {
			applier.InstallPrefix = endpointUpdateOpts.installPrefix
			applier.StageDir = filepath.Join(endpointUpdateOpts.installPrefix, "stage")
			applier.LogPath = filepath.Join(endpointUpdateOpts.installPrefix, "system.jsonl")
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
