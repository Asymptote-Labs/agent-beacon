package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBeaconIntroLinesUseInfinityMark(t *testing.T) {
	lines := beaconIntroLines()
	if len(lines) == 0 {
		t.Fatal("beaconIntroLines returned no lines")
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "∞") {
		t.Fatalf("beaconIntroLines() = %q, want infinity mark", got)
	}
	if !strings.Contains(got, "Beacon CLI") {
		t.Fatalf("beaconIntroLines() = %q, want Beacon CLI label", got)
	}
}

func TestPrintBeaconIntroIncludesStartingCommands(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	printBeaconIntro(cmd)

	got := out.String()
	for _, want := range []string{
		"∞",
		"Beacon CLI",
		"beacon endpoint install",
		"beacon endpoint status",
		"beacon endpoint wazuh print-config",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("printBeaconIntro output missing %q:\n%s", want, got)
		}
	}
}

func TestShouldAnimateBeaconIntroFalseForNonFileWriter(t *testing.T) {
	var out bytes.Buffer
	if shouldAnimateBeaconIntro(&out) {
		t.Fatal("shouldAnimateBeaconIntro returned true for non-file writer")
	}
}
