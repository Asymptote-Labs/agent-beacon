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

const logAccessUsage = "System log access: shared (hooks append to a log every account can write; the default) or restricted " +
	"(only the privileged collector writes it, hooks send events over a local socket or, on Windows, a named pipe, and only " +
	"root -- SYSTEM and Administrators on Windows -- and the " + asymptoteobserve.RuntimeLogReadersGroup +
	" group can read it). Empty keeps the current mode. System installs only"

// reportLogAccess says what the system log's access mode means for the people who read it. A
// restricted log is unreadable to an existing log shipper that does not run as root until its
// account joins the readers group, which nothing else on the screen would tell them.
func reportLogAccess(w io.Writer, result lifecycle.InstallResult) {
	switch result.LogAccess {
	case asymptoteobserve.LogAccessRestricted:
		if runtime.GOOS == "windows" {
			fmt.Fprintf(w, "Runtime log access: restricted (SYSTEM, Administrators and the %s group only; hooks send events to %s)\n", loggroup.Name, localingest.DefaultSocketPath)
		} else {
			fmt.Fprintf(w, "Runtime log access: restricted (0640 root:%s; hooks send events to %s)\n", loggroup.Name, localingest.DefaultSocketPath)
		}
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
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("net localgroup %s %s /add (from an elevated shell)", loggroup.Name, account)
	}
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("sudo dseditgroup -o edit -a %s -t user %s", account, loggroup.Name)
	}
	return fmt.Sprintf("sudo usermod -aG %s %s", loggroup.Name, account)
}

// elevatedRunHint says how to rerun a command with the privilege a restricted log needs.
func elevatedRunHint() string {
	if runtime.GOOS == "windows" {
		return "from an elevated shell"
	}
	return "with sudo"
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
