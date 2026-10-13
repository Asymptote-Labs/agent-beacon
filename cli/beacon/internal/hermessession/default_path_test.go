package hermessession

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestDefaultDBPathFollowsHermesHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	profile := filepath.Join(home, "profiles", "work")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	defaultPath := filepath.Join(home, ".hermes", "state.db")
	if runtime.GOOS == "windows" {
		defaultPath = filepath.Join(home, "LocalAppData", "hermes", "state.db")
	}
	t.Setenv("BEACON_HERMES_PROFILE_FIXTURE", profile)
	for _, tc := range []struct{ name, override, want string }{
		{"default", "", defaultPath},
		{"blank", "  ", defaultPath},
		{"absolute profile", profile, filepath.Join(profile, "state.db")},
		{"trimmed profile", "  " + profile + "  ", filepath.Join(profile, "state.db")},
		{"home expansion", "~/profiles/work", filepath.Join(profile, "state.db")},
		{"environment expansion", "$BEACON_HERMES_PROFILE_FIXTURE", filepath.Join(profile, "state.db")},
		{"braced environment expansion", "${BEACON_HERMES_PROFILE_FIXTURE}", filepath.Join(profile, "state.db")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERMES_HOME", tc.override)
			if got := DefaultDBPath(); got != tc.want {
				t.Fatalf("DefaultDBPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHermesExpansionPreservesUnsetReference(t *testing.T) {
	t.Setenv("BEACON_UNSET_FIXTURE", "")
	if err := os.Unsetenv("BEACON_UNSET_FIXTURE"); err != nil {
		t.Fatal(err)
	}
	if got := hermesExpandVars("$BEACON_UNSET_FIXTURE/work"); got != "$BEACON_UNSET_FIXTURE/work" {
		t.Fatalf("unset expansion=%q", got)
	}
}

func TestDefaultHermesRootIncludesProducerSuffix(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "-owned-fixture")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	want := filepath.Join(home, ".hermes-owned-fixture", "state.db")
	if runtime.GOOS == "windows" {
		want = filepath.Join(home, "LocalAppData", "hermes-owned-fixture", "state.db")
	}
	if got := DefaultDBPath(); got != want {
		t.Fatalf("suffixed default=%q, want %q", got, want)
	}
}
