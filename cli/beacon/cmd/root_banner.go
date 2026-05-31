package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

const beaconIntroDelay = 45 * time.Millisecond

var sleepBeaconIntro = time.Sleep

func printBeaconIntro(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	lines := beaconIntroLines()
	if shouldAnimateBeaconIntro(out) {
		for _, line := range lines {
			fmt.Fprintln(out, line)
			sleepBeaconIntro(beaconIntroDelay)
		}
	} else {
		for _, line := range lines {
			fmt.Fprintln(out, line)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Start with:")
	fmt.Fprintln(out, "  beacon endpoint install")
	fmt.Fprintln(out, "  beacon endpoint status")
	fmt.Fprintln(out, "  beacon endpoint wazuh print-config")
	fmt.Fprintln(out)
}

func beaconIntroLines() []string {
	return []string{
		fmt.Sprintf("       ∞∞∞     ∞∞∞      Beacon CLI %s", version.GetVersion()),
		"     ∞∞  ∞∞ ∞∞  ∞∞      Local AI runtime telemetry",
		"    ∞∞     ∞     ∞∞     Endpoint visibility for agentic tools",
		fmt.Sprintf("     ∞∞  ∞∞ ∞∞  ∞∞      %s", compactWorkingDir()),
		"       ∞∞∞     ∞∞∞",
	}
}

func compactWorkingDir() string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		return "beacon endpoint"
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if rel, err := filepath.Rel(home, wd); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
		if wd == home {
			return "~"
		}
	}
	return wd
}

func shouldAnimateBeaconIntro(out io.Writer) bool {
	if os.Getenv("BEACON_NO_ANIMATION") != "" || os.Getenv("CI") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
