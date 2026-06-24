package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var endpointUpdateServiceOpts struct {
	checkOnly bool
	scheduled bool
}

var endpointUpdateEnableCmd = &cobra.Command{
	Use:          "enable",
	Short:        "Enable check-only update monitoring",
	SilenceUsage: true,
	RunE:         runUpdateEnable,
}

var endpointUpdateDisableCmd = &cobra.Command{
	Use:          "disable",
	Short:        "Disable automatic updates and remove the background updater",
	SilenceUsage: true,
	RunE:         runUpdateDisable,
}

var endpointUpdateStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show auto-update mode and updater daemon status",
	SilenceUsage: true,
	RunE:         runUpdateStatus,
}

func init() {
	endpointUpdateCmd.AddCommand(endpointUpdateEnableCmd)
	endpointUpdateCmd.AddCommand(endpointUpdateDisableCmd)
	endpointUpdateCmd.AddCommand(endpointUpdateStatusCmd)
	endpointUpdateEnableCmd.Flags().BoolVar(&endpointUpdateServiceOpts.checkOnly, "check-only", false, "Enable periodic checks without applying updates")
	// The launchd job runs `update --scheduled`; Phase 1 is check-only. Hidden
	// from the user-facing help.
	endpointUpdateCmd.Flags().BoolVar(&endpointUpdateServiceOpts.scheduled, "scheduled", false, "Internal: run as the scheduled updater job")
	_ = endpointUpdateCmd.Flags().MarkHidden("scheduled")
}

// setConfigAutoUpdateMode persists the auto-update mode in the system endpoint
// config, creating a default config if none exists.
func setConfigAutoUpdateMode(mode string) error {
	return setConfigAutoUpdateModeAt(endpointconfig.ConfigPath(false), mode)
}

func setConfigAutoUpdateModeAt(path, mode string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		cfg := endpointconfig.Default(false, writer.DefaultPath(false))
		cfg.AutoUpdate = &endpointconfig.AutoUpdate{Mode: mode}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0644)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	autoUpdate, err := json.Marshal(endpointconfig.AutoUpdate{Mode: mode})
	if err != nil {
		return err
	}
	raw["auto_update"] = autoUpdate
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

func requireRootForUpdater() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("managing the background updater requires root; rerun with sudo")
	}
	return nil
}

func runUpdateEnable(cmd *cobra.Command, args []string) error {
	if err := requireRootForUpdater(); err != nil {
		return err
	}
	if err := requireSystemPackageForUpdater(detectUpdateInstall()); err != nil {
		return err
	}
	if !endpointUpdateServiceOpts.checkOnly {
		return fmt.Errorf("phase 1 supports check-only monitoring only; rerun with --check-only")
	}
	mode := selfupdate.ModeCheckOnly
	if err := setConfigAutoUpdateMode(string(mode)); err != nil {
		return fmt.Errorf("write auto-update config: %w", err)
	}
	mgr := service.UpdaterManager{}
	if _, err := mgr.WritePlist(selfupdate.SystemBeaconPath()); err != nil {
		return fmt.Errorf("write updater plist: %w", err)
	}
	if err := mgr.Load(); err != nil {
		return fmt.Errorf("load updater job: %w", err)
	}
	fmt.Printf("Update checks enabled (mode: %s).\n", mode)
	return nil
}

func requireSystemPackageForUpdater(install selfupdate.Install) error {
	if install.SupportsSeamlessUpdate() {
		return nil
	}
	return fmt.Errorf("background update checks require the system package install (detected: %s)", install.Kind)
}

func runUpdateDisable(cmd *cobra.Command, args []string) error {
	if err := requireRootForUpdater(); err != nil {
		return err
	}
	if err := setConfigAutoUpdateMode(string(selfupdate.ModeOff)); err != nil {
		return fmt.Errorf("write auto-update config: %w", err)
	}
	mgr := service.UpdaterManager{}
	if err := mgr.Unload(); err != nil {
		return fmt.Errorf("unload updater job: %w", err)
	}
	_ = os.Remove(mgr.PlistPath())
	fmt.Println("Update checks disabled; background updater removed.")
	return nil
}

func runUpdateStatus(cmd *cobra.Command, args []string) error {
	mode := selfupdate.ResolveMode(configAutoUpdateMode())
	install := selfupdate.DetectInstall()
	fmt.Printf("Update mode:      %s\n", mode)
	fmt.Printf("Install kind:     %s\n", install.Kind)
	st := service.UpdaterManager{}.Status()
	fmt.Printf("Updater daemon:   loaded=%t running=%t\n", st.Loaded, st.Running)
	if st.Message != "" {
		fmt.Printf("  %s\n", st.Message)
	}
	return nil
}

// runScheduledUpdate is the entrypoint the launchd job calls. Phase 1 only
// performs check-only monitoring and writes a local system-log event.
func runScheduledUpdate(parent context.Context) error {
	mode := selfupdate.ResolveMode(configAutoUpdateMode())
	if mode == selfupdate.ModeOff {
		current := version.GetVersion()
		res := selfupdate.CheckResult{Mode: mode}
		_ = selfupdate.EmitCheckEvent(selfupdate.CheckEventOptions{
			Result:       res,
			Action:       selfupdate.EventUnsupported,
			Reason:       "mode_off",
			LogPath:      endpointSystemLogPath(),
			AgentVersion: current,
		})
		fmt.Println("Update checks are off; nothing to do.")
		return nil
	}
	if mode != selfupdate.ModeCheckOnly {
		current := version.GetVersion()
		res := selfupdate.CheckResult{Mode: mode}
		if err := selfupdate.EmitCheckEvent(selfupdate.CheckEventOptions{
			Result:       res,
			Action:       selfupdate.EventUnsupported,
			Reason:       "mode_not_supported_in_phase1",
			LogPath:      endpointSystemLogPath(),
			AgentVersion: current,
		}); err != nil {
			return fmt.Errorf("write update check event: %w", err)
		}
		fmt.Printf("Phase 1 scheduled updater supports check-only mode only (resolved: %s).\n", mode)
		return nil
	}

	current := version.GetVersion()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
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
	if action == selfupdate.EventAvailable {
		fmt.Printf("Update available: %s (current %s). check-only mode; not applying.\n", res.LatestVersion, res.CurrentVersion)
	}
	return nil
}

func endpointSystemLogPath() string {
	if endpointUpdateServiceOpts.scheduled {
		return selfupdate.SystemLogPath(writer.DefaultPath(false), false)
	}
	if endpointOpts.logPath == "" && detectUpdateInstall().SupportsSeamlessUpdate() {
		return selfupdate.SystemLogPath(writer.DefaultPath(false), false)
	}
	cfg := loadOrDefaultConfig()
	if endpointOpts.logPath != "" {
		cfg.LogPath = endpointOpts.logPath
	}
	return selfupdate.SystemLogPath(cfg.LogPath, cfg.UserMode)
}
