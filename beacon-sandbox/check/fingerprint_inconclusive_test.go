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
