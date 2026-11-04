package hermessession

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// hermesFixtureHome moves the home directory to a temporary one, clears every variable that moves
// Hermes's home, and returns the home and the native root Hermes would use there.
func hermesFixtureHome(t *testing.T) (home, native string) {
	t.Helper()
	home = t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	native = filepath.Join(home, ".hermes")
	if runtime.GOOS == "windows" {
		native = filepath.Join(home, "LocalAppData", "hermes")
	}
	return home, native
}

func mkdirAll(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeActiveProfile(t *testing.T, root, content string) {
	t.Helper()
	mkdirAll(t, root)
	if err := os.WriteFile(filepath.Join(root, "active_profile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func homeDirOf(t *testing.T, home string) string {
	t.Helper()
	dir, err := HomeDir(home)
	if err != nil {
		t.Fatalf("HomeDir: %v", err)
	}
	return dir
}

func TestHomeDirFollowsHermesHome(t *testing.T) {
	home, native := hermesFixtureHome(t)
	work := t.TempDir()
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(work, "hermes-data")
	t.Setenv("BEACON_HERMES_HOME_FIXTURE", custom)
	for _, tc := range []struct{ name, value, want string }{
		{"blank", " \t ", native},
		{"absolute", custom, custom},
		{"trimmed", "  " + custom + "\n", custom},
		{"relative", "hermes-data", filepath.Join(cwd, "hermes-data")},
		{"tilde path", "~/hermes-data", filepath.Join(home, "hermes-data")},
		{"variable", "$BEACON_HERMES_HOME_FIXTURE", custom},
		{"braced variable", "${BEACON_HERMES_HOME_FIXTURE}", custom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERMES_HOME", tc.value)
			got := homeDirOf(t, home)
			if got != tc.want {
				t.Fatalf("HomeDir with HERMES_HOME=%q = %q, want %q", tc.value, got, tc.want)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("HomeDir with HERMES_HOME=%q = %q, want an absolute path", tc.value, got)
			}
			if db := DefaultDBPath(); db != filepath.Join(tc.want, "state.db") {
				t.Fatalf("DefaultDBPath with HERMES_HOME=%q = %q, want %q", tc.value, db, filepath.Join(tc.want, "state.db"))
			}
		})
	}
}

// `hermes profile use <name>` records the profile in <root>/active_profile, and the launcher moves
// HERMES_HOME to <root>/profiles/<name> for every later plain `hermes` command.
func TestHomeDirFollowsActiveProfile(t *testing.T) {
	home, native := hermesFixtureHome(t)
	work := filepath.Join(native, "profiles", "work")
	mkdirAll(t, work)
	for _, tc := range []struct{ name, content, want string }{
		{"no file", "", native},
		{"named", "work\n", work},
		{"case-insensitive", "Work", work},
		{"default", "default", native},
		{"default any case", "Default\n", native},
		{"blank", " \n", native},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(native, "active_profile")
			_ = os.Remove(path)
			if tc.name != "no file" {
				writeActiveProfile(t, native, tc.content)
			}
			if got := homeDirOf(t, home); got != tc.want {
				t.Fatalf("HomeDir with active_profile %q = %q, want %q", tc.content, got, tc.want)
			}
			if db := DefaultDBPath(); db != filepath.Join(tc.want, "state.db") {
				t.Fatalf("DefaultDBPath with active_profile %q = %q, want %q", tc.content, db, filepath.Join(tc.want, "state.db"))
			}
		})
	}
}

// Hermes refuses to start when active_profile names a profile it cannot use, so Beacon reports the
// problem instead of using the default profile's directory.
func TestHomeDirRefusesUnusableActiveProfile(t *testing.T) {
	home, native := hermesFixtureHome(t)
	mkdirAll(t, filepath.Join(native, "profiles", "hermes"))
	for _, tc := range []struct{ name, content, want string }{
		{"missing", "gone", "does not exist"},
		{"traversal", "../../outside", "not a valid profile name"},
		{"reserved", "hermes", "not a valid profile name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeActiveProfile(t, native, tc.content)
			dir, err := HomeDir(home)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "active_profile") {
				t.Fatalf("HomeDir with active_profile %q = %q, %v; want an error saying %q", tc.content, dir, err, tc.want)
			}
			// The session reader keeps reading the base directory's store.
			if db := DefaultDBPath(); db != filepath.Join(native, "state.db") {
				t.Fatalf("DefaultDBPath with an unusable profile = %q, want %q", db, filepath.Join(native, "state.db"))
			}
		})
	}
}

func TestHomeDirProfileRootFollowsHermesHome(t *testing.T) {
	home, native := hermesFixtureHome(t)
	nativeWork := filepath.Join(native, "profiles", "work")
	mkdirAll(t, nativeWork)
	writeActiveProfile(t, native, "work")

	t.Run("HERMES_HOME names a profile", func(t *testing.T) {
		// An explicit profile directory wins over the sticky profile.
		coder := filepath.Join(native, "profiles", "coder")
		mkdirAll(t, coder)
		t.Setenv("HERMES_HOME", coder)
		if got := homeDirOf(t, home); got != coder {
			t.Fatalf("HomeDir = %q, want the profile HERMES_HOME names, %q", got, coder)
		}
	})

	t.Run("HERMES_HOME is the native root", func(t *testing.T) {
		// systemd units commonly hard-code HERMES_HOME=~/.hermes; the sticky profile still applies.
		t.Setenv("HERMES_HOME", native)
		if got := homeDirOf(t, home); got != nativeWork {
			t.Fatalf("HomeDir = %q, want %q", got, nativeWork)
		}
	})

	t.Run("HERMES_HOME elsewhere is its own profile root", func(t *testing.T) {
		data := t.TempDir()
		t.Setenv("HERMES_HOME", data)
		if got := homeDirOf(t, home); got != data {
			t.Fatalf("HomeDir = %q, want %q: the native root's active_profile does not apply", got, data)
		}
		research := filepath.Join(data, "profiles", "research")
		mkdirAll(t, research)
		writeActiveProfile(t, data, "research")
		if got := homeDirOf(t, home); got != research {
			t.Fatalf("HomeDir = %q, want %q", got, research)
		}
		if db := DefaultDBPath(); db != filepath.Join(research, "state.db") {
			t.Fatalf("DefaultDBPath = %q, want %q", db, filepath.Join(research, "state.db"))
		}
	})
}

func TestHomeDirFollowsActiveProfileUnderSuffixedRoot(t *testing.T) {
	home, native := hermesFixtureHome(t)
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "-dev")
	suffixed := native + "-dev"
	work := filepath.Join(suffixed, "profiles", "work")
	mkdirAll(t, work)
	// The unsuffixed root's sticky profile belongs to another installation.
	writeActiveProfile(t, native, "other")
	if got := homeDirOf(t, home); got != suffixed {
		t.Fatalf("HomeDir = %q, want the suffixed root %q", got, suffixed)
	}
	writeActiveProfile(t, suffixed, "work")
	if got := homeDirOf(t, home); got != work {
		t.Fatalf("HomeDir = %q, want %q", got, work)
	}
}

func TestHomeDirNeedsAHome(t *testing.T) {
	_, _ = hermesFixtureHome(t)
	t.Setenv("LOCALAPPDATA", "")
	if dir, err := HomeDir(""); err == nil {
		t.Fatalf("HomeDir with no home and no HERMES_HOME = %q, want an error", dir)
	}
	data := t.TempDir()
	t.Setenv("HERMES_HOME", data)
	if dir, err := HomeDir(""); err != nil || dir != data {
		t.Fatalf("HomeDir with only HERMES_HOME = %q, %v; want %q", dir, err, data)
	}
}

// Hermes compares resolved paths, so a HERMES_HOME reached through the real directory behind a
// symlinked ~/.hermes, even one that does not exist yet, is still inside the native root.
func TestHomeDirComparesResolvedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege Windows runners do not grant by default")
	}
	home, native := hermesFixtureHome(t)
	real := t.TempDir()
	if err := os.Symlink(real, native); err != nil {
		t.Fatal(err)
	}
	mkdirAll(t, filepath.Join(real, "profiles", "work"))
	writeActiveProfile(t, real, "work")
	t.Setenv("HERMES_HOME", filepath.Join(real, "not-yet", "custom"))
	if got, want := homeDirOf(t, home), filepath.Join(native, "profiles", "work"); got != want {
		t.Fatalf("HomeDir = %q, want %q", got, want)
	}
}
