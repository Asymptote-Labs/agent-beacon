package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// The reported Linux install printed an unbroken run of success lines while the collector had no
// logout persistence, so the user found out at their next logout with collection already stopped.
// These pin what install and repair now say instead -- and, just as importantly, when they stay
// quiet, because a logout warning on a backend that has no such concept is its own bug.
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
			// Applicable and satisfied. The install is fully persistent; saying so again here
			// would just be noise in a transcript that already reported success.
			name:   "already enabled",
			result: lifecycle.InstallResult{LingerApplicable: true, LingerEnabled: true, LingerDetail: "linger already enabled for someone"},
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

// The gap the feedback was about: running now, not running after logout. The user should not have
// to know what linger is, or go find the command themselves.
func TestPrintLingerGapNamesTheGapAndTheFix(t *testing.T) {
	var out bytes.Buffer
	printLingerGap(&out, lifecycle.InstallResult{
		LingerApplicable:  true,
		LingerDetail:      "linger is disabled for someone; enabling it needs administrator approval",
		LingerRemediation: "sudo loginctl enable-linger someone",
	})
	got := out.String()

	for _, want := range []string{
		// What is wrong, in terms of what the user loses rather than which flag is unset.
		"logs out",
		// Why, kept verbatim from the outcome so the reason is never paraphrased away.
		"administrator approval",
		// The exact command, copy-pasteable, no placeholder left to substitute.
		"sudo loginctl enable-linger someone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output should mention %q, got:\n%s", want, got)
		}
	}
	// It is a warning about a degraded install, not a failure: the collector is running.
	if !strings.Contains(got, "collector is running") {
		t.Errorf("output should say the collector is running, got:\n%s", got)
	}
}

// The unverifiable case: os/user could not name the current user, so the linger state was never
// read. Claiming collection will stop would be asserting something unknown, and printing a
// remediation would name a user this code could not resolve.
func TestPrintLingerGapReportsAnUnverifiedStateAsUnverified(t *testing.T) {
	var out bytes.Buffer
	printLingerGap(&out, lifecycle.InstallResult{
		LingerApplicable: true,
		LingerDetail:     "could not determine the current user, so logout persistence is unverified",
	})
	got := out.String()

	if !strings.Contains(got, "could not verify") {
		t.Errorf("output should report the state as unverified, got:\n%s", got)
	}
	if strings.Contains(got, "will stop") {
		t.Errorf("an unverified state must not be asserted as broken, got:\n%s", got)
	}
	if strings.Contains(got, "loginctl") {
		t.Errorf("no fix should be offered for a user this code could not resolve, got:\n%s", got)
	}
	if !strings.Contains(got, "unverified") {
		t.Errorf("output should carry the recorded detail, got:\n%s", got)
	}
}
