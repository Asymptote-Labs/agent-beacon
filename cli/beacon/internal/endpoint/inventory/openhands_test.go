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
