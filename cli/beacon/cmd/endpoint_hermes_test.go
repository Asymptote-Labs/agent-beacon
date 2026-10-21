package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
)

// The cursor is what keeps a scheduled sweep from re-appending every Hermes session's whole
// history, so it has to land beside the log that sweep writes to. A system-mode sweep that stores
// its cursor under the operator's home lets a later user-mode run skip sessions the user log never
// received, and vice versa.
func TestHermesStatePathFollowsTheSweepItBelongsTo(t *testing.T) {
	if got := resolveHermesStatePath("/explicit/path.json", "", true); got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden: %q", got)
	}
	if got := resolveHermesStatePath("/explicit/path.json", "", false); got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden in system mode: %q", got)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")

	// The native root keeps the cursor file installs had before homes were keyed, however its
	// database is named.
	user := resolveHermesStatePath("", "", true)
	if want := filepath.Join(home, ".beacon", "endpoint", "state", "hermes.json"); user != want {
		t.Errorf("user cursor = %q, want %q", user, want)
	}
	if explicit := resolveHermesStatePath("", hermessession.DefaultDBPath(), true); explicit != user {
		t.Errorf("--db naming the native store moved its cursor to %q", explicit)
	}
	// Every other home gets its own cursor: a profile, a custom HERMES_HOME (the default profile of
	// its own root), and a same-named profile under that root.
	seen := map[string]string{user: "native root"}
	for _, dir := range []string{
		filepath.Join(home, ".hermes", "profiles", "work"),
		filepath.Join(home, "custom-hermes"),
		filepath.Join(home, "custom-hermes", "profiles", "work"),
	} {
		cursor := resolveHermesStatePath("", filepath.Join(dir, "state.db"), true)
		if other, ok := seen[cursor]; ok {
			t.Errorf("%s shares cursor %q with %s", dir, cursor, other)
		}
		seen[cursor] = dir
	}

	system := resolveHermesStatePath("", "", false)
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

// The command fixes its database once: the default is not re-resolved per sweep, and a relative
// --db run from inside a profile names that profile's home rather than ".".
func TestHermesDBPathIsResolvedOnceAndAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	if got := resolveHermesDBPath(""); got != hermessession.DefaultDBPath() {
		t.Errorf("default db = %q, want %q", got, hermessession.DefaultDBPath())
	}
	work := filepath.Join(home, ".hermes", "profiles", "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	got := resolveHermesDBPath("state.db")
	if !filepath.IsAbs(got) || hermessession.Profile(filepath.Dir(got)) != "work" {
		t.Errorf("relative --db resolved to %q, want an absolute path in the work profile", got)
	}
}
