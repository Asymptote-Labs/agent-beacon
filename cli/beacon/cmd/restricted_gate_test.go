package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func withRestrictedGate(t *testing.T, restricted bool, euid int) {
	t.Helper()
	prevLoad, prevEUID := restrictedGateLoad, restrictedGateEUID
	restrictedGateLoad = func() (asymptoteobserve.RestrictedLog, bool) { return asymptoteobserve.RestrictedLog{}, restricted }
	restrictedGateEUID = func() int { return euid }
	t.Cleanup(func() { restrictedGateLoad, restrictedGateEUID = prevLoad, prevEUID })
}

func findCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := rootCmd.Find(args)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	return cmd
}

func TestRestrictedModeRefusesOrdinaryUsers(t *testing.T) {
	withRestrictedGate(t, true, 501)
	for _, args := range [][]string{
		{"endpoint", "uninstall"},
		{"endpoint", "status"},
		{"endpoint", "disconnect"},
		{"endpoint", "hooks", "uninstall"},
		{"endpoint", "dashboard"},
		{"scan"},
	} {
		err := requireRootInRestrictedMode(findCommand(t, args...), nil)
		if err == nil || !strings.Contains(err.Error(), "sudo beacon "+strings.Join(args, " ")) {
			t.Errorf("%v: error = %v, want a refusal naming sudo", args, err)
		}
	}
	for _, args := range [][]string{{}, {"version"}, {"completion"}} {
		if err := requireRootInRestrictedMode(findCommand(t, args...), nil); err != nil {
			t.Errorf("%v was refused: %v", args, err)
		}
	}
}

func TestRestrictedModeAllowsRoot(t *testing.T) {
	withRestrictedGate(t, true, 0)
	if err := requireRootInRestrictedMode(findCommand(t, "endpoint", "uninstall"), nil); err != nil {
		t.Fatalf("root was refused: %v", err)
	}
}

func TestSharedModeChangesNothing(t *testing.T) {
	withRestrictedGate(t, false, 501)
	if err := requireRootInRestrictedMode(findCommand(t, "endpoint", "uninstall"), nil); err != nil {
		t.Fatalf("an endpoint not in restricted mode refused a command: %v", err)
	}
}
