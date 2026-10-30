package cmd

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// A HEC token readable by every user is fixed in place by doctor --fix, and the advice names the
// elevated Windows command rather than sudo, since only Windows reports it.
func TestSecretFilesPrivateHasAFixAndWindowsAdvice(t *testing.T) {
	check := diagnostics.Check{Name: "secret_files_private", Target: `C:\ProgramData\Beacon\Endpoint`,
		Status: diagnostics.StatusFail, Evidence: "secret_readable_by_users"}
	if got := actionForCheck(check, lifecycle.RuntimeLogSource{}); strings.Contains(got, "sudo") || !strings.Contains(got, "doctor --system --fix") {
		t.Fatalf("remediation = %q, want the elevated doctor --system --fix", got)
	}
	plan := planDoctorFixes(doctorResult{Checks: []diagnostics.Check{check}}, lifecycle.Status{})
	if len(plan.Fixes) != 1 || plan.Fixes[0].Action != "restrict_secret_files" {
		t.Fatalf("fix plan = %#v, want restrict_secret_files", plan)
	}
	check.Status, check.Evidence = diagnostics.StatusWarn, "acl_unreadable"
	if plan := planDoctorFixes(doctorResult{Checks: []diagnostics.Check{check}}, lifecycle.Status{}); len(plan.Fixes) != 0 {
		t.Fatalf("an unreadable ACL planned a fix: %#v", plan)
	}
}
