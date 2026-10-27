package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// `beacon endpoint enforcement enable` turns on policy enforcement for the agents on this machine.
// Enforcement is part of Beacon's enterprise edition, not this one, so here enable installs that
// edition on a Mac connected to its organization in system mode, then runs the installed binary's
// own enable. A build that carries enforcement sets activateEnforcement, which enable runs instead,
// and adds its other subcommands to endpointEnforcementCmd.

// activateEnforcement turns enforcement on in a build that carries it. Nil in this repository.
var activateEnforcement func(cmd *cobra.Command, dashboardURL string) error

// enforcementHandoffEnv marks the installed binary's enable as started by this one, so a binary
// without enforcement refuses instead of installing again.
const enforcementHandoffEnv = "BEACON_ENFORCEMENT_HANDOFF"

const (
	enforcementLocateTimeout  = 30 * time.Second
	enforcementInstallTimeout = 15 * time.Minute
)

var endpointEnforcementOpts struct {
	dashboardURL string
}

var endpointEnforcementCmd = &cobra.Command{
	Use:   "enforcement",
	Short: "Policy enforcement for AI coding agents (enterprise edition)",
	Long: `Policy enforcement for AI coding agents, part of Beacon's enterprise edition.

enable installs the enterprise edition on a Mac that is connected to its organization in system
mode (beacon endpoint connect --system), if the organization has enforcement, and turns it on.`,
}

var endpointEnforcementEnableCmd = &cobra.Command{
	Use:          "enable",
	Short:        "Turn on policy enforcement on this machine (needs sudo)",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runEndpointEnforcementEnable,
}

// Seams, so tests reach none of the machine, the platform or the installer.
var (
	enforcementPlatformSupported  = func() bool { return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" }
	enforcementHasRoot            = lifecycle.HasSystemPrivileges
	enforcementConnected          = func() bool { return asymptote.Connected(false) }
	enforcementCredentials        = systemEnforcementCredentials
	enforcementUserModeProbe      = probeConsoleUserMode
	installedBeaconVersionLine    = defaultInstalledBeaconVersionLine
	locateEnterpriseRelease       = edition.Locate
	installEnterpriseRelease      = edition.Install
	runInstalledEnforcementEnable = defaultRunInstalledEnforcementEnable
)

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

// checkEnforcementPrerequisites refuses, before anything is downloaded, on a machine that cannot
// run enforcement: anything but a Mac in system mode connected to its organization, with no
// user-mode Beacon running for the person at the console.
func checkEnforcementPrerequisites() error {
	if !enforcementPlatformSupported() {
		return errors.New("policy enforcement is available for macOS on Apple Silicon only")
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

// systemEnforcementCredentials reads the platform this machine connected to and its device key.
// The key is read into memory for the one request that proves the device; it is never copied.
func systemEnforcementCredentials() (string, string, error) {
	enrollment, err := asymptote.LoadEnrollment(false)
	if err != nil {
		return "", "", fmt.Errorf("reading this machine's enrollment: %w", err)
	}
	key, err := asymptote.ReadDeviceKey(false)
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

// probeConsoleUserMode reports what shows the console user running Beacon in user mode: its
// collector or forwarder loaded in their launchd session, or Claude Code hooks that write to their
// own runtime log. Enforcement runs in system mode only, and enabling it would repoint those hooks.
func probeConsoleUserMode() (string, []string, error) {
	info, ok, err := activeConsoleUser()
	if err != nil || !ok {
		return "", nil, err
	}
	var found []string
	if u, err := user.Lookup(info.Username); err == nil {
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
	return info.Username, found, nil
}

func userModeActiveError(who string, found []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Beacon is running in user mode for %s, and enforcement runs in system mode only:\n", who)
	for _, f := range found {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	fmt.Fprintf(&b, "Stop user mode first. As %s:\n", who)
	for _, label := range []string{service.UserLabel, service.ForwarderLabel, service.InventoryLabel} {
		fmt.Fprintf(&b, "  launchctl bootout gui/$(id -u)/%s; launchctl disable gui/$(id -u)/%s\n", label, label)
	}
	fmt.Fprintf(&b, "then point the hooks at system mode and run this again:\n")
	fmt.Fprintf(&b, "  sudo %s endpoint user-config repair-installed --system --harness claude,codex", selfupdate.SystemBeaconPath())
	return errors.New(b.String())
}

func defaultInstalledBeaconVersionLine(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, selfupdate.SystemBeaconPath(), "version").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// defaultRunInstalledEnforcementEnable runs the installed binary's enable, which turns enforcement
// on. It inherits this process's terminal.
func defaultRunInstalledEnforcementEnable(ctx context.Context, dashboardURL string) error {
	args := []string{"endpoint", "enforcement", "enable"}
	if dashboardURL != "" {
		args = append(args, "--dashboard-url", dashboardURL)
	}
	bin := selfupdate.SystemBeaconPath()
	child := exec.CommandContext(ctx, bin, args...)
	child.Env = append(os.Environ(), enforcementHandoffEnv+"=1")
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := child.Run(); err != nil {
		return fmt.Errorf("the enterprise edition is installed, but turning enforcement on did not finish (%v). Run it again with:\n  sudo %s endpoint enforcement enable", err, bin)
	}
	return nil
}

func runningBeaconPath() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "beacon"
}
