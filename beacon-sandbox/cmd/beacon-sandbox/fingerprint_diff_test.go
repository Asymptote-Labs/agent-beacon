package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/check"
)

func diffFingerprint(outcome check.Outcome, populated bool) check.Fingerprint {
	count := 0
	if populated {
		count = 3
	}
	return check.Fingerprint{
		Scenario:        "same-scenario",
		Outcome:         outcome,
		Actions:         map[string]int{"command.executed": count},
		Harnesses:       map[string]int{"claude_code": count},
		PopulatedFields: map[string]int{"command.command": count},
		EventCount:      count,
	}
}

func savedFingerprintDiff(t *testing.T, before, after check.Fingerprint) (string, error) {
	t.Helper()
	beforeDir := filepath.Join(t.TempDir(), "before")
	afterDir := filepath.Join(t.TempDir(), "after")
	for _, fixture := range []struct {
		dir         string
		fingerprint check.Fingerprint
	}{
		{beforeDir, before},
		{afterDir, after},
	} {
		if err := os.MkdirAll(fixture.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := fixture.fingerprint.WriteJSON(filepath.Join(fixture.dir, "fingerprint.json")); err != nil {
			t.Fatal(err)
		}
	}
	output, err := os.CreateTemp(t.TempDir(), "diff-report")
	if err != nil {
		t.Fatal(err)
	}
	commandErr := func() error {
		stdout := os.Stdout
		os.Stdout = output
		defer func() { os.Stdout = stdout }()
		return cmdDiff([]string{beforeDir, afterDir})
	}()
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b), commandErr
}

func TestPersistedFingerprintDiffTreatsInconclusiveAsUnmeasured(t *testing.T) {
	for _, tc := range []struct {
		before          check.Outcome
		after           check.Outcome
		beforePopulated bool
		afterPopulated  bool
	}{
		{check.Pass, check.Inconclusive, true, false},
		{check.Fail, check.Inconclusive, true, false},
		{check.Inconclusive, check.Pass, false, true},
		{check.Inconclusive, check.Fail, false, true},
		{check.Inconclusive, check.Inconclusive, true, false},
	} {
		t.Run(string(tc.before)+" to "+string(tc.after), func(t *testing.T) {
			before := diffFingerprint(tc.before, tc.beforePopulated)
			after := diffFingerprint(tc.after, tc.afterPopulated)
			report, err := savedFingerprintDiff(t, before, after)
			if err != nil {
				t.Errorf("an inconclusive comparison was treated as a failed regression: %v", err)
			}
			if strings.Contains(report, "REGRESSION") || strings.Contains(report, "improved") || strings.Contains(report, "no capability change") {
				t.Errorf("offline report claimed directional proof/equivalence:\n%s", report)
			}
			if !strings.Contains(report, "not comparable") || !strings.Contains(report, "capabilities not compared") {
				t.Errorf("offline report omitted its measurement limitation:\n%s", report)
			}
			if report != check.CompareFingerprints(before, after).Describe() {
				t.Errorf("saved fingerprint report diverged from the direct comparison:\n%s", report)
			}
		})
	}
}

func TestPersistedFingerprintDiffPreservesComparableSignals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     check.Fingerprint
		after      check.Fingerprint
		wantError  bool
		wantReport string
	}{
		{"real presence loss", diffFingerprint(check.Pass, true), diffFingerprint(check.Pass, false), true, "REGRESSION"},
		{"real presence gain", diffFingerprint(check.Pass, false), diffFingerprint(check.Pass, true), false, "improved"},
		{"outcome failure", diffFingerprint(check.Pass, true), diffFingerprint(check.Fail, true), true, "outcome PASS -> FAIL"},
		{"outcome recovery", diffFingerprint(check.Fail, true), diffFingerprint(check.Pass, true), false, "outcome FAIL -> PASS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := savedFingerprintDiff(t, tc.before, tc.after)
			if (err != nil) != tc.wantError || !strings.Contains(report, tc.wantReport) {
				t.Errorf("comparable diff policy changed: error=%v\n%s", err, report)
			}
		})
	}
}

func TestFingerprintDiffRefusesDifferentScenarios(t *testing.T) {
	before := diffFingerprint(check.Inconclusive, true)
	after := diffFingerprint(check.Inconclusive, false)
	after.Scenario = "different-scenario"
	report, err := savedFingerprintDiff(t, before, after)
	if err == nil || !strings.Contains(err.Error(), "refusing to compare different scenarios") || report != "" {
		t.Errorf("scenario mismatch bypassed by neutrality: error=%v, report=%q", err, report)
	}
}
