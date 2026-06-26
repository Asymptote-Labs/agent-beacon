package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPrintRootSplash(t *testing.T) {
	cmd := &cobra.Command{Use: "beacon"}
	var out bytes.Buffer
	cmd.SetOut(&out)

	printRootSplash(cmd)

	got := out.String()
	for _, want := range []string{
		"██████╗ ███████╗ █████╗  ██████╗ ██████╗ ███╗   ██╗",
		"Open-source telemetry layer for AI agents.",
		"Start with:",
		"beacon endpoint install",
		"beacon endpoint status",
		"beacon endpoint wazuh print-config",
		"Usage:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("root splash missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("root splash written to a buffer should not include ANSI escapes:\n%q", got)
	}
	for _, line := range beaconBanner {
		if len([]rune(line)) > 64 {
			t.Fatalf("banner line %q is too wide: got %d columns", line, len(line))
		}
	}
}
