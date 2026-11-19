package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// `beacon endpoint enforcement enable` turns on policy enforcement for the agents on this machine.
// Enforcement is part of Beacon's enterprise edition, not this one, so here enable installs that
// edition, then runs the installed binary's own enable. A build that carries enforcement sets
// activateEnforcement, which enable runs instead, and adds its other subcommands to
// endpointEnforcementCmd.
//
// On a Mac or Linux machine connected to its organization in system mode, run as root, the
// edition's package (the macOS .pkg, or the .deb or .rpm on Linux) is installed into /opt/beacon.
// On one connected to Beacon Cloud in user mode, run without root, only the edition's beacon is
// kept, in the user's endpoint directory (userEnterpriseBeaconPath); this build then hands that
// binary the enforcement subcommands it lacks and, while enforcement is on, its hook installs,
// whose enforce entries only the enterprise edition writes.

// activateEnforcement turns enforcement on in a build that carries it. Nil in this repository.
var activateEnforcement func(cmd *cobra.Command, dashboardURL string) error

// enforcementHandoffEnv marks the installed binary's enable as started by this one, so a binary
// without enforcement refuses instead of installing again.
const enforcementHandoffEnv = "BEACON_ENFORCEMENT_HANDOFF"

const (
	enforcementLocateTimeout  = 30 * time.Second
	enforcementInstallTimeout = 15 * time.Minute
)

// Where enforcement lives under an endpoint directory (the user's or the system's), and where
// user-mode enforcement keeps the enterprise binary. The enforcement directory and its config
// file are the enterprise edition's enforcement.Dir and enforcement.ConfigFile, which its enable
// writes; the config's presence is what says enforcement is on.
const (
	userEnterpriseDirName = "enterprise"
	enforcementDirName    = "enforcement"
	enforcementConfigName = "config.json"
)

var endpointEnforcementOpts struct {
	dashboardURL string
}

var endpointEnforcementCmd = &cobra.Command{
	Use:   "enforcement",
	Short: "Policy enforcement for AI coding agents (enterprise edition)",
	Long: `Policy enforcement for AI coding agents, part of Beacon's enterprise edition.

enable installs the enterprise edition, if the platform this machine is connected to offers it,
and turns enforcement on. It runs on macOS (Apple Silicon) and Linux (amd64, arm64):
  - on a machine connected to Beacon Cloud in user mode (beacon endpoint connect), run without
    sudo: for this user, with the edition kept in ~/.beacon/endpoint/enterprise
  - on a machine connected to its organization in system mode (beacon endpoint connect --system),
    run with sudo: for the machine, with the edition installed into /opt/beacon`,
	// Unknown subcommands land here: status, update and disable come with the enterprise edition,
	// so a user-mode install passes them, flags and all, to the one it keeps.
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true,
	RunE:               runEndpointEnforcement,
}

var endpointEnforcementEnableCmd = &cobra.Command{
	Use:          "enable",
	Short:        "Turn on policy enforcement (sudo on a system-mode install)",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runEndpointEnforcementEnable,
}

// Seams, so tests reach none of the machine, the platform or the installer.
var (
	enforcementPlatformSupported  = defaultEnforcementPlatformSupported
	enforcementHasRoot            = lifecycle.HasSystemPrivileges
	enforcementConnected          = func() bool { return asymptote.Connected(false) }
	enforcementUserConnected      = func() bool { return asymptote.Connected(true) }
	enforcementSystemBaseDir      = endpointconfig.SystemBaseDir
	enforcementCredentials        = systemEnforcementCredentials
	enforcementUserCredentials    = userEnforcementCredentials
	enforcementUserModeProbe      = probeConsoleUserMode
	installedBeaconVersionLine    = defaultInstalledBeaconVersionLine
	userEnterpriseVersionLine     = defaultUserEnterpriseVersionLine
	locateEnterpriseRelease       = edition.Locate
	locateUserEnterpriseRelease   = defaultLocateUserEnterpriseRelease
	installEnterpriseRelease      = edition.Install
	installUserEnterpriseRelease  = edition.InstallUser
	runInstalledEnforcementEnable = defaultRunInstalledEnforcementEnable
	runUserEnforcementEnable      = defaultRunUserEnforcementEnable
	runUserEnterpriseBeacon       = defaultRunUserEnterpriseBeacon
)

// errEnforcementPlatform is enable on a machine the enterprise edition is not built for.
var errEnforcementPlatform = errors.New("policy enforcement is available for macOS on Apple Silicon and for Linux on amd64 and arm64 only")

// defaultEnforcementPlatformSupported reports whether the enterprise edition is built for this
// machine: Apple Silicon Macs, and Linux on amd64 and arm64.
func defaultEnforcementPlatformSupported() bool {
	switch runtime.GOOS {
	case "darwin":
		return runtime.GOARCH == "arm64"
	case "linux":
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	}
	return false
}

// defaultLocateUserEnterpriseRelease asks for the package a user-mode install takes its beacon
// from, which on Linux is the release's tarball rather than a .deb or .rpm.
func defaultLocateUserEnterpriseRelease(ctx context.Context, client *http.Client, dashboardURL, deviceKey, version string) (edition.Release, error) {
	return edition.LocateFor(ctx, client, dashboardURL, deviceKey, version, edition.UserTarget())
}

func init() {
	endpointEnforcementEnableCmd.Flags().StringVar(&endpointEnforcementOpts.dashboardURL, "dashboard-url", "",
		"platform to ask (default: the one this machine connected to)")
	endpointEnforcementCmd.AddCommand(endpointEnforcementEnableCmd)
	endpointCmd.AddCommand(endpointEnforcementCmd)
}

func runEndpointEnforcementEnable(cmd *cobra.Command, _ []string) error {
	if activateEnforcement != nil {
		return activateEnforcement(cmd, endpointEnforcementOpts.dashboardURL)
	}
	if os.Getenv(enforcementHandoffEnv) != "" {
		return errors.New("the installed beacon does not carry policy enforcement")
	}
	userMode, err := enforcementMode()
	if err != nil {
		return err
	}
	if userMode {
		return enableUserModeEnforcement(cmd)
	}
	if err := checkEnforcementPrerequisites(); err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	ctx, cancel := context.WithTimeout(cmd.Context(), enforcementInstallTimeout)
	defer cancel()
	if selfupdate.VersionLineEdition(installedBeaconVersionLine(ctx)) == edition.Enterprise {
		fmt.Fprintln(out, "The enterprise edition is already installed.")
	} else if err := installEnterpriseEdition(ctx, out); err != nil {
		return err
	}
	return runInstalledEnforcementEnable(cmd.Context(), endpointEnforcementOpts.dashboardURL)
}

// enforcementMode says where enable turns enforcement on: system mode, run as root on a machine
// connected with --system, or user mode, run without root on a machine connected in user mode. Any
// other combination is refused with what to run instead.
func enforcementMode() (userMode bool, err error) {
	if !enforcementPlatformSupported() {
		return false, errEnforcementPlatform
	}
	root := enforcementHasRoot()
	switch {
	case root && enforcementConnected():
		return false, nil
	case !root && enforcementUserConnected():
		return true, nil
	case root:
		return false, fmt.Errorf("this machine is not connected to an organization in system mode. If Beacon runs in user mode here, run this without sudo:\n  beacon endpoint enforcement enable\nTo connect it in system mode instead:\n  sudo %s endpoint connect --system --dashboard-url https://asymptotelabs.ai", selfupdate.SystemBeaconPath())
	case enforcementConnected():
		return false, fmt.Errorf("turning on enforcement installs Beacon's enterprise edition, which needs root:\n  sudo %s endpoint enforcement enable", runningBeaconPath())
	default:
		return false, errors.New("policy enforcement needs Beacon Cloud. Connect this machine first:\n  beacon login && beacon endpoint connect")
	}
}

// enableUserModeEnforcement is enable on a machine connected in user mode. There is no root to run
// the package's installer, whose postinstall would set the machine up in system mode anyway, so
// only the edition's beacon is kept, beside this one, and its enable turns enforcement on for this
// user. Running enable again replaces that binary when the platform has a newer release.
func enableUserModeEnforcement(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	dashboard, deviceKey, err := enforcementUserCredentials()
	if err != nil {
		return err
	}
	if flag := strings.TrimSpace(endpointEnforcementOpts.dashboardURL); flag != "" {
		dashboard = flag
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), enforcementInstallTimeout)
	defer cancel()
	locateCtx, cancelLocate := context.WithTimeout(ctx, enforcementLocateTimeout)
	rel, err := locateUserEnterpriseRelease(locateCtx, nil, dashboard, deviceKey, "")
	cancelLocate()
	if err != nil {
		var refused *edition.HTTPError
		if errors.As(err, &refused) && refused.Status == http.StatusUnauthorized {
			return fmt.Errorf("%s did not accept this machine's device key (revoked or expired?). Reconnect with:\n  beacon endpoint connect", dashboard)
		}
		return explainEnterpriseRefusal(dashboard, err)
	}

	bin := userEnterpriseBeaconPath()
	line := userEnterpriseVersionLine(ctx)
	if selfupdate.VersionLineMatches(line, rel.Version) && selfupdate.VersionLineEdition(line) == edition.Enterprise {
		fmt.Fprintf(out, "Beacon %s, enterprise edition, is already installed.\n", rel.Version)
	} else {
		fmt.Fprintf(out, "Installing Beacon %s, enterprise edition (%.0f MB download)…\n", rel.Version, float64(rel.Size)/1e6)
		if err := installUserEnterpriseRelease(ctx, rel, bin); err != nil {
			return fmt.Errorf("installing the enterprise edition: %w", err)
		}
		fmt.Fprintf(out, "Installed Beacon %s, enterprise edition, at %s.\n", rel.Version, bin)
	}
	return runUserEnforcementEnable(cmd.Context(), bin, endpointEnforcementOpts.dashboardURL)
}

// checkEnforcementPrerequisites refuses, before anything is downloaded, on a machine that cannot
// run enforcement: anything but a Mac or Linux machine in system mode connected to its
// organization, with no user-mode Beacon running for the person at the console.
func checkEnforcementPrerequisites() error {
	if !enforcementPlatformSupported() {
		return errEnforcementPlatform
	}
	if !enforcementHasRoot() {
		return fmt.Errorf("turning on enforcement installs Beacon's enterprise edition, which needs root:\n  sudo %s endpoint enforcement enable", runningBeaconPath())
	}
	if !enforcementConnected() {
		return fmt.Errorf("this machine is not connected to an organization in system mode. Connect it first:\n  sudo %s endpoint connect --system --dashboard-url https://asymptotelabs.ai", selfupdate.SystemBeaconPath())
	}
	who, found, err := enforcementUserModeProbe()
	if err != nil {
		return fmt.Errorf("checking for a user-mode Beacon: %w", err)
	}
	if len(found) > 0 {
		return userModeActiveError(who, found)
	}
	return nil
}

func installEnterpriseEdition(ctx context.Context, out io.Writer) error {
	dashboard, deviceKey, err := enforcementCredentials()
	if err != nil {
		return err
	}
	if flag := strings.TrimSpace(endpointEnforcementOpts.dashboardURL); flag != "" {
		dashboard = flag
	}
	locateCtx, cancel := context.WithTimeout(ctx, enforcementLocateTimeout)
	rel, err := locateEnterpriseRelease(locateCtx, nil, dashboard, deviceKey, "")
	cancel()
	if err != nil {
		return explainEnterpriseRefusal(dashboard, err)
	}
	fmt.Fprintf(out, "Installing Beacon %s, enterprise edition (%.0f MB)…\n", rel.Version, float64(rel.Size)/1e6)
	res, err := installEnterpriseRelease(ctx, rel, version.GetVersion(), endpointSystemLogPath())
	if err != nil {
		if res.RolledBack {
			fmt.Fprintln(out, "The install failed and was rolled back; Beacon is as it was.")
		}
		return fmt.Errorf("installing the enterprise edition: %w", err)
	}
	fmt.Fprintf(out, "Installed Beacon %s, enterprise edition (verified: %s).\n", rel.Version, res.Verification)
	return nil
}

// systemEnforcementCredentials reads the platform this machine connected to in system mode and its
// device key.
func systemEnforcementCredentials() (string, string, error) {
	return enforcementCredentialsFor(false)
}

// userEnforcementCredentials reads the platform this user connected to in user mode and its device
// key.
func userEnforcementCredentials() (string, string, error) {
	return enforcementCredentialsFor(true)
}

// enforcementCredentialsFor reads the platform a mode's connection enrolled with and its device
// key. The key is read into memory for the requests that prove the device; it is never copied.
func enforcementCredentialsFor(userMode bool) (string, string, error) {
	enrollment, err := asymptote.LoadEnrollment(userMode)
	if err != nil {
		return "", "", fmt.Errorf("reading this machine's enrollment: %w", err)
	}
	key, err := asymptote.ReadDeviceKey(userMode)
	if err != nil {
		return "", "", fmt.Errorf("reading this machine's device key: %w", err)
	}
	return enrollment.DashboardURL, key, nil
}

func explainEnterpriseRefusal(dashboard string, err error) error {
	var refused *edition.HTTPError
	if !errors.As(err, &refused) {
		return fmt.Errorf("asking %s for the enterprise edition: %w", dashboard, err)
	}
	switch refused.Status {
	case http.StatusUnauthorized:
		return fmt.Errorf("%s did not accept this machine's device key (revoked or expired?). Reconnect with:\n  sudo %s endpoint connect --system --dashboard-url %s",
			dashboard, selfupdate.SystemBeaconPath(), dashboard)
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%s refused: %s", dashboard, firstNonEmpty(refused.Detail, "policy enforcement is not available for this organization"))
	default:
		return fmt.Errorf("%s could not provide the enterprise edition: %v", dashboard, refused)
	}
}

// probeConsoleUserMode reports what shows a console user running Beacon in user mode: its
// collector or forwarder running in their session (launchd on macOS, their systemd user manager on
// Linux), or Claude Code hooks that write to their own runtime log. Enforcement runs in system mode
// only, and enabling it would repoint those hooks. It asks about everyone the system install
// configures (activeConsoleUsers): on Linux every logged-in user, not only the one at the console,
// and names those it found in user mode.
func probeConsoleUserMode() (string, []string, error) {
	users, err := activeConsoleUsers()
	if err != nil || len(users) == 0 {
		return "", nil, err
	}
	var who, found []string
	for _, info := range users {
		if f := consoleUserModeFindings(info); len(f) > 0 {
			who = append(who, info.Username)
			found = append(found, f...)
		}
	}
	return strings.Join(who, ", "), found, nil
}

// consoleUserModeFindings is what shows one user running Beacon in user mode.
var consoleUserModeFindings = func(info consoleUserInfo) []string {
	var found []string
	if runtime.GOOS == "linux" {
		found = linuxUserModeUnits(info, systemdUserUnitState)
	} else if u, err := user.Lookup(info.Username); err == nil {
		for _, label := range []string{service.UserLabel, service.ForwarderLabel} {
			if exec.Command("launchctl", "print", "gui/"+u.Uid+"/"+label).Run() == nil {
				found = append(found, fmt.Sprintf("%s is loaded in %s's session", label, info.Username))
			}
		}
	}
	userLog := filepath.Join(info.HomeDir, writer.UserLogPath)
	settings := filepath.Join(info.HomeDir, ".claude", "settings.json")
	if data, err := os.ReadFile(settings); err == nil && strings.Contains(string(data), userLog) {
		found = append(found, fmt.Sprintf("Claude Code hooks in %s write to %s", settings, userLog))
	}
	return found
}

// linuxUserModeUnits reports the user-mode collector and forwarder units Beacon installed for a
// user (their files under ~/.config/systemd/user) that are running, or whose state their systemd
// user manager could not report: asking another user's manager (systemctl --machine=<user>@) needs
// systemd 248, older than RHEL 8's, Ubuntu 20.04's and Debian 11's, and a unit that may be running
// must not read as stopped.
func linuxUserModeUnits(info consoleUserInfo, state func(username, unit string) string) []string {
	var found []string
	for _, unit := range userModeSystemdUnits[:2] {
		file := filepath.Join(info.HomeDir, ".config", "systemd", "user", unit)
		if _, err := os.Stat(file); err != nil {
			continue
		}
		switch s := state(info.Username, unit); s {
		case "active", "activating", "reloading", "refreshing":
			found = append(found, fmt.Sprintf("%s is running in %s's session", unit, info.Username))
		case "inactive", "failed", "deactivating", "maintenance":
		default:
			found = append(found, fmt.Sprintf("%s is installed for %s (%s), and systemd could not say whether it is running", unit, info.Username, file))
		}
	}
	return found
}

// systemdUserUnitState is what `systemctl is-active` says of unit in username's user manager, or ""
// when it said nothing (systemctl missing, or one too old to reach another user's manager).
func systemdUserUnitState(username, unit string) string {
	out, _ := exec.Command("systemctl", "--user", "--machine="+username+"@", "is-active", unit).Output()
	state, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return state
}

// userModeSystemdUnits are a user-mode endpoint's systemd user units: the collector and the
// forwarder, which show it running, then its two scheduled jobs.
var userModeSystemdUnits = []string{
	service.SystemdUserUnit, service.ForwarderSystemdUnit, service.InventoryTimerUnit, service.SessionSyncTimerUnit,
}

func userModeActiveError(who string, found []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Beacon is running in user mode for %s, and enforcement runs in system mode only:\n", who)
	for _, f := range found {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	if strings.Contains(who, ", ") {
		fmt.Fprintf(&b, "Stop user mode first. As each of %s:\n", who)
	} else {
		fmt.Fprintf(&b, "Stop user mode first. As %s:\n", who)
	}
	if runtime.GOOS == "linux" {
		fmt.Fprintf(&b, "  systemctl --user disable --now %s\n", strings.Join(userModeSystemdUnits, " "))
	} else {
		for _, label := range []string{service.UserLabel, service.ForwarderLabel, service.InventoryLabel, service.SessionSyncLabel} {
			fmt.Fprintf(&b, "  launchctl bootout gui/$(id -u)/%s; launchctl disable gui/$(id -u)/%s\n", label, label)
		}
	}
	fmt.Fprintf(&b, "then point the hooks at system mode and run this again:\n")
	fmt.Fprintf(&b, "  sudo %s endpoint user-config repair-installed --system --harness claude,codex", selfupdate.SystemBeaconPath())
	return errors.New(b.String())
}

func defaultInstalledBeaconVersionLine(ctx context.Context) string {
	return beaconVersionLine(ctx, selfupdate.SystemBeaconPath())
}

func defaultUserEnterpriseVersionLine(ctx context.Context) string {
	return beaconVersionLine(ctx, userEnterpriseBeaconPath())
}

// beaconVersionLine is what `<bin> version` prints, or "" when it does not run.
func beaconVersionLine(ctx context.Context, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// defaultRunInstalledEnforcementEnable runs the installed binary's enable, which turns enforcement
// on. It inherits this process's terminal.
func defaultRunInstalledEnforcementEnable(ctx context.Context, dashboardURL string) error {
	bin := selfupdate.SystemBeaconPath()
	if err := runEnterpriseEnable(ctx, bin, dashboardURL); err != nil {
		return fmt.Errorf("the enterprise edition is installed, but turning enforcement on did not finish (%v). Run it again with:\n  sudo %s endpoint enforcement enable", err, bin)
	}
	return nil
}

// defaultRunUserEnforcementEnable runs the kept enterprise binary's enable, which turns enforcement
// on for this user.
func defaultRunUserEnforcementEnable(ctx context.Context, bin, dashboardURL string) error {
	if err := runEnterpriseEnable(ctx, bin, dashboardURL); err != nil {
		return fmt.Errorf("the enterprise edition is installed, but turning enforcement on did not finish (%v). Run it again with:\n  beacon endpoint enforcement enable", err)
	}
	return nil
}

func runEnterpriseEnable(ctx context.Context, bin, dashboardURL string) error {
	args := []string{"endpoint", "enforcement", "enable"}
	if dashboardURL != "" {
		args = append(args, "--dashboard-url", dashboardURL)
	}
	return defaultRunUserEnterpriseBeacon(ctx, bin, args...)
}

// defaultRunUserEnterpriseBeacon runs an enterprise binary with args, marked as handed off so a
// binary without enforcement refuses rather than installing again. It inherits this process's
// terminal.
func defaultRunUserEnterpriseBeacon(ctx context.Context, bin string, args ...string) error {
	child := exec.CommandContext(ctx, bin, args...)
	child.Env = append(os.Environ(), enforcementHandoffEnv+"=1")
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	return child.Run()
}

func userEnterpriseBeaconPath() string {
	return filepath.Join(endpointconfig.BaseDir(true), userEnterpriseDirName, "beacon")
}

func userEnforcementDir() string {
	return filepath.Join(endpointconfig.BaseDir(true), enforcementDirName)
}

// userEnterpriseBeacon is the enterprise binary kept for user-mode enforcement, when this build
// should hand it work: only a build without enforcement does, never one handed work itself, and
// never as root, so nothing loops and a system install never runs a user's binary.
func userEnterpriseBeacon() (string, bool) {
	if activateEnforcement != nil || os.Getenv(enforcementHandoffEnv) != "" || enforcementHasRoot() {
		return "", false
	}
	bin := userEnterpriseBeaconPath()
	if info, err := os.Stat(bin); err != nil || info.IsDir() {
		return "", false
	}
	return bin, true
}

// userEnforcementOn reports whether enforcement is on for this user (its enable wrote the config).
func userEnforcementOn() bool {
	_, err := os.Stat(filepath.Join(userEnforcementDir(), enforcementConfigName))
	return err == nil
}

// enforcementOn reports whether enforcement is on here, for this user or for the whole machine.
func enforcementOn() bool {
	if userEnforcementOn() {
		return true
	}
	_, err := os.Stat(filepath.Join(enforcementSystemBaseDir(), enforcementDirName, enforcementConfigName))
	return err == nil
}

// enforcementStatusLine is endpoint status's enforcement line: whether it is on, on the machines
// that can have it (and anywhere it is somehow on).
func enforcementStatusLine() string {
	switch {
	case enforcementOn():
		return "Enforcement: on"
	case enforcementPlatformSupported():
		return "Enforcement: off"
	}
	return ""
}

// runEndpointEnforcement forwards an enforcement subcommand this build lacks (status, update,
// disable) to the enterprise binary kept for user-mode enforcement. Without one, status can still
// say whether enforcement is on, and anything else shows help.
func runEndpointEnforcement(cmd *cobra.Command, args []string) error {
	bin, ok := userEnterpriseBeacon()
	if !ok {
		if len(args) > 0 && args[0] == "status" {
			return printEnforcementOnOrOff(cmd.OutOrStdout(), slices.Contains(args[1:], "--json"))
		}
		return cmd.Help()
	}
	if err := runUserEnterpriseBeacon(cmd.Context(), bin, append([]string{"endpoint", "enforcement"}, args...)...); err != nil {
		// The enterprise binary has already said what went wrong.
		cmd.SilenceErrors = true
		return err
	}
	return nil
}

// printEnforcementOnOrOff is enforcement status without the enterprise edition, which has the
// rest: as its status puts it, or with jsonOut its "enabled" field.
func printEnforcementOnOrOff(out io.Writer, jsonOut bool) error {
	on := enforcementOn()
	if jsonOut {
		return json.NewEncoder(out).Encode(map[string]bool{"enabled": on})
	}
	state := "off"
	if on {
		state = "on"
	}
	_, err := fmt.Fprintf(out, "Enforcement:   %s\n", state)
	return err
}

// enforcedHookBeacon is the kept enterprise binary a user-mode hook install must go through while
// enforcement is on for this user. This build's hooks carry no enforcement, so installing them
// here would quietly take enforcement out of every agent; the enterprise binary writes the same
// hooks plus its enforce entries. It is first brought up to this build's version when older.
func enforcedHookBeacon(ctx context.Context, warnings io.Writer, cfg endpointconfig.Config) (string, bool) {
	if !cfg.UserMode || !userEnforcementOn() {
		return "", false
	}
	bin, ok := userEnterpriseBeacon()
	if ok {
		refreshUserEnterpriseBeacon(ctx, warnings, bin)
	}
	return bin, ok
}

// refreshUserEnterpriseBeacon updates the kept enterprise binary at bin to this build's release
// when it is older: after a community upgrade (brew upgrade) an older one would write older hooks
// and refuse agents it does not know. When that release cannot be had (the enterprise one follows
// the public release, or the machine is offline) it warns, and the kept binary does the install.
func refreshUserEnterpriseBeacon(ctx context.Context, warnings io.Writer, bin string) {
	want := version.GetVersion()
	kept := selfupdate.VersionLineVersion(userEnterpriseVersionLine(ctx))
	if !updatecheck.Older(kept, want) {
		return
	}
	err := func() error {
		dashboard, deviceKey, err := enforcementUserCredentials()
		if err != nil {
			return err
		}
		locateCtx, cancel := context.WithTimeout(ctx, enforcementLocateTimeout)
		rel, err := locateUserEnterpriseRelease(locateCtx, nil, dashboard, deviceKey, want)
		cancel()
		if err != nil {
			return err
		}
		return installUserEnterpriseRelease(ctx, rel, bin)
	}()
	if err != nil {
		fmt.Fprintf(warnings, "Warning: enforcement's hooks come from Beacon %s, older than this one (%s), which could not be updated: %v. Retry later with: beacon endpoint enforcement enable\n", kept, want, err)
	}
}

// installHooksWithEnterpriseBeacon runs `hooks install` through bin for selection: --all, or
// --harness and its targets, which the enterprise binary treats exactly as this one would.
func installHooksWithEnterpriseBeacon(ctx context.Context, bin string, cfg endpointconfig.Config, selection ...string) error {
	args := append([]string{"endpoint", "hooks", "install"}, selection...)
	args = append(args, "--level", endpointOpts.hookLevel, "--log-path", cfg.LogPath)
	return runUserEnterpriseBeacon(ctx, bin, args...)
}

// removeUserEnforcement takes out what user-mode enforcement keeps in the user endpoint directory:
// its token and config, and the enterprise binary. Left behind, a later install would hand its
// hooks to that binary and turn enforcement back on.
func removeUserEnforcement() error {
	for _, dir := range []string{userEnforcementDir(), filepath.Dir(userEnterpriseBeaconPath())} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func runningBeaconPath() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "beacon"
}
