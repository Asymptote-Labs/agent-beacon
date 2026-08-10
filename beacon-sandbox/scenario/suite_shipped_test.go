package scenario

import (
	"strings"
	"testing"
)

// TestShippedSuiteLoadsAndValidates covers the scenarios this repository actually ships.
//
// Every other test in this file builds a scenario in a temp directory, so a typo in a real one --
// an unknown field, a malformed expectation, a platform that does not exist -- was caught by nothing
// here. It surfaced instead when someone dispatched a run, which on Windows means waiting several
// minutes for a runner to tell you a YAML key was misspelled.
//
// Loading the directory is most of the value: LoadSuite validates each scenario as it reads it, so
// this fails on anything Validate rejects.
func TestShippedSuiteLoadsAndValidates(t *testing.T) {
	suite, err := LoadSuite("../scenarios")
	if err != nil {
		t.Fatalf("the shipped scenario suite does not load: %v", err)
	}
	if len(suite.Scenarios) == 0 {
		t.Fatal("the shipped scenario suite is empty; LoadSuite found no files")
	}

	seen := make(map[string]bool, len(suite.Scenarios))
	for _, scenario := range suite.Scenarios {
		if seen[scenario.ID] {
			// Two scenarios with one id means one of them is unreachable by --scenario, and which one
			// depends on directory order.
			t.Fatalf("duplicate scenario id %q", scenario.ID)
		}
		seen[scenario.ID] = true

		// Every expectation states why it exists. That convention is what makes a failure report
		// readable by someone who did not write the scenario, and it is worth enforcing rather than
		// hoping for -- a missing `why` shows up as a bare assertion name in the verdict.
		for i, expect := range scenario.Expect {
			if strings.TrimSpace(expect.Why) == "" {
				t.Errorf("%s: expect[%d] (%s) has no `why`", scenario.ID, i, expect.Action)
			}
		}
	}
}

// TestWindowsScenariosDeclareTheirPlatform guards a default that fails in the wrong direction.
//
// Platform defaults to linux when unset, so a Windows scenario that forgets the field is silently
// filed as a Linux one: it would be selected for a Modal run, where its `C:\` paths and PowerShell
// prompt cannot work, and skipped by the Windows runner that was meant to run it.
func TestWindowsScenariosDeclareTheirPlatform(t *testing.T) {
	suite, err := LoadSuite("../scenarios")
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}
	for _, scenario := range suite.Scenarios {
		// The id prefix is the convention this suite already follows, so it is a usable independent
		// signal for what the platform field should say.
		wantWindows := strings.HasPrefix(scenario.ID, "w")
		isWindows := scenario.TargetPlatform() == PlatformWindows
		if wantWindows != isWindows {
			t.Errorf("%s: id prefix and platform disagree (platform=%q); a w-prefixed scenario must "+
				"declare platform: windows, or it will be dispatched to Linux",
				scenario.ID, scenario.TargetPlatform())
		}
	}
}
