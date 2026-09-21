package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var connectOpts struct {
	dashboardURL    string
	noBrowser       bool
	vectorBin       string
	keepCredentials bool
	privacyMode     string
}

var (
	connectAccountLoad     = account.Load
	connectManagedEndpoint = asymptote.Connect
)

var endpointConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Forward this endpoint's telemetry to Beacon Managed",
	Long: `Connect this endpoint to Beacon Managed.

For a user endpoint, uses the signed-in Beacon account to mint a device-specific
ingest key, stores that key in a private secrets file, and starts a Vector
forwarder. The account token is never given to Vector or copied into endpoint
configuration. System mode retains browser device approval.

Requires Vector 0.50 or newer: the signed macOS package installs it at
/opt/beacon/bin/vector, Homebrew provides it as "vector", and Linux packages are
at https://vector.dev. Revoke a device from the dashboard's Beacon Endpoints
page; run "beacon endpoint disconnect" to stop forwarding locally.`,
	SilenceUsage: true,
	RunE:         runEndpointConnect,
}

var endpointDisconnectCmd = &cobra.Command{
	Use:   "disconnect",
	Short: "Stop forwarding to Asymptote and remove the local forwarder",
	Long: `Stop the Asymptote forwarder and remove its configuration and, unless
--keep-credentials is given, the stored device key and enrollment record.

This does not revoke the device on the server. Revoke it from the dashboard's
Beacon Endpoints page so the key can never be used again.`,
	SilenceUsage: true,
	RunE:         runEndpointDisconnect,
}

func init() {
	for _, c := range []*cobra.Command{endpointConnectCmd, endpointDisconnectCmd} {
		addEndpointPathFlags(c)
		c.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print output as JSON")
	}
	endpointConnectCmd.Flags().StringVar(&connectOpts.dashboardURL, "dashboard-url", "", "Beacon service URL (defaults to "+auth.DefaultDashboardURL+", or "+auth.DashboardURLEnv+")")
	endpointConnectCmd.Flags().BoolVar(&connectOpts.noBrowser, "no-browser", false, "System mode: print the device approval URL instead of opening a browser")
	endpointConnectCmd.Flags().StringVar(&connectOpts.vectorBin, "vector-bin", "", "Vector binary to run (defaults to "+asymptote.VectorBinEnv+", "+asymptote.PackagedVectorPath+", Homebrew, then PATH)")
	endpointConnectCmd.Flags().StringVar(&connectOpts.privacyMode, "privacy-mode", "", "Managed forwarding privacy: standard or metadata-only (defaults to onboarding choice)")
	endpointDisconnectCmd.Flags().BoolVar(&connectOpts.keepCredentials, "keep-credentials", false, "Keep the enrollment record and device key so a later connect can reuse this device")
	endpointCmd.AddCommand(endpointConnectCmd)
	endpointCmd.AddCommand(endpointDisconnectCmd)
}

func runEndpointConnect(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("connecting a system endpoint needs root: rerun with sudo, or pass --user for a per-user install")
	}
	return connectEndpoint(cmd, userMode, loadOrDefaultConfig().LogPath)
}

// connectEndpoint runs the enrollment and forwarder setup for the given mode and log
// path, printing the outcome. `endpoint install --connect` and the onboarding offer
// call it after a successful install; `endpoint connect` calls it directly.
func connectEndpoint(cmd *cobra.Command, userMode bool, logPath string) error {
	hostname, _ := os.Hostname()
	out := cmd.OutOrStdout()
	if endpointOpts.jsonOutput {
		out = cmd.ErrOrStderr()
	}
	privacyMode, err := selectedManagedPrivacyMode(userMode)
	if err != nil {
		return err
	}
	options := asymptote.ConnectOptions{
		UserMode:    userMode,
		LogPath:     logPath,
		VectorBin:   connectOpts.vectorBin,
		PrivacyMode: privacyMode,
		Out:         out,
		Enroll: asymptote.EnrollOptions{
			DashboardURL: connectOpts.dashboardURL,
			NoBrowser:    connectOpts.noBrowser,
			OpenBrowser:  connectBrowserOpener(userMode),
			Device: asymptote.DeviceInfo{
				Hostname:      hostname,
				OS:            runtime.GOOS,
				Arch:          runtime.GOARCH,
				BeaconVersion: version.GetVersion(),
			},
		},
	}
	if userMode {
		session, err := connectAccountLoad()
		if err != nil {
			return fmt.Errorf("Beacon account sign-in is required: run `beacon login` first: %w", err)
		}
		if session.Expired(time.Now()) {
			return errors.New("Beacon account session expired: run `beacon login` again")
		}
		if !hasAccountScope(session.Scopes, account.ScopeDeviceEnroll) {
			return fmt.Errorf("Beacon account session lacks %s: run `beacon login` again", account.ScopeDeviceEnroll)
		}
		requestedDashboard := connectOpts.dashboardURL
		if requestedDashboard == "" {
			requestedDashboard = os.Getenv(auth.DashboardURLEnv)
		}
		if requestedDashboard != "" && auth.NormalizeDashboardURL(requestedDashboard) != auth.NormalizeDashboardURL(session.BaseURL) {
			return errors.New("--dashboard-url must match the signed-in Beacon account service")
		}
		options.Enroll.DashboardURL = session.BaseURL
		options.AccountEnroll = &asymptote.AccountEnrollOptions{
			BaseURL:     session.BaseURL,
			AccessToken: session.AccessToken,
		}
	}
	result, err := connectManagedEndpoint(context.Background(), options)
	if err != nil {
		return err
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Fprintf(out, "Connected to Beacon Managed as device %s", result.Enrollment.DeviceID)
	if result.Enrollment.OrganizationName != "" {
		fmt.Fprintf(out, " for %s", result.Enrollment.OrganizationName)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Forwarder: %s (loaded=%t running=%t)\n", result.Forwarder, result.ForwarderState.Loaded, result.ForwarderState.Running)
	fmt.Fprintf(out, "Managed privacy: %s\n", managedprivacy.Label(result.Enrollment.PrivacyMode))
	fmt.Fprintf(out, "Vector config: %s\n", result.VectorConfig)
	fmt.Fprintf(out, "Device key: %s (never printed; the Vector forwarder reads it)\n", result.SecretsFile)
	fmt.Fprintf(out, "Events recorded from now on appear on %s/dashboard/telemetry. Revoke this device from %s/dashboard/endpoints.\n",
		result.Enrollment.DashboardURL, result.Enrollment.DashboardURL)
	return nil
}

func selectedManagedPrivacyMode(userMode bool) (string, error) {
	if connectOpts.privacyMode != "" {
		return managedprivacy.Normalize(connectOpts.privacyMode)
	}
	if enrollment, err := asymptote.LoadEnrollment(userMode); err == nil && enrollment.PrivacyMode != "" {
		return managedprivacy.Normalize(enrollment.PrivacyMode)
	}
	if userMode {
		profile := onboarding.Load()
		if profile.Onboarding.PrivacyMode != "" {
			return managedprivacy.Normalize(profile.Onboarding.PrivacyMode)
		}
	}
	return managedprivacy.Standard, nil
}

func hasAccountScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func runEndpointDisconnect(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("disconnecting a system endpoint needs root: rerun with sudo, or pass --user for a per-user install")
	}
	enrollment, loadErr := asymptote.LoadEnrollment(userMode)
	cfg := loadOrDefaultConfig()
	if err := asymptote.Disconnect(asymptote.DisconnectOptions{
		UserMode:        userMode,
		LogPath:         cfg.LogPath,
		KeepCredentials: connectOpts.keepCredentials,
	}); err != nil {
		return err
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"disconnected":     true,
			"kept_credentials": connectOpts.keepCredentials,
			"device_id":        deviceIDOrEmpty(enrollment, loadErr),
		})
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Asymptote forwarder stopped and removed.")
	if connectOpts.keepCredentials {
		fmt.Fprintf(out, "Enrollment record and device key kept under %s.\n", asymptote.Dir(userMode))
	} else {
		fmt.Fprintf(out, "Local enrollment record and device key removed from %s.\n", asymptote.Dir(userMode))
	}
	if loadErr == nil && enrollment != nil {
		fmt.Fprintf(out, "The device %s is still registered server-side; revoke it from %s/dashboard/endpoints.\n", enrollment.DeviceID, enrollment.DashboardURL)
	} else {
		fmt.Fprintln(out, "Revoke the device from the dashboard's Beacon Endpoints page if it is still registered.")
	}
	return nil
}

func deviceIDOrEmpty(e *asymptote.Enrollment, err error) string {
	if err != nil || e == nil {
		return ""
	}
	return e.DeviceID
}

// connectBrowserOpener returns the browser opener for enrollment. A system-mode connect runs
// as root, where opening a browser would start it as root (or fail under launchd); it opens
// as the active console user instead, which is the person sitting at the machine.
func connectBrowserOpener(userMode bool) func(string) error {
	if userMode || os.Geteuid() != 0 {
		return nil
	}
	info, ok, err := defaultActiveConsoleUser()
	if err != nil || !ok {
		return nil
	}
	return func(url string) error {
		var opener []string
		switch runtime.GOOS {
		case "darwin":
			opener = []string{"open", url}
		case "linux":
			opener = []string{"xdg-open", url}
		default:
			return auth.OpenBrowser(url)
		}
		args := append([]string{"-u", info.Username, "env", "HOME=" + info.HomeDir, "USER=" + info.Username, "LOGNAME=" + info.Username}, opener...)
		cmd := exec.Command("sudo", args...)
		cmd.Stdout, cmd.Stderr = nil, nil
		return cmd.Start()
	}
}

// managedIngestStatusLine renders the human status line for `beacon endpoint status`.
func managedIngestStatusLine(status asymptote.ManagedIngestStatus) string {
	if !status.Enabled {
		if status.Message != "" {
			return "Beacon Managed: not connected (" + status.Message + ")"
		}
		return "Beacon Managed: not connected (run `beacon endpoint connect`)"
	}
	var b strings.Builder
	b.WriteString("Beacon Managed: connected")
	if status.OrganizationName != "" {
		fmt.Fprintf(&b, " to %s", status.OrganizationName)
	}
	fmt.Fprintf(&b, " as device %s", status.DeviceID)
	fmt.Fprintf(&b, "; forwarder loaded=%t running=%t", status.Forwarder.Loaded, status.Forwarder.Running)
	if status.PrivacyMode != "" {
		fmt.Fprintf(&b, "; privacy %s", managedprivacy.Label(status.PrivacyMode))
	}
	switch status.Credential {
	case "valid":
		b.WriteString("; credential valid")
	case "revoked":
		b.WriteString("; credential revoked (run `beacon endpoint connect` to re-enroll)")
	case "unknown":
		b.WriteString("; credential unknown (" + status.CredentialMessage + ")")
	}
	if status.BufferBytes > 0 {
		fmt.Fprintf(&b, "; buffer %s", humanBytes(status.BufferBytes))
	}
	return b.String()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
