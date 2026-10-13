package hermessession

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestDefaultDBPathFollowsHermesProfile(t *testing.T) {
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

// Expected paths come from the actual Python ntpath producer expansion oracle.
func TestWindowsHermesProfileExpansionMatchesProducer(t *testing.T) {
	t.Setenv("BEACON_UNSET_FIXTURE", "")
	if err := os.Unsetenv("BEACON_UNSET_FIXTURE"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_PROFILE_FIXTURE", `C:\owned\profile`)
	t.Setenv("BEACON_NONRECURSIVE_FIXTURE", "$BEACON_PROFILE_FIXTURE")
	for _, tc := range []struct{ path, want string }{
		{"%BEACON_PROFILE_FIXTURE%/work", "C:\\owned\\profile/work"},
		{"$BEACON_PROFILE_FIXTURE/work", "C:\\owned\\profile/work"},
		{"${BEACON_PROFILE_FIXTURE}/work", "C:\\owned\\profile/work"},
		{"%BEACON_UNSET_FIXTURE%/work", "%BEACON_UNSET_FIXTURE%/work"},
		{"%%profile%%", "%profile%"},
		{"$$profile", "$profile"},
		{"'$BEACON_PROFILE_FIXTURE'/work", "'$BEACON_PROFILE_FIXTURE'/work"},
		{"%BEACON_PROFILE_FIXTURE%/$BEACON_UNSET_FIXTURE", "C:\\owned\\profile/$BEACON_UNSET_FIXTURE"},
		{"%missing$BEACON_PROFILE_FIXTURE", "%missing$BEACON_PROFILE_FIXTURE"},
		{"${missing$BEACON_PROFILE_FIXTURE", "${missing$BEACON_PROFILE_FIXTURE"},
		{"%BEACON_NONRECURSIVE_FIXTURE%/work", "$BEACON_PROFILE_FIXTURE/work"},
		{"%", "%"},
		{"${", "${"},
	} {
		if got := hermesExpandWindowsVars(tc.path); got != tc.want {
			t.Errorf("Windows profile expansion(%q)=%q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestHermesExpansionPreservesUnsetUnixReference(t *testing.T) {
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
