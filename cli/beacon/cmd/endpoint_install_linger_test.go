package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

func TestPrintLingerWarningOnlyWhenApplicableAndDisabled(t *testing.T) {
	for name, result := range map[string]lifecycle.InstallResult{
		"not applicable": {},
		"enabled":        {LingerApplicable: true, LingerEnabled: true, LingerDetail: "linger already enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			printLingerWarning(&stderr, result)
			if stderr.Len() != 0 {
				t.Fatalf("successful/non-systemd install should be quiet, got %q", stderr.String())
			}
		})
	}

	var stderr bytes.Buffer
	printLingerWarning(&stderr, lifecycle.InstallResult{
		LingerApplicable:  true,
		LingerDetail:      "administrator approval is required",
		LingerRemediation: "sudo loginctl enable-linger beacon-user",
	})
	got := stderr.String()
	for _, want := range []string{
		"collection will stop after logout",
		"administrator approval is required",
		"sudo loginctl enable-linger beacon-user",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning missing %q: %s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "pkttyagent") {
		t.Fatalf("authentication-agent noise leaked to CLI: %s", got)
	}
}
