package check

import (
	"strings"
	"testing"
)

func measuredFingerprint(outcome Outcome, count int) Fingerprint {
	return Fingerprint{
		Scenario:        "same-scenario",
		Outcome:         outcome,
		Actions:         map[string]int{"command.executed": count},
		Harnesses:       map[string]int{"claude_code": count},
		PopulatedFields: map[string]int{"command.command": count},
		EventCount:      count,
	}
}

func TestInconclusiveFingerprintsNeverClaimDirectionalChanges(t *testing.T) {
	for _, tc := range []struct {
		before Outcome
		after  Outcome
	}{
		{Pass, Inconclusive},
		{Fail, Inconclusive},
		{Inconclusive, Pass},
		{Inconclusive, Fail},
		{Inconclusive, Inconclusive},
	} {
		t.Run(string(tc.before)+" to "+string(tc.after), func(t *testing.T) {
			before := measuredFingerprint(tc.before, 3)
			after := Fingerprint{
				Scenario:        before.Scenario,
				Outcome:         tc.after,
				Actions:         map[string]int{"file.read": 1},
				Harnesses:       map[string]int{"copilot": 1},
				PopulatedFields: map[string]int{"file.path": 1},
				EventCount:      1,
			}
			d := CompareFingerprints(before, after)
			if d.HasRegression() || len(d.Regressions) != 0 || len(d.Improvements) != 0 {
				t.Errorf("an unmeasured run created directional evidence:\n%s", d.Describe())
			}
			if !strings.Contains(strings.Join(d.Neutral, "\n"), "not comparable") {
				t.Errorf("comparison needs an explicit neutral explanation: %+v", d)
			}
			report := d.Describe()
			if strings.Contains(report, "no capability change") || !strings.Contains(report, "capabilities not compared") {
				t.Errorf("report must distinguish missing measurement from equivalence:\n%s", report)
			}
		})
	}
}

func TestInconclusiveEmptyFingerprintsAreNotObservedEquivalence(t *testing.T) {
	d := CompareFingerprints(
		Fingerprint{Scenario: "same-scenario", Outcome: Inconclusive},
		Fingerprint{Scenario: "same-scenario", Outcome: Inconclusive},
	)
	if d.HasRegression() || len(d.Improvements) != 0 {
		t.Fatalf("empty inconclusive runs invented directional evidence: %+v", d)
	}
	if !strings.Contains(d.Describe(), "not comparable") || strings.Contains(d.Describe(), "no capability change") {
		t.Fatalf("two unmeasured runs were reported as equivalent:\n%s", d.Describe())
	}
}

func TestComparableFingerprintSignalsRemainDirectional(t *testing.T) {
	t.Run("presence loss", func(t *testing.T) {
		d := CompareFingerprints(measuredFingerprint(Pass, 3), measuredFingerprint(Pass, 0))
		if !d.HasRegression() || len(d.Regressions) != 3 || len(d.Improvements) != 0 {
			t.Fatalf("real comparable field/action/harness loss disappeared:\n%s", d.Describe())
		}
	})
	t.Run("presence gain", func(t *testing.T) {
		d := CompareFingerprints(measuredFingerprint(Pass, 0), measuredFingerprint(Pass, 3))
		if d.HasRegression() || len(d.Improvements) != 3 {
			t.Fatalf("real comparable gains disappeared:\n%s", d.Describe())
		}
	})
	t.Run("count changes", func(t *testing.T) {
		d := CompareFingerprints(measuredFingerprint(Pass, 3), measuredFingerprint(Pass, 1))
		if d.HasRegression() || len(d.Improvements) != 0 || len(d.Neutral) == 0 {
			t.Fatalf("ordinary count variation changed policy:\n%s", d.Describe())
		}
	})
	t.Run("unchanged measured run", func(t *testing.T) {
		d := CompareFingerprints(measuredFingerprint(Pass, 3), measuredFingerprint(Pass, 3))
		if !strings.Contains(d.Describe(), "no capability change") {
			t.Fatalf("a comparable unchanged run lost its normal report:\n%s", d.Describe())
		}
	})
	for _, tc := range []struct {
		before     Outcome
		after      Outcome
		regression bool
	}{
		{Pass, Fail, true},
		{Fail, Pass, false},
	} {
		t.Run(string(tc.before)+" to "+string(tc.after), func(t *testing.T) {
			d := CompareFingerprints(measuredFingerprint(tc.before, 3), measuredFingerprint(tc.after, 3))
			if d.HasRegression() != tc.regression || (!tc.regression && len(d.Improvements) == 0) {
				t.Fatalf("comparable outcome transition changed policy:\n%s", d.Describe())
			}
		})
	}
}

func TestResolvedHardFailureFingerprintRemainsComparable(t *testing.T) {
	v := Verdict{Scenario: "same-scenario", Outcome: Inconclusive}
	v.Add(Finding{Check: "invariant.jsonl_parseable", Severity: SevFail, Summary: "synthetic independent failure"})
	v.Resolve()
	after := v.Fingerprint(Log{})
	d := CompareFingerprints(Fingerprint{Scenario: v.Scenario, Outcome: Pass}, after)
	if after.Outcome != Fail || !d.HasRegression() || !strings.Contains(d.Describe(), "outcome PASS -> FAIL") {
		t.Fatalf("independent hard failure was neutralized:\n%s", d.Describe())
	}
}
