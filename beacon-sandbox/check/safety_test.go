package check

import (
	"strings"
	"testing"
)

func TestHostChangeFailsTheRun(t *testing.T) {
	v := Verdict{Scenario: "t"}
	Safety(&v, HostSafety{
		HostChanged:  "modified: /Users/x/.beacon (absent -> present:abcd1234)",
		ArgvCheckRan: true,
	})
	v.Resolve()

	if v.Outcome != Fail {
		t.Fatalf("a guarded host change must fail the run, got %s", v.Outcome)
	}
	if !hasCheck(v, "safety.host_untouched") {
		t.Errorf("expected safety.host_untouched to fire:\n%s", v.Report())
	}
}

func TestCleanHostPasses(t *testing.T) {
	v := Verdict{Scenario: "t"}
	Safety(&v, HostSafety{HostChanged: "host state unchanged", ArgvCheckRan: true})
	v.Resolve()

	if v.Outcome != Pass {
		t.Fatalf("expected PASS for a clean host, got %s:\n%s", v.Outcome, v.Report())
	}
	if len(v.Findings) != 0 {
		t.Errorf("a clean, verified run should add no findings, got %d", len(v.Findings))
	}
}

func TestSecretInArgvFailsTheRun(t *testing.T) {
	v := Verdict{Scenario: "t"}
	Safety(&v, HostSafety{SecretInArgv: true, ArgvCheckRan: true})
	v.Resolve()

	if v.Outcome != Fail {
		t.Fatalf("a credential in argv must fail the run, got %s", v.Outcome)
	}
	if !hasCheck(v, "safety.secret_not_in_argv") {
		t.Errorf("expected the argv check to fire:\n%s", v.Report())
	}
}

// A check that did not run must read as unproven, never as clean. This is the exact mistake
// that left the README's argv claim unverified in the first place.
func TestSkippedArgvCheckIsWarnedNotSilent(t *testing.T) {
	v := Verdict{Scenario: "t"}
	Safety(&v, HostSafety{SecretInArgv: false, ArgvCheckRan: false})
	v.Resolve()

	if !hasCheck(v, "safety.secret_not_in_argv") {
		t.Fatalf("a skipped check must still be reported:\n%s", v.Report())
	}
	for _, f := range v.Findings {
		if f.Check == "safety.secret_not_in_argv" {
			if f.Severity != SevWarn {
				t.Errorf("expected warn for an unrun check, got %s", f.Severity)
			}
			if !strings.Contains(f.Summary, "unverified") {
				t.Errorf("summary should say unverified, got %q", f.Summary)
			}
		}
	}
	if v.Outcome == Fail {
		t.Error("an unrun check should not fail the run; it is unproven, not disproven")
	}
}

func TestFingerprintRecordsZeroesForMissingFields(t *testing.T) {
	log, err := ReadLog(writeLog(t, goodLog()))
	if err != nil {
		t.Fatal(err)
	}
	v := Verdict{Scenario: "t", Outcome: Pass}
	fp := v.Fingerprint(log)

	// command.command is populated in goodLog; file.path is not. Both must be present as
	// keys, because a regression from 5 to 0 has to be distinguishable from a missing key.
	if fp.PopulatedFields["command.command"] != 1 {
		t.Errorf("expected command.command populated once, got %d", fp.PopulatedFields["command.command"])
	}
	if n, ok := fp.PopulatedFields["file.path"]; !ok || n != 0 {
		t.Errorf("expected file.path recorded as 0, got %d (present=%v)", n, ok)
	}
	if fp.Actions["command.executed"] != 1 {
		t.Errorf("expected the action histogram populated, got %v", fp.Actions)
	}
}

// The headline purpose of fingerprints: catch a field going empty even when the action still
// fires and every expectation still passes.
func TestFieldGoingEmptyIsARegression(t *testing.T) {
	before := Fingerprint{Scenario: "s", Outcome: Pass,
		Actions:         map[string]int{"command.executed": 1},
		PopulatedFields: map[string]int{"command.command": 1},
	}
	after := Fingerprint{Scenario: "s", Outcome: Pass,
		Actions:         map[string]int{"command.executed": 1},
		PopulatedFields: map[string]int{"command.command": 0},
	}
	d := CompareFingerprints(before, after)

	if !d.HasRegression() {
		t.Fatalf("a field going empty must be a regression:\n%s", d.Describe())
	}
	if !strings.Contains(d.Describe(), "command.command no longer populated") {
		t.Errorf("regression should name the field, got:\n%s", d.Describe())
	}
}

func TestActionDisappearingIsARegression(t *testing.T) {
	before := Fingerprint{Scenario: "s", Actions: map[string]int{"file.read": 2}}
	after := Fingerprint{Scenario: "s", Actions: map[string]int{}}

	d := CompareFingerprints(before, after)
	if !d.HasRegression() {
		t.Fatalf("an action disappearing must be a regression:\n%s", d.Describe())
	}
}

// Count changes must NOT be regressions: a model taking three turns instead of two
// legitimately shifts every count, and flagging that would bury the real signal.
func TestCountChangeIsNeutralNotRegression(t *testing.T) {
	before := Fingerprint{Scenario: "s",
		Actions:         map[string]int{"token.usage": 8},
		PopulatedFields: map[string]int{"command.command": 1},
	}
	after := Fingerprint{Scenario: "s",
		Actions:         map[string]int{"token.usage": 12},
		PopulatedFields: map[string]int{"command.command": 3},
	}
	d := CompareFingerprints(before, after)

	if d.HasRegression() {
		t.Errorf("differing counts are nondeterminism, not regression:\n%s", d.Describe())
	}
	if len(d.Neutral) == 0 {
		t.Errorf("count changes should still be reported as varying:\n%s", d.Describe())
	}
}

func TestFieldBecomingPopulatedIsAnImprovement(t *testing.T) {
	before := Fingerprint{Scenario: "s", PopulatedFields: map[string]int{"command.command": 0}}
	after := Fingerprint{Scenario: "s", PopulatedFields: map[string]int{"command.command": 4}}

	d := CompareFingerprints(before, after)
	if d.HasRegression() {
		t.Error("a field becoming populated is not a regression")
	}
	if len(d.Improvements) == 0 || !strings.Contains(d.Describe(), "now populated") {
		t.Errorf("expected an improvement to be reported:\n%s", d.Describe())
	}
}

func TestPassToFailIsARegression(t *testing.T) {
	d := CompareFingerprints(
		Fingerprint{Scenario: "s", Outcome: Pass},
		Fingerprint{Scenario: "s", Outcome: Fail},
	)
	if !d.HasRegression() {
		t.Error("PASS -> FAIL must be a regression")
	}
}
