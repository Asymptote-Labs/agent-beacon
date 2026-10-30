package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// The reported Linux install printed an unbroken run of success lines while the collector had no
// logout persistence, so the user found out at their next logout with collection already stopped.
// Install and repair now warn -- and stay quiet when there is no gap, because a logout warning on a
// backend that has no such concept is its own bug.
func TestPrintLingerGapSaysNothingWhenThereIsNoGap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result lifecycle.InstallResult
	}{
		{
			// launchd, system mode, the supervised backend, and any --no-start install: linger
			// is not a question, so there is nothing to warn about.
			name:   "not applicable",
			result: lifecycle.InstallResult{},
		},
		{
			// A detail is recorded on the success path too. It belongs in the manifest, not in
			// a warning that has nothing to warn about.
			name:   "enabled during this install",
			result: lifecycle.InstallResult{LingerApplicable: true, LingerEnabled: true, LingerDetail: "linger enabled for someone"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printLingerGap(&out, tc.result)
			if out.Len() != 0 {
				t.Errorf("printLingerGap wrote %q, want silence", out.String())
			}
		})
	}
}

// The gap the feedback was about: the warning carries the recorded reason and the exact fix.
func TestPrintLingerGapNamesTheGapAndTheFix(t *testing.T) {
	var out bytes.Buffer
	printLingerGap(&out, lifecycle.InstallResult{
		LingerApplicable:  true,
		LingerDetail:      "linger is disabled for someone; enabling it needs administrator approval",
		LingerRemediation: "sudo loginctl enable-linger someone",
	})
	got := out.String()
	for _, want := range []string{"administrator approval", "sudo loginctl enable-linger someone"} {
		if !strings.Contains(got, want) {
			t.Errorf("output should mention %q, got:\n%s", want, got)
		}
	}
}

// The unverifiable case: os/user could not name the current user, so the linger state was never
// read. Printing a remediation would name a user this code could not resolve.
func TestPrintLingerGapOffersNoFixForAnUnverifiedState(t *testing.T) {
	var out bytes.Buffer
	printLingerGap(&out, lifecycle.InstallResult{
		LingerApplicable: true,
		LingerDetail:     "could not determine the current user, so logout persistence is unverified",
	})
	got := out.String()

	if strings.Contains(got, "loginctl") {
		t.Errorf("no fix should be offered for a user this code could not resolve, got:\n%s", got)
	}
	if !strings.Contains(got, "unverified") {
		t.Errorf("output should carry the recorded detail, got:\n%s", got)
	}
}
