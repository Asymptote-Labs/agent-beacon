package cmd

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// `beacon endpoint claude-cowork print-config` is what an admin copies into Claude's monitoring
// settings, so every value passed on the command line has to reach the output: a dropped
// --headers leaves the export unauthenticated and the collector silently receives nothing.
func TestCoworkPrintConfigEmitsTheFlagValues(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })

	cmd := endpointCoworkPrintConfigCmd
	args := []string{
		"--endpoint", "https://collector.example.com",
		"--headers", "Authorization=Bearer token",
		"--resource-attributes", "deployment.environment=prod,service.name=claude-cowork",
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	out, err := captureStdout(t, func() error {
		cmd.Run(cmd, nil)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{args[1], args[3], args[5]} {
		if !strings.Contains(out, want) {
			t.Errorf("print-config output missing %q:\n%s", want, out)
		}
	}
}
