package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel"
)

// Seams so the commands can be tested without a service manager, Vector or root.
var (
	installSentinelForwarder   = sentinel.Install
	removeSentinelForwarder    = sentinel.Remove
	reconcileSentinelForwarder = sentinel.Reconcile
	readSentinelForwarder      = sentinel.ReadStatus
	sentinelHasPrivileges      = lifecycle.HasSystemPrivileges
)

var sentinelForwarderOpts struct {
	endpoint      string
	dcr           string
	tenant        string
	client        string
	certificate   string
	authority     string
	readFrom      string
	vectorBin     string
	secretStdin   bool
	passwordStdin bool
}

// newSentinelForwarderCommand is `beacon endpoint sentinel forwarder`: Vector running the Logs
// Ingestion config as a system service, managed by Beacon.
func newSentinelForwarderCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "forwarder",
		Short: "Run Vector as a system service that sends the runtime log to Microsoft Sentinel",
		Long: `Run the Vector bundled with Beacon as a system service that sends every runtime log line
to Microsoft Sentinel through the Azure Monitor Logs Ingestion API, using a Microsoft Entra
app registration. The service survives reboots, repair and package upgrades, and uninstall
removes it. System installs only.`,
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "Set up the Sentinel forwarder, or replace its settings",
		Long: `Set up the Sentinel forwarder, or replace the settings of one already set up.

The credential never goes on the command line. Give a client secret in AZURE_CLIENT_SECRET
(or on stdin with --client-secret-stdin), or a certificate with --client-certificate and its
password, if it has one, in AZURE_CLIENT_CERTIFICATE_PASSWORD (or --certificate-password-stdin).
Both are stored where only root, or SYSTEM and Administrators on Windows, can read them.

Vector checks the config before anything changes, so a rejected install leaves a working
forwarder as it was.`,
		Example: `  printf '%s' "$CLIENT_SECRET" | sudo beacon endpoint sentinel forwarder install --system \
    --endpoint https://beacon-dcr-abcd.eastus-1.ingest.monitor.azure.com \
    --dcr-immutable-id dcr-0123456789abcdef0123456789abcdef \
    --tenant-id <tenant GUID> --client-id <app GUID> --client-secret-stdin

  sudo beacon endpoint sentinel forwarder install --system --client-certificate app.pfx \
    --endpoint ... --dcr-immutable-id ... --tenant-id ... --client-id ...`,
		SilenceUsage: true,
		RunE:         runSentinelForwarderInstall,
	}
	addEndpointPathFlags(install)
	flags := install.Flags()
	flags.StringVar(&sentinelForwarderOpts.endpoint, "endpoint", "", "The DCR's logs ingestion URL (or a Data Collection Endpoint's)")
	flags.StringVar(&sentinelForwarderOpts.dcr, "dcr-immutable-id", "", "The DCR's immutable ID (dcr-...)")
	flags.StringVar(&sentinelForwarderOpts.tenant, "tenant-id", "", "Microsoft Entra tenant ID")
	flags.StringVar(&sentinelForwarderOpts.client, "client-id", "", "App registration (client) ID")
	flags.BoolVar(&sentinelForwarderOpts.secretStdin, "client-secret-stdin", false, "Read the client secret from stdin instead of AZURE_CLIENT_SECRET")
	flags.StringVar(&sentinelForwarderOpts.certificate, "client-certificate", "", "Authenticate with this PFX certificate instead of a client secret")
	flags.BoolVar(&sentinelForwarderOpts.passwordStdin, "certificate-password-stdin", false, "Read the certificate password from stdin instead of AZURE_CLIENT_CERTIFICATE_PASSWORD")
	flags.StringVar(&sentinelForwarderOpts.authority, "authority-host", "", "Entra authority for a sovereign cloud, such as https://login.microsoftonline.us")
	flags.StringVar(&sentinelForwarderOpts.readFrom, "read-from", "beginning", "Where to start in a log not read before: beginning (send the current log once) or end")
	flags.StringVar(&sentinelForwarderOpts.vectorBin, "vector", "", "Vector binary to run (default: the one Beacon's package installed)")
	flags.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print output as JSON")
	for _, name := range []string{"endpoint", "dcr-immutable-id", "tenant-id", "client-id"} {
		_ = install.MarkFlagRequired(name)
	}
	install.MarkFlagsMutuallyExclusive("client-secret-stdin", "client-certificate")

	status := &cobra.Command{
		Use:          "status",
		Short:        "Show the Sentinel forwarder's settings and service state",
		SilenceUsage: true,
		RunE:         runSentinelForwarderStatus,
	}
	addEndpointPathFlags(status)
	status.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print output as JSON")

	remove := &cobra.Command{
		Use:          "remove",
		Short:        "Stop the Sentinel forwarder and delete its settings and credential",
		SilenceUsage: true,
		RunE:         runSentinelForwarderRemove,
	}
	addEndpointPathFlags(remove)

	reconcile := &cobra.Command{
		Use:   "reconcile",
		Short: "Internal: bring the Sentinel forwarder's config and service up to date",
		Long: `Re-render the Sentinel forwarder's config from its recorded settings and the pack this
version carries, have Vector validate it, and restart the service. Install and repair do
this on their own; the macOS package runs it after an upgrade.`,
		Hidden:       true,
		SilenceUsage: true,
		RunE:         runSentinelForwarderReconcile,
	}
	addEndpointPathFlags(reconcile)

	group.AddCommand(install, status, remove, reconcile)
	return group
}

// requireSystemForwarder refuses user mode and an unprivileged system run.
func requireSystemForwarder(action string) error {
	if endpointUserMode() {
		return errors.New("the Sentinel forwarder runs only with a system install; pass --system")
	}
	if !sentinelHasPrivileges() {
		return fmt.Errorf("%s the Sentinel forwarder needs elevated privileges; %s", action, lifecycle.SystemPrivilegeHint())
	}
	return nil
}

func runSentinelForwarderInstall(cmd *cobra.Command, args []string) error {
	if err := requireSystemForwarder("installing"); err != nil {
		return err
	}
	opts := sentinel.InstallOptions{
		Endpoint:            sentinelForwarderOpts.endpoint,
		DCRImmutableID:      sentinelForwarderOpts.dcr,
		TenantID:            sentinelForwarderOpts.tenant,
		ClientID:            sentinelForwarderOpts.client,
		CertificatePath:     sentinelForwarderOpts.certificate,
		AuthorityHost:       sentinelForwarderOpts.authority,
		ReadFrom:            sentinelForwarderOpts.readFrom,
		LogPath:             loadOrDefaultConfig().LogPath,
		VectorBin:           sentinelForwarderOpts.vectorBin,
		ClientSecret:        os.Getenv("AZURE_CLIENT_SECRET"),
		CertificatePassword: os.Getenv("AZURE_CLIENT_CERTIFICATE_PASSWORD"),
	}
	if sentinelForwarderOpts.secretStdin || sentinelForwarderOpts.passwordStdin {
		value, err := readStdinSecret(cmd.InOrStdin())
		if err != nil {
			return err
		}
		if sentinelForwarderOpts.secretStdin {
			opts.ClientSecret = value
		} else {
			opts.CertificatePassword = value
		}
	}
	if opts.CertificatePath != "" {
		// A secret left in the environment is not a reason to refuse a certificate install.
		opts.ClientSecret = ""
	}
	result, err := installSentinelForwarder(opts)
	if err != nil {
		return err
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Sentinel forwarder installed.")
	fmt.Fprintf(out, "  Service: %s (running=%t)\n", result.Service.Label, result.Service.Running)
	fmt.Fprintf(out, "  Config:  %s\n", result.Config)
	fmt.Fprintf(out, "  Vector:  %s\n", result.Vector)
	fmt.Fprintln(out, "Check delivery: write a validation event with `beacon endpoint sentinel validate --system`, then run in Log Analytics:")
	fmt.Fprintln(out, `  BeaconRuntime_CL | where Message has "Beacon endpoint Sentinel validation event"`)
	return nil
}

// readStdinSecret reads one value from stdin, without its trailing newline.
func readStdinSecret(in io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(in, 64*1024))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", errors.New("nothing on stdin")
	}
	return value, nil
}

func runSentinelForwarderStatus(cmd *cobra.Command, args []string) error {
	if endpointUserMode() {
		return errors.New("the Sentinel forwarder runs only with a system install; pass --system")
	}
	status := readSentinelForwarder()
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}
	out := cmd.OutOrStdout()
	if !status.Installed {
		fmt.Fprintln(out, "Sentinel forwarder: not installed (or this account cannot read its settings; "+lifecycle.SystemPrivilegeHint()+")")
		return nil
	}
	fmt.Fprintln(out, sentinelForwarderStatusLine(status))
	fmt.Fprintf(out, "  Endpoint:   %s\n", status.Settings.Endpoint)
	fmt.Fprintf(out, "  DCR:        %s\n", status.Settings.DCRImmutableID)
	fmt.Fprintf(out, "  Credential: %s (app %s)\n", status.Settings.Credential, status.Settings.ClientID)
	fmt.Fprintf(out, "  Log:        %s\n", status.Settings.LogPath)
	return nil
}

// sentinelForwarderStatusLine is the one line `beacon endpoint status` prints for it.
func sentinelForwarderStatusLine(status sentinel.Status) string {
	line := fmt.Sprintf("Sentinel forwarder: loaded=%t running=%t", status.Service.Loaded, status.Service.Running)
	if !status.Service.Loaded && status.Service.Message != "" {
		line += " (" + status.Service.Message + ")"
	}
	if status.LastProblem != "" {
		line += "; Vector last reported: " + status.LastProblem
	}
	return line
}

func runSentinelForwarderRemove(cmd *cobra.Command, args []string) error {
	if err := requireSystemForwarder("removing"); err != nil {
		return err
	}
	if err := removeSentinelForwarder(false); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Sentinel forwarder removed, with its settings and credential.")
	return nil
}

func runSentinelForwarderReconcile(cmd *cobra.Command, args []string) error {
	if err := requireSystemForwarder("reconciling"); err != nil {
		return err
	}
	result, err := reconcileSentinelForwarder(sentinel.ReconcileOptions{LogPath: loadOrDefaultConfig().LogPath, Restart: true})
	if err != nil {
		return err
	}
	if result.Skipped == "" {
		fmt.Fprintln(cmd.OutOrStdout(), result.Message())
	}
	return nil
}

// reportSentinelForwarder prints the install or repair reconcile outcome when one is set up.
func reportSentinelForwarder(out, errOut io.Writer, result *sentinel.ReconcileResult) {
	if result == nil || result.Skipped != "" {
		return
	}
	if result.Warning != "" {
		fmt.Fprintln(errOut, "Warning: "+result.Message())
		return
	}
	fmt.Fprintln(out, result.Message())
}
