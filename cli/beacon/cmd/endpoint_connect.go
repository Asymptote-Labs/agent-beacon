package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var connectOpts struct {
	dashboardURL    string
	noBrowser       bool
	vectorBin       string
	keepCredentials bool
}

var endpointConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Forward this endpoint's telemetry to Asymptote managed ingest",
	Long: `Connect this endpoint to Asymptote managed ingest.

Opens the Asymptote dashboard so a member of your organization can approve this
device, then stores the per-device key it receives in a private secrets file
and starts a Vector forwarder that ships the runtime and inventory JSONL to
Asymptote. Nothing recorded before approval is sent. Re-running on a connected
machine rotates its key in place.

Requires Vector 0.56 or newer: the signed macOS package installs it at
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
	endpointConnectCmd.Flags().StringVar(&connectOpts.dashboardURL, "dashboard-url", "", "Asymptote dashboard URL (defaults to "+auth.DefaultDashboardURL+", or "+auth.DashboardURLEnv+")")
	endpointConnectCmd.Flags().BoolVar(&connectOpts.noBrowser, "no-browser", false, "Print the approval URL instead of opening a browser")
	endpointConnectCmd.Flags().StringVar(&connectOpts.vectorBin, "vector-bin", "", "Vector binary to run (defaults to "+asymptote.VectorBinEnv+", "+asymptote.PackagedVectorPath+", Homebrew, then PATH)")
	endpointDisconnectCmd.Flags().BoolVar(&connectOpts.keepCredentials, "keep-credentials", false, "Keep the enrollment record and device key so a later connect can reuse this device")
	endpointCmd.AddCommand(endpointConnectCmd)
	endpointCmd.AddCommand(endpointDisconnectCmd)
}

func runEndpointConnect(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("connecting a system endpoint needs root: rerun with sudo, or pass --user for a per-user install")
	}
	cfg := loadOrDefaultConfig()
	hostname, _ := os.Hostname()
	out := cmd.OutOrStdout()
	if endpointOpts.jsonOutput {
		out = cmd.ErrOrStderr()
	}
	result, err := asymptote.Connect(context.Background(), asymptote.ConnectOptions{
		UserMode:  userMode,
		LogPath:   cfg.LogPath,
		VectorBin: connectOpts.vectorBin,
		Out:       out,
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
	})
	if err != nil {
		return err
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Fprintf(out, "Connected to Asymptote as device %s", result.Enrollment.DeviceID)
	if result.Enrollment.OrganizationName != "" {
		fmt.Fprintf(out, " for %s", result.Enrollment.OrganizationName)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Forwarder: %s (loaded=%t running=%t)\n", result.Forwarder, result.ForwarderState.Loaded, result.ForwarderState.Running)
	fmt.Fprintf(out, "Vector config: %s\n", result.VectorConfig)
	fmt.Fprintf(out, "Device key: %s (never printed; the Vector forwarder reads it)\n", result.SecretsFile)
	fmt.Fprintf(out, "Events recorded from now on appear on %s/dashboard/telemetry. Revoke this device from %s/dashboard/endpoints.\n",
		result.Enrollment.DashboardURL, result.Enrollment.DashboardURL)
	return nil
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
			return "Asymptote managed ingest: not connected (" + status.Message + ")"
		}
		return "Asymptote managed ingest: not connected (run `beacon endpoint connect`)"
	}
	var b strings.Builder
	b.WriteString("Asymptote managed ingest: connected")
	if status.OrganizationName != "" {
		fmt.Fprintf(&b, " to %s", status.OrganizationName)
	}
	fmt.Fprintf(&b, " as device %s", status.DeviceID)
	fmt.Fprintf(&b, "; forwarder loaded=%t running=%t", status.Forwarder.Loaded, status.Forwarder.Running)
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

// managedIngestFromConfig reports whether config.json says this endpoint is connected, for
// callers that must not touch the network or the secrets file.
func managedIngestFromConfig(cfg endpointconfig.Config) bool {
	return cfg.ManagedIngest != nil && cfg.ManagedIngest.Enabled
}
