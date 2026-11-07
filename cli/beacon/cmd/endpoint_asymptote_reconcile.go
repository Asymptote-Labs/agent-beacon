package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// reconcileManagedForwarder is indirected so the command can be tested without a connected
// endpoint or a service manager.
var reconcileManagedForwarder = asymptote.ReconcileForwarder

// addAsymptoteReconcileCommand attaches `beacon endpoint asymptote reconcile`, which the macOS
// package postinstall runs (it installs with --no-start, so Install's own reconcile does not
// run there) and which operators can run by hand. Install and repair reconcile on their own.
//
// It always restarts a running forwarder, as the Sentinel reconcile does after an upgrade: the
// package has just installed a new Vector, and its install may have replaced the runtime log with
// a copy, which Vector only notices when it restarts.
func addAsymptoteReconcileCommand(group *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Internal: re-render a connected endpoint's Beacon Cloud forwarder config in place",
		Long: `Re-render vector.toml for a connected endpoint from the pack this version carries,
keeping the recorded ingest URL and privacy mode. Vector validates the new config
before it replaces the old one, and a running forwarder restarts, so it runs the
Vector the package installed and reads the current runtime log, which the upgrade may
have replaced. Nothing is enrolled, the device key is not touched, and no network
request is made.`,
		Hidden:       true,
		SilenceUsage: true,
		RunE:         runAsymptoteReconcile,
	}
	addEndpointPathFlags(cmd)
	cmd.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print output as JSON")
	group.AddCommand(cmd)
}

func runAsymptoteReconcile(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	if !userMode && !lifecycle.HasSystemPrivileges() {
		return fmt.Errorf("reconciling the system forwarder needs elevated privileges; %s", lifecycle.SystemPrivilegeHint())
	}
	logPath := endpointOpts.logPath
	if logPath == "" {
		logPath = loadOrDefaultConfig().LogPath
	}
	result, err := reconcileManagedForwarder(asymptote.ReconcileOptions{UserMode: userMode, LogPath: logPath, Restart: true})
	if endpointOpts.jsonOutput {
		if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil && err == nil {
			err = encodeErr
		}
		return err
	}
	if err != nil {
		return err
	}
	if result.Warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: "+result.Message())
		return nil
	}
	if message := result.Message(); message != "" {
		fmt.Fprintln(cmd.OutOrStdout(), message)
	}
	return nil
}

// reportManagedForwarder prints the install or repair reconcile outcome when there is something
// to say: an endpoint that is not connected hears nothing about Beacon Cloud.
func reportManagedForwarder(out, errOut io.Writer, result *asymptote.ReconcileResult) {
	if result == nil || result.Skipped != "" || (result.Unchanged && !result.Restarted && result.Warning == "") {
		return
	}
	if result.Warning != "" {
		fmt.Fprintln(errOut, "Warning: "+result.Message())
		return
	}
	fmt.Fprintln(out, result.Message())
}
