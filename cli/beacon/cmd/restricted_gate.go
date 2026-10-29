package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// On a machine in restricted log mode (install --system --restricted-log) every beacon command
// except the harmless informational ones needs root. Ordinary users can neither read the runtime
// log nor change the endpoint, and refusing up front gives them one clear answer instead of a
// different permission error from each command.
//
// This is a guard on this binary, not the security boundary: the boundary is that the log, the
// system config and the service definitions are root's, which no copy of the CLI changes.

// restrictedModeAllowed are the command paths an ordinary user may still run, with everything
// under them. "beacon" alone is the splash screen.
var restrictedModeAllowed = []string{
	"beacon",
	"beacon help",
	"beacon version",
	"beacon completion",
	"beacon __complete",
	"beacon __completeNoDesc",
}

// Seams for tests.
var (
	restrictedGateEUID = os.Geteuid
	restrictedGateLoad = asymptoteobserve.LoadRestrictedLog
)

func requireRootInRestrictedMode(cmd *cobra.Command, _ []string) error {
	if _, restricted := restrictedGateLoad(); !restricted {
		return nil
	}
	if restrictedGateEUID() == 0 {
		return nil
	}
	path := cmd.CommandPath()
	for _, allowed := range restrictedModeAllowed {
		if path == allowed || (allowed != "beacon" && strings.HasPrefix(path, allowed+" ")) {
			return nil
		}
	}
	return fmt.Errorf("this machine's Beacon endpoint is in restricted mode, so `%s` must be run as root: sudo %s", path, path)
}

func init() {
	rootCmd.PersistentPreRunE = requireRootInRestrictedMode
}
