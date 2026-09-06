package cmd

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
)

// A detected Grok Bot must not become a medium-severity "telemetry missing" warning. Every other
// detected runtime with missing telemetry gets that warning because there is a local fix -- a
// hooks install, a repair, a Cowork setup command -- and the Action names it. Grok Bot has none:
// the app is a client to a Cursor-hosted computer, and its actions reach a collector only through
// Cursor's Team Settings. Warning with an empty Action would send an operator hunting for a local
// remedy that does not exist, so the check reports presence and stops.
func TestGrokBotDetectionIsInformationalNotAWarning(t *testing.T) {
	h := harness.DiscoverGrokBot()
	h.Detected = true

	check := harnessCheck(h, "", true)
	if check.Status != diagnostics.StatusOK {
		t.Fatalf("status = %q, want ok: there is no local fix to warn towards", check.Status)
	}
	if check.Severity != diagnostics.SeverityInfo {
		t.Fatalf("severity = %q, want info", check.Severity)
	}
	if check.Evidence != "cloud_export_only" {
		t.Fatalf("evidence = %q, want cloud_export_only", check.Evidence)
	}
	if check.Action != "" {
		t.Fatalf("action = %q, want none: the export is configured in Cursor Team Settings, not here", check.Action)
	}
	if check.Message != h.Message {
		t.Fatalf("message = %q, want the discovery message explaining the export path", check.Message)
	}
}

// An absent Grok Bot reads like every other absent runtime.
func TestGrokBotNotInstalledCheck(t *testing.T) {
	h := harness.DiscoverGrokBot()
	h.Detected = false
	check := harnessCheck(h, "", true)
	if check.Status != diagnostics.StatusOK || check.Evidence != "not_installed" {
		t.Fatalf("check = %#v, want the standard not-installed result", check)
	}
}

// The remedy table has to know the capability so a future caller does not fall through to a
// hooks-install command for a runtime that has no hooks.
func TestHarnessActionHasNoLocalCommandForGrokBot(t *testing.T) {
	if got := harnessAction(harness.DiscoverGrokBot(), true); got != "" {
		t.Fatalf("harnessAction = %q, want none", got)
	}
}
