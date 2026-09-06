package inventory

import (
	"path/filepath"
	"testing"
)

// OpenHands' hooks.json is a file the user also edits, and its schema forbids top-level fields it
// does not know -- so Beacon has no marker it could stamp there without failing validation and
// taking every hook in the file down. Detection keys on the hook command instead, and both
// directions matter: missing the install reports telemetry as absent on a machine that has it, and
// claiming another runtime's hooks attributes someone else's install to OpenHands.
func TestOpenHandsManagedDetectionReadsTheHookCommand(t *testing.T) {
	for name, tc := range map[string]struct {
		hooks string
		want  bool
	}{
		"flag form":       {`{"stop":[{"matcher":"*","hooks":[{"type":"command","command":"'/opt/beacon/hooks/beacon-hooks' --platform openhands --log '/tmp/runtime.jsonl' stop"}]}]}`, true},
		"equals form":     {`{"stop":[{"matcher":"*","hooks":[{"type":"command","command":"beacon-hooks --platform=openhands stop"}]}]}`, true},
		"wrapper form":    {`{"hooks":{"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"beacon-hooks --platform openhands stop"}]}]}}`, true},
		"another runtime": {`{"stop":[{"matcher":"*","hooks":[{"type":"command","command":"beacon-hooks --platform claude stop"}]}]}`, false},
		"opencode":        {`{"stop":[{"matcher":"*","hooks":[{"type":"command","command":"beacon-hooks --platform opencode stop"}]}]}`, false},
		"the user's own":  {`{"stop":[{"matcher":"*","hooks":[{"type":"command","command":".openhands/hooks/on_stop.sh"}]}]}`, false},
		"no hooks at all": {`{}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := beaconManaged(candidate{runtime: "openhands"}, []byte(tc.hooks))
			if got != tc.want {
				t.Errorf("beaconManaged(openhands) = %t, want %t for %s", got, tc.want, tc.hooks)
			}
		})
	}
}

// OpenHands reads the first hooks.json it finds rather than merging the two, so a repository with
// its own file shadows the user-level one entirely. Reporting both scopes is what lets an operator
// see that -- an inventory showing only the user file would report a working install for a machine
// where Beacon's hooks never run.
func TestOpenHandsCandidatesCoverBothScopes(t *testing.T) {
	t.Setenv("OH_PERSISTENCE_DIR", "")
	home := t.TempDir()
	work := t.TempDir()

	items := openHandsCandidates(home, work)
	if len(items) != 2 {
		t.Fatalf("openHandsCandidates returned %d entries, want one per scope: %+v", len(items), items)
	}
	byScope := map[string]candidate{}
	for _, item := range items {
		byScope[item.scope] = item
	}
	if got, want := byScope[ScopeUser].path, filepath.Join(home, ".openhands", "hooks.json"); got != want {
		t.Errorf("user candidate = %q, want %q", got, want)
	}
	if got, want := byScope[ScopeProject].path, filepath.Join(work, ".openhands", "hooks.json"); got != want {
		t.Errorf("project candidate = %q, want %q", got, want)
	}
	for _, item := range items {
		// KindHookConfig rather than KindManagedConfig: Beacon merges into a file the user owns,
		// it does not own the file.
		if item.kind != KindHookConfig {
			t.Errorf("candidate %q kind = %q, want %q", item.path, item.kind, KindHookConfig)
		}
		// The runtime name is the canonical harness name, so an inventory row and a telemetry row
		// for the same machine name the same runtime.
		if item.runtime != "openhands" {
			t.Errorf("candidate %q runtime = %q, want openhands", item.path, item.runtime)
		}
	}
}

// Sandboxed and containerized setups redirect OpenHands state onto a volume with
// OH_PERSISTENCE_DIR, and on a machine that sets it ~/.openhands is not where OpenHands looks. An
// inventory scanning the wrong directory reports "not installed" for a working install.
func TestOpenHandsUserCandidateHonorsThePersistenceDirEnv(t *testing.T) {
	persistence := t.TempDir()
	t.Setenv("OH_PERSISTENCE_DIR", persistence)

	items := openHandsCandidates(t.TempDir(), t.TempDir())
	for _, item := range items {
		if item.scope != ScopeUser {
			continue
		}
		if want := filepath.Join(persistence, "hooks.json"); item.path != want {
			t.Fatalf("user candidate = %q, want %q", item.path, want)
		}
		return
	}
	t.Fatal("openHandsCandidates returned no user-scope entry")
}
