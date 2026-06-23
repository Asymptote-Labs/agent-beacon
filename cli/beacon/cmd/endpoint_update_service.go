package cmd

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// jitterEnv overrides the maximum scheduled-run jitter (seconds). Default 1800;
// 0 disables jitter (used in tests and manual kicks).
const jitterEnv = "BEACON_UPDATE_JITTER_SECONDS"
const defaultJitterSeconds = 1800

var endpointUpdateServiceOpts struct {
	checkOnly bool
	scheduled bool
}

var endpointUpdateEnableCmd = &cobra.Command{
	Use:          "enable",
	Short:        "Enable automatic updates and install the background updater",
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

// endpointUpdateInstallDaemonCmd is invoked by the package postinstall. It
// reconciles the launchd updater job with the resolved mode without changing the
// stored mode, so a prior `off` is respected across package updates.
var endpointUpdateInstallDaemonCmd = &cobra.Command{
	Use:          "install-daemon",
	Short:        "Internal: reconcile the updater launchd job with the configured mode",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runUpdateInstallDaemon,
}

func init() {
	endpointUpdateCmd.AddCommand(endpointUpdateEnableCmd)
	endpointUpdateCmd.AddCommand(endpointUpdateDisableCmd)
	endpointUpdateCmd.AddCommand(endpointUpdateStatusCmd)
	endpointUpdateCmd.AddCommand(endpointUpdateInstallDaemonCmd)
	endpointUpdateEnableCmd.Flags().BoolVar(&endpointUpdateServiceOpts.checkOnly, "check-only", false, "Check for updates and surface them, but do not apply automatically")
	// The launchd job runs `update --scheduled`; it resolves the mode and
	// either checks or applies. Hidden from the user-facing help.
	endpointUpdateCmd.Flags().BoolVar(&endpointUpdateServiceOpts.scheduled, "scheduled", false, "Internal: run as the scheduled updater job")
	_ = endpointUpdateCmd.Flags().MarkHidden("scheduled")
}

// setConfigAutoUpdateMode persists the auto-update mode in the system endpoint
// config, creating a default config if none exists.
func setConfigAutoUpdateMode(mode string) error {
	cfg, err := endpointconfig.Load(false)
	if err != nil {
		cfg = endpointconfig.Default(false, writer.DefaultPath(false))
	}
	cfg.UserMode = false
	cfg.AutoUpdate = &endpointconfig.AutoUpdate{Mode: mode}
	_, err = endpointconfig.Save(cfg)
	return err
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
	mode := selfupdate.ModeAuto
	if endpointUpdateServiceOpts.checkOnly {
		mode = selfupdate.ModeCheckOnly
	}
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
	fmt.Printf("Automatic updates enabled (mode: %s).\n", mode)
	return nil
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
	fmt.Println("Automatic updates disabled; background updater removed.")
	return nil
}

func runUpdateStatus(cmd *cobra.Command, args []string) error {
	mode := selfupdate.ResolveMode(configAutoUpdateMode())
	install := selfupdate.DetectInstall()
	fmt.Printf("Auto-update mode: %s\n", mode)
	fmt.Printf("Install kind:     %s\n", install.Kind)
	st := service.UpdaterManager{}.Status()
	fmt.Printf("Updater daemon:   loaded=%t running=%t\n", st.Loaded, st.Running)
	if st.Message != "" {
		fmt.Printf("  %s\n", st.Message)
	}
	return nil
}

func runUpdateInstallDaemon(cmd *cobra.Command, args []string) error {
	if err := requireRootForUpdater(); err != nil {
		return err
	}
	mode := selfupdate.ResolveMode(configAutoUpdateMode())
	mgr := service.UpdaterManager{}
	if mode == selfupdate.ModeOff {
		_ = mgr.Unload()
		_ = os.Remove(mgr.PlistPath())
		fmt.Println("Auto-update is off; updater daemon not installed.")
		return nil
	}
	if _, err := mgr.WritePlist(selfupdate.SystemBeaconPath()); err != nil {
		return fmt.Errorf("write updater plist: %w", err)
	}
	if err := mgr.Load(); err != nil {
		return fmt.Errorf("load updater job: %w", err)
	}
	fmt.Printf("Background updater installed (mode: %s).\n", mode)
	return nil
}

// runScheduledUpdate is the entrypoint the launchd job calls. It resolves the
// effective mode and either checks-only or applies, after a randomized delay to
// spread fleet-wide load.
func runScheduledUpdate(parent context.Context) error {
	mode := selfupdate.ResolveMode(configAutoUpdateMode())
	if mode == selfupdate.ModeOff {
		fmt.Println("Auto-update is off; nothing to do.")
		return nil
	}
	sleepJitter()

	current := version.GetVersion()
	if mode == selfupdate.ModeCheckOnly {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		res, err := selfupdate.CheckWithMode(ctx, current, configAutoUpdateMode())
		if err != nil {
			return fmt.Errorf("update check failed: %w", err)
		}
		if res.UpdateAvailable {
			fmt.Printf("Update available: %s (current %s). check-only mode; not applying.\n", res.LatestVersion, res.CurrentVersion)
		}
		return nil
	}
	// auto
	return applyUpdate(parent, current)
}

func sleepJitter() {
	max := defaultJitterSeconds
	if v := os.Getenv(jitterEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			max = n
		}
	}
	if max <= 0 {
		return
	}
	d := time.Duration(rand.Intn(max)) * time.Second
	time.Sleep(d)
}
