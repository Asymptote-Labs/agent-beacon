package inventory

import (
	"path/filepath"
	"testing"
)

// Beacon owns its Kiro hook file outright, but detection still keys on the hook command rather
// than on a marker: Kiro's v1 schema publishes exactly two top-level keys and says nothing about
// what its loader does with a third, so a marker would be a guess about a loader that fails in
// silence. Both directions matter -- missing the install reports telemetry as absent on a machine
// that has it, and claiming another runtime's hooks attributes someone else's install to Kiro.
func TestKiroManagedDetectionReadsTheHookCommand(t *testing.T) {
	for name, tc := range map[string]struct {
		hooks string
		want  bool
	}{
		"flag form": {`{"version":"v1","hooks":[{"name":"beacon-endpoint-telemetry-stop","trigger":"Stop",` +
			`"action":{"type":"command","command":"'/opt/beacon/hooks/beacon-hooks' --platform kiro --log '/tmp/runtime.jsonl' stop"}}]}`, true},
		"equals form": {`{"version":"v1","hooks":[{"name":"b","trigger":"Stop",` +
			`"action":{"type":"command","command":"beacon-hooks --platform=kiro stop"}}]}`, true},
		"another runtime": {`{"version":"v1","hooks":[{"name":"b","trigger":"Stop",` +
			`"action":{"type":"command","command":"beacon-hooks --platform claude stop"}}]}`, false},
		// The two runtimes whose Beacon file has the same name and lives at the same depth under a
		// dot directory. Nothing but the platform flag separates them, so this is where a
		// substring rule would have merged two installs.
		"grok": {`{"version":"v1","hooks":[{"name":"b","trigger":"Stop",` +
			`"action":{"type":"command","command":"beacon-hooks --platform grok stop"}}]}`, false},
		"the user's own": {`{"version":"v1","hooks":[{"name":"lint","trigger":"PostFileSave",` +
			`"action":{"type":"command","command":"npx eslint --fix"}}]}`, false},
		"no hooks at all": {`{"version":"v1","hooks":[]}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := beaconManaged(candidate{runtime: "kiro"}, []byte(tc.hooks))
			if got != tc.want {
				t.Errorf("beaconManaged(kiro) = %t, want %t for %s", got, tc.want, tc.hooks)
			}
		})
	}
}

// KIRO_HOME moves the global Kiro directory, and on a machine that sets it ~/.kiro is not where
// Kiro looks. An inventory scanning the wrong directory reports "not installed" for a working
// install, which is the direction that matters: it understates coverage silently. The project
// scope does not consult KIRO_HOME: that variable moves the global directory, not the workspace one.
func TestKiroCandidatesHonorKiroHomeForTheUserScopeOnly(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("KIRO_HOME", profile)
	work := t.TempDir()

	want := map[string]string{
		ScopeUser:    filepath.Join(profile, "hooks", "beacon-endpoint.json"),
		ScopeProject: filepath.Join(work, ".kiro", "hooks", "beacon-endpoint.json"),
	}
	items := kiroCandidates(t.TempDir(), work)
	if len(items) != len(want) {
		t.Fatalf("kiroCandidates returned %d entries, want one per scope: %+v", len(items), items)
	}
	for _, item := range items {
		if item.path != want[item.scope] {
			t.Errorf("%s candidate = %q, want %q", item.scope, item.path, want[item.scope])
		}
	}
}
