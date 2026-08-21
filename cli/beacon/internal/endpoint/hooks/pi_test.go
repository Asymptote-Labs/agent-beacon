package hooks

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// An empty level means "the default", and the default has to be the user level. Every caller that
// does not care about scope passes the zero value, so treating "" as unknown would turn status,
// repair and uninstall into errors for the ordinary install.
func TestPiExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	want := filepath.Join(home, ".pi", "agent", "extensions", "beacon.ts")
	for _, level := range []Level{"", LevelUser} {
		got, err := PiExtensionPath(level)
		if err != nil {
			t.Fatalf("PiExtensionPath(%q) returned error: %v", level, err)
		}
		if got != want {
			t.Fatalf("PiExtensionPath(%q) = %q, want %q", level, got, want)
		}
	}
}

// Pi's project-level extension directory is `.pi/extensions`, not `.pi/agent/extensions`: the
// `agent` segment exists only under the home directory. Deriving one path from the other would
// write the project extension where Pi does not look for it, so the install would report success
// and collect nothing.
func TestPiExtensionPathProjectLevelOmitsAgentSegment(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := PiExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("PiExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".pi", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PiExtensionPath(project) = %q, want %q", got, want)
	}
}

func TestPiExtensionPathRejectsUnknownLevel(t *testing.T) {
	if _, err := PiExtensionPath(Level("machine")); err == nil {
		t.Fatal("PiExtensionPath accepted an unknown level; a typo in a scope flag must fail loudly " +
			"rather than silently installing at the default scope")
	}
}

// The marker is the only thing that distinguishes a file Beacon may overwrite from one it must
// not, and it is read by two other packages. Pinning the literal here means a change to it is a
// deliberate edit to a test rather than a silent break of install/uninstall/status agreement.
func TestPiManagedExtensionMarkerIsStable(t *testing.T) {
	if PiManagedExtensionMarker != "beacon-managed-pi-extension:v1" {
		t.Fatalf("PiManagedExtensionMarker = %q; changing it strands extensions installed by "+
			"earlier builds, which uninstall then refuses to remove", PiManagedExtensionMarker)
	}
}
