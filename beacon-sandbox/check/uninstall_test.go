package check

import (
	"strings"
	"testing"
)

// clean is the shape of a removal that did everything right: service gone, data kept, Beacon's own
// status agreeing.
func clean() Removal {
	return Removal{
		Ran:          "true",
		ExitCode:     "0",
		Output:       "Endpoint uninstalled",
		ServiceKind:  "systemd",
		ServiceLabel: "beacon-collector.service",
		ServiceGone:  "true",
		LogRetained:  "true",
		Status:       `{"service":{"running":false}}`,
	}
}

func findingsFor(r Removal) []Finding {
	var v Verdict
	Uninstall(&v, r)
	return v.Findings
}

func has(findings []Finding, name string, sev Severity) bool {
	for _, f := range findings {
		if f.Check == name && f.Severity == sev {
			return true
		}
	}
	return false
}

func TestACleanRemovalRaisesNoFailures(t *testing.T) {
	for _, f := range findingsFor(clean()) {
		if f.Severity == SevFail {
			t.Fatalf("clean removal produced a failure: %s — %s", f.Check, f.Summary)
		}
	}
}

// TestAScenarioThatDidNotUninstallIsSilent is what keeps this check honest about its own absence.
//
// Most scenarios do not uninstall. Emitting a passing finding for them would report a check that
// never ran as one that succeeded, which is the failure mode this package is built to avoid.
func TestAScenarioThatDidNotUninstallIsSilent(t *testing.T) {
	if findings := findingsFor(Removal{}); len(findings) != 0 {
		t.Fatalf("a run with no uninstall produced %d finding(s): %#v", len(findings), findings)
	}
}

// TestAServiceLeftRegisteredFails is the bug this check exists for.
//
// It shipped once: uninstall reported success while the Windows service stayed registered with
// automatic start, so the collector returned at the next reboot after the operator was told it was
// gone. A reviewer reading a diff caught it, because no run had ever asked.
func TestAServiceLeftRegisteredFails(t *testing.T) {
	r := clean()
	r.ServiceGone = "false"
	r.ServiceQuery = "SERVICE_NAME: BeaconCollector STATE: 4 RUNNING"

	findings := findingsFor(r)
	if !has(findings, "uninstall.service_removed", SevFail) {
		t.Fatalf("a still-registered service did not fail: %#v", findings)
	}
	// The evidence has to travel with the finding, or whoever reads the report cannot tell a
	// still-registered service from a probe that misfired.
	for _, f := range findings {
		if f.Check == "uninstall.service_removed" && !strings.Contains(f.Summary, "BeaconCollector") {
			t.Fatalf("the finding does not quote what the service manager said: %q", f.Summary)
		}
	}
}

// TestAnUnaskedServiceQuestionIsWarnedNotPassed covers the third state.
//
// The supervised backend has no service manager to ask, and a probe that could not identify what was
// registered has no question to put to one. Neither is evidence of a clean removal, and reporting
// either as one would be exactly the silent pass this tool exists to prevent.
func TestAnUnaskedServiceQuestionIsWarnedNotPassed(t *testing.T) {
	r := clean()
	r.ServiceGone = ""
	r.ServiceKind = "none"

	findings := findingsFor(r)
	if has(findings, "uninstall.service_removed", SevInfo) {
		t.Fatal("an unasked question was reported as a clean removal")
	}
	if !has(findings, "uninstall.service_removed", SevWarn) {
		t.Fatalf("an unasked question produced no warning: %#v", findings)
	}
}

// TestDestroyedDataFails covers the opposite direction from a leftover service: too much removed
// rather than too little.
func TestDestroyedDataFails(t *testing.T) {
	r := clean()
	r.LogRetained = "false"
	if !has(findingsFor(r), "uninstall.data_retained", SevFail) {
		t.Fatal("a runtime log destroyed by a non-purge uninstall did not fail")
	}
}

func TestUncheckedRetentionIsWarnedNotPassed(t *testing.T) {
	r := clean()
	r.LogRetained = ""
	findings := findingsFor(r)
	if has(findings, "uninstall.data_retained", SevInfo) {
		t.Fatal("unchecked retention was reported as confirmed")
	}
	if !has(findings, "uninstall.data_retained", SevWarn) {
		t.Fatalf("unchecked retention produced no warning: %#v", findings)
	}
}

func TestANonZeroUninstallFails(t *testing.T) {
	r := clean()
	r.ExitCode = "1"
	r.Output = "Error: could not stop the service"
	if !has(findingsFor(r), "uninstall.command_succeeded", SevFail) {
		t.Fatal("a failed uninstall command did not fail the run")
	}
}

// TestStatusDisagreeingWithTheMachineFails covers the case where the two authorities disagree.
//
// Both are asked on purpose. Beacon's status is Beacon's opinion of itself; the service manager is a
// fact about the machine. Those are the two that came apart in the original bug, so a check reading
// only one of them would have passed it.
func TestStatusDisagreeingWithTheMachineFails(t *testing.T) {
	r := clean()
	r.Status = `{"service":{"label":"beacon-collector.service","loaded":true,"running":true}}`
	if !has(findingsFor(r), "uninstall.status_agrees", SevFail) {
		t.Fatal("a status still reporting a running collector after uninstall did not fail")
	}
}
