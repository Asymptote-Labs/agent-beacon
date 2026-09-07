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

// Kiro merges hook files across scopes rather than taking the first it finds, so a user-scope and
// a project-scope install are both live at once. Reporting both is what keeps the inventory from
// understating an install -- the opposite of the OpenHands case, where showing one scope would
// overstate it.
func TestKiroCandidatesCoverBothScopes(t *testing.T) {
	t.Setenv("KIRO_HOME", "")
	home := t.TempDir()
	work := t.TempDir()

	items := kiroCandidates(home, work)
	if len(items) != 2 {
		t.Fatalf("kiroCandidates returned %d entries, want one per scope: %+v", len(items), items)
	}
	byScope := map[string]candidate{}
	for _, item := range items {
		byScope[item.scope] = item
	}
	user, ok := byScope[ScopeUser]
	if !ok {
		t.Fatalf("no user-scope candidate: %+v", items)
	}
	if want := filepath.Join(home, ".kiro", "hooks", "beacon-endpoint.json"); user.path != want {
		t.Errorf("user candidate = %q, want %q", user.path, want)
	}
	project, ok := byScope[ScopeProject]
	if !ok {
		t.Fatalf("no project-scope candidate: %+v", items)
	}
	if want := filepath.Join(work, ".kiro", "hooks", "beacon-endpoint.json"); project.path != want {
		t.Errorf("project candidate = %q, want %q", project.path, want)
	}
}

// KIRO_HOME moves the global Kiro directory, and on a machine that sets it ~/.kiro is not where
// Kiro looks. An inventory scanning the wrong directory reports "not installed" for a working
// install, which is the direction that matters: it understates coverage silently.
func TestKiroUserCandidateHonorsKiroHome(t *testing.T) {
	home := t.TempDir()
	profile := t.TempDir()
	t.Setenv("KIRO_HOME", profile)

	items := kiroCandidates(home, t.TempDir())
	want := filepath.Join(profile, "hooks", "beacon-endpoint.json")
	found := false
	for _, item := range items {
		if item.path == want {
			found = true
		}
		if item.scope == ScopeUser && item.path != want {
			t.Errorf("user candidate = %q, want %q", item.path, want)
		}
	}
	if !found {
		t.Fatalf("no candidate under KIRO_HOME: %+v", items)
	}
}

// The project scope does not consult KIRO_HOME: that variable moves the global directory, not the
// workspace one.
func TestKiroProjectCandidateIgnoresKiroHome(t *testing.T) {
	t.Setenv("KIRO_HOME", t.TempDir())
	work := t.TempDir()

	for _, item := range kiroCandidates(t.TempDir(), work) {
		if item.scope != ScopeProject {
			continue
		}
		if want := filepath.Join(work, ".kiro", "hooks", "beacon-endpoint.json"); item.path != want {
			t.Fatalf("project candidate = %q, want %q", item.path, want)
		}
		return
	}
	t.Fatal("no project-scope candidate")
}
