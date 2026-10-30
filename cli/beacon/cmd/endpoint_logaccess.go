package cmd

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/loggroup"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

const logAccessUsage = "System log access: shared (hooks append to a world-writable log; the default) or restricted " +
	"(only the root collector writes it, hooks send events over a local socket, and only root and the " +
	asymptoteobserve.RuntimeLogReadersGroup + " group can read it). Empty keeps the current mode. System installs on macOS and Linux only"

// reportLogAccess says what the system log's access mode means for the people who read it. A
// restricted log is unreadable to an existing log shipper that does not run as root until its
// account joins the readers group, which nothing else on the screen would tell them.
func reportLogAccess(w io.Writer, result lifecycle.InstallResult) {
	switch result.LogAccess {
	case asymptoteobserve.LogAccessRestricted:
		fmt.Fprintf(w, "Runtime log access: restricted (0640 root:%s; hooks send events to %s)\n", loggroup.Name, localingest.DefaultSocketPath)
		if result.LogReadersGroupCreated {
			fmt.Fprintf(w, "Created the %s group. Add each account that should read the log, such as a log shipper's: %s\n", loggroup.Name, addToGroupHint("<account>"))
		} else {
			fmt.Fprintf(w, "Accounts that read the log need to be in the %s group: %s\n", loggroup.Name, addToGroupHint("<account>"))
		}
	case asymptoteobserve.LogAccessShared:
		fmt.Fprintln(w, "Runtime log access: shared (any local account can append to and read the log; --log-access restricted narrows it)")
	}
}

func addToGroupHint(account string) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("sudo dseditgroup -o edit -a %s -t user %s", account, loggroup.Name)
	}
	return fmt.Sprintf("sudo usermod -aG %s %s", loggroup.Name, account)
}

// readable reports whether this account can open path for reading.
func readable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
