package cmd

import (
	"bytes"
	"os"
	"path/filepath"
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

// isTerminal gates the full-screen onboarding wizard and the trace browser, so
// anything that is not a real terminal has to read as false.
//
// This previously tested os.ModeCharDevice, which /dev/null also sets. The result
// was that `beacon endpoint install < /dev/null` -- an ordinary shape for a wrapper
// script or an unattended run -- looked interactive, started the wizard against an
// input already at EOF, and failed the install with "onboarding cancelled".
func TestIsTerminalRejectsDevNullPipesAndRegularFiles(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()

	regular, err := os.Create(filepath.Join(t.TempDir(), "redirected.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()

	for name, file := range map[string]*os.File{
		"/dev/null":    devNull,
		"pipe read":    readEnd,
		"pipe write":   writeEnd,
		"regular file": regular,
		"nil":          nil,
	} {
		if isTerminal(file) {
			t.Fatalf("isTerminal(%s) = true, want false", name)
		}
	}
}
