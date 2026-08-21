package main

import "testing"

// The meta mutations must turn a clean removal into a failure, or they prove nothing.
//
// Each mode is checked against the one key it targets, rather than against a compound condition over
// several. The compound version could not fail: it read a key the fixture never seeded, so the
// expression was false whatever the mutation did, and a mutation that changed nothing would still
// have passed. A self-test that cannot fail is the exact thing these mutations exist to rule out, so
// getting it wrong here is worse than getting it wrong anywhere else in the tool.
func TestMetaMutationsInvertARecordedRemoval(t *testing.T) {
	cases := []struct {
		mode string
		key  string
	}{
		{mode: "leave-service", key: "uninstall_service_gone"},
		{mode: "drop-retained-log", key: "uninstall_log_retained"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			meta := map[string]string{
				"uninstall_ran":          "true",
				"uninstall_service_gone": "true",
				"uninstall_log_retained": "true",
			}
			if !applyMetaMutation(meta, tc.mode) {
				t.Fatalf("%s was not recognised as a meta mutation", tc.mode)
			}
			if meta[tc.key] != "false" {
				t.Fatalf("%s left %s as %q; the observation it is supposed to invert is unchanged, "+
					"so the self-test would report a passing check that was never exercised",
					tc.mode, tc.key, meta[tc.key])
			}
			// Only the one observation. A mutation that damaged several would produce a cascade of
			// findings, and the self-test could no longer say which check it had exercised.
			for _, other := range cases {
				if other.key == tc.key {
					continue
				}
				if meta[other.key] != "true" {
					t.Fatalf("%s also changed %s to %q; each mutation must invert exactly one observation",
						tc.mode, other.key, meta[other.key])
				}
			}
		})
	}
}

// A run that never uninstalled cannot demonstrate an uninstall check, and claiming otherwise would
// report a self-test as passed when it never ran.
func TestMetaMutationRefusesARunThatDidNotUninstall(t *testing.T) {
	if applyMetaMutation(map[string]string{}, "leave-service") {
		t.Fatal("a run with no uninstall accepted an uninstall mutation")
	}
}

func TestUnknownModesFallThroughToLogMutations(t *testing.T) {
	meta := map[string]string{"uninstall_ran": "true"}
	if applyMetaMutation(meta, "corrupt-line") {
		t.Fatal("corrupt-line was claimed by the meta mutator; it damages the log")
	}
}
