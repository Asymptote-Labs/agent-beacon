package cmd

import (
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// TestLogPermissionRemediationMatchesThePlatformThatFailed guards advice, which is the part of
// doctor nobody tests and everybody follows.
//
// The check used to end in an unconditional `chmod 666`. That was right while only POSIX reached
// it. Windows reaches it now, where chmod does not exist and would not restore an ACL if it did --
// so the most consequential line doctor prints there, the remediation for a system-mode log that
// hooks cannot write, was a command that cannot work. A wrong fix is worse than none: the operator
// runs it, nothing changes, and the next thing they doubt is the diagnosis.
func TestLogPermissionRemediationMatchesThePlatformThatFailed(t *testing.T) {
	action := func(evidence string) string {
		return actionForCheck(diagnostics.Check{
			Name:     "runtime_log_permissions",
			Target:   `C:\ProgramData\Beacon\Endpoint\logs`,
			Evidence: evidence,
		}, lifecycle.RuntimeLogSource{})
	}

	// Every ACL evidence is Windows-only by construction, so none of them may produce POSIX advice.
	for _, evidence := range []string{
		"acl_missing_interactive_write",
		"acl_not_writable_by_user",
		"acl_unreadable",
	} {
		got := action(evidence)
		if got == "" {
			t.Fatalf("evidence %q produced no remediation at all", evidence)
		}
		if strings.Contains(got, "chmod") {
			t.Fatalf("evidence %q suggests %q; chmod does not exist on Windows and would not "+
				"restore an ACL if it did", evidence, got)
		}
	}

	// The system-mode grant is the one that decides whether an endpoint captures anything, so its
	// advice has to be the actual command, not a gesture at the problem.
	grant := action("acl_missing_interactive_write")
	if runtime.GOOS == "windows" {
		if !strings.Contains(grant, "icacls") || !strings.Contains(grant, "S-1-5-4") {
			t.Fatalf("the missing-grant remediation is %q; it should be the icacls command that "+
				"restores the grant, naming the well-known SID rather than a localized account name", grant)
		}
	} else if grant != "beacon endpoint doctor --fix" {
		// No such grant exists here, so the hint is empty and the advice falls back to the command
		// that does apply. Printing an icacls line on a Mac would be worse than saying nothing.
		t.Fatalf("off Windows the missing-grant remediation is %q, want the doctor --fix fallback", grant)
	}

	if runtime.GOOS != "windows" {
		if got := action("not_writable"); got != `chmod 666 C:\ProgramData\Beacon\Endpoint\logs` {
			t.Fatalf("the POSIX mode remediation changed: %q", got)
		}
	}

	if got := action("runtime_log_missing"); got != "beacon endpoint doctor --fix" {
		t.Fatalf("missing-log remediation = %q", got)
	}
}
