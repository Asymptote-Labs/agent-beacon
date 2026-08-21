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

// TestEveryPlatformVerifiesUninstallSomewhere encodes the gap this closed.
//
// The gap was not "one scenario is missing an assertion" — it was that *no* scenario on *any* platform
// ever asked whether uninstall removed anything. Every one installed, captured, and stopped. So the
// property worth pinning is per-platform coverage, which stays true as capture scenarios are added and
// would go false again if the lifecycle ones lost the flag.
//
// Deliberately not "every install scenario verifies uninstall". w03 and w04 install in order to test
// capture; making them assert removal too would report an uninstall regression against a capture
// scenario, which sends whoever reads it to the wrong code.
func TestEveryPlatformVerifiesUninstallSomewhere(t *testing.T) {
	suite, err := LoadSuite("../scenarios")
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}

	installs := map[Platform]int{}
	verifies := map[Platform]int{}
	for _, scenario := range suite.Scenarios {
		if scenario.Install == nil {
			continue
		}
		platform := scenario.TargetPlatform()
		installs[platform]++
		if scenario.Install.VerifyUninstall {
			verifies[platform]++
		}
	}

	if len(installs) == 0 {
		t.Fatal("no scenario installs an endpoint, so nothing covers the install path at all")
	}
	for platform, count := range installs {
		if verifies[platform] == 0 {
			t.Errorf("%s has %d install scenario(s) and none of them verifies uninstall; "+
				"an uninstall that reports success while leaving the service registered would go "+
				"unnoticed on this platform", platform, count)
		} else {
			t.Logf("%s: %d/%d install scenarios verify uninstall", platform, verifies[platform], count)
		}
	}
}
