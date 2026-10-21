package hermessession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatePathForDBPreservesOnlyTheCanonicalNativeDefault(t *testing.T) {
	home, native := hermesFixtureHome(t)
	legacy := DefaultStatePath()
	statePath := func(db string) string {
		t.Helper()
		path, err := StatePathForDB(db, legacy)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	defaultDB := filepath.Join(native, "state.db")
	if implicit, explicit := statePath(""), statePath(defaultDB); implicit != legacy || explicit != legacy {
		t.Fatalf("native default cursors = %q, %q, want legacy %q", implicit, explicit, legacy)
	}
	profile := filepath.Join(native, "profiles", "work")
	mkdirAll(t, profile)
	writeActiveProfile(t, native, "work")
	active := statePath("")
	if active == legacy {
		t.Fatal("active profile inherited the native default's checkpoint")
	}
	t.Setenv("HERMES_HOME", profile)
	if explicit := statePath(filepath.Join(profile, "state.db")); statePath("") != active || explicit != active {
		t.Fatalf("active profile, HERMES_HOME and --db differ: %q, %q, %q", active, statePath(""), explicit)
	}
	if got := statePath(defaultDB); got != legacy {
		t.Fatalf("explicit native default with a selected profile = %q, want %q", got, legacy)
	}
	other := statePath(filepath.Join(home, "other", "profiles", "work", "state.db"))
	if other == legacy || other == active {
		t.Fatalf("a different root with the same profile name shares a cursor: %q", other)
	}
	if err := os.Remove(filepath.Join(native, "active_profile")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "-isolated")
	suffixed := statePath("")
	if suffixed == legacy || suffixed == active || suffixed == other {
		t.Fatalf("suffixed native root shares another store's cursor: %q", suffixed)
	}
	if got := statePath(DefaultDBPath()); got != suffixed {
		t.Fatalf("explicit suffixed database = %q, want %q", got, suffixed)
	}
}

func TestStatePathForDBUsesCanonicalStoreIdentity(t *testing.T) {
	home, _ := hermesFixtureHome(t)
	store := filepath.Join(home, "custom")
	mkdirAll(t, store)
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(store, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Chdir(home)
	var want string
	for _, db := range []string{filepath.Join(store, "state.db"), filepath.Join(alias, "state.db"), filepath.Join("custom", "state.db")} {
		got, err := StatePathForDB(db, DefaultStatePath())
		if err != nil {
			t.Fatal(err)
		}
		if want != "" && got != want {
			t.Fatalf("same store %q selects %q, want %q", db, got, want)
		}
		want = got
	}
}

func TestStatePathForDBDoesNotRequireHomeForAnExplicitStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("LOCALAPPDATA", "")
	got, err := StatePathForDB(dbPath, "legacy.json")
	if err != nil || got == "legacy.json" || got == "" {
		t.Fatalf("explicit nondefault store without a home = %q, %v", got, err)
	}
}
