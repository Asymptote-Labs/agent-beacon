package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// The cursor is what keeps a scheduled sweep from re-appending every Hermes session's whole
// history, so it has to land beside the log that sweep writes to. A system-mode sweep that stores
// its cursor under the operator's home lets a later user-mode run skip sessions the user log never
// received, and vice versa.
func TestHermesStatePathFollowsTheSweepItBelongsTo(t *testing.T) {
	if got, err := resolveHermesStatePath("/explicit/path.json", "", true); err != nil || got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden: %q, %v", got, err)
	}
	if got, err := resolveHermesStatePath("/explicit/path.json", "", false); err != nil || got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden in system mode: %q, %v", got, err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")

	user, err := resolveHermesStatePath("", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".beacon", "endpoint", "state", "hermes.json"); user != want {
		t.Errorf("user cursor = %q, want %q", user, want)
	}

	system, err := resolveHermesStatePath("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(system) {
		t.Errorf("system cursor = %q, which is not somewhere a sweep can reliably write", system)
	}
	if strings.HasPrefix(system, home) {
		t.Errorf("a system sweep put its cursor in the operator's home: %q", system)
	}
	if system == user {
		t.Errorf("system and user sweeps share one cursor at %q", system)
	}
}
