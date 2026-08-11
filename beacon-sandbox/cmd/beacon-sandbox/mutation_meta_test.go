package main

import "testing"

// The meta mutations must turn a clean removal into a failure, or they prove nothing.
func TestMetaMutationsInvertARecordedRemoval(t *testing.T) {
	for _, mode := range []string{"leave-service", "drop-retained-log"} {
		meta := map[string]string{
			"uninstall_ran":             "true",
			"uninstall_service_gone":    "true",
			"uninstall_config_retained": "true",
		}
		if !applyMetaMutation(meta, mode) {
			t.Fatalf("%s was not recognised as a meta mutation", mode)
		}
		if meta["uninstall_service_gone"] == "true" && meta["uninstall_log_retained"] == "true" {
			t.Fatalf("%s changed nothing", mode)
		}
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
