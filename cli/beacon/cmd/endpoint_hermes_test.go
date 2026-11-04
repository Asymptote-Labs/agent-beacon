package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
	"github.com/spf13/cobra"
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

// Before cursors were keyed by home, every default sweep advanced hermes.json, including one whose
// default home was a custom HERMES_HOME. Its first keyed sweep starts from that cursor rather than
// re-emitting the history it already collected; another home never inherits it.
func TestHermesKeyedCursorSeedsFromTheLegacyCursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "custom-hermes"))

	legacy := legacyHermesStatePath("", "", true)
	if want := filepath.Join(home, ".beacon", "endpoint", "state", "hermes.json"); legacy != want {
		t.Fatalf("legacy cursor = %q, want %q", legacy, want)
	}
	seed := &hermessession.State{Version: hermessession.StateVersion, Sessions: map[string]*hermessession.Cursor{"s1": {Started: true, LastMessageID: 7}}}
	if err := seed.Save(legacy); err != nil {
		t.Fatal(err)
	}
	keyed := resolveHermesStatePath("", "", true)
	state, err := hermessession.LoadStateSeeded(keyed, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if c := state.Sessions["s1"]; c == nil || c.LastMessageID != 7 {
		t.Errorf("keyed cursor %q did not start from the legacy cursor: %+v", keyed, state.Sessions)
	}
	if other := legacyHermesStatePath("", filepath.Join(home, "elsewhere", "state.db"), true); other != "" {
		t.Errorf("a non-default home would inherit the legacy cursor %q", other)
	}
}

// Status names the sessions whose event ids a home keys by its own path, so a cloned profile's
// events can be told apart from the ones the home it came from wrote under the same session id.
func TestHermesStatusReportsSessionsKeyedByHome(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, started_at REAL NOT NULL)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT, timestamp REAL NOT NULL)`,
		`INSERT INTO sessions (id, source, started_at) VALUES ('cloned', 'cli', 1780632830), ('own', 'cli', 1780632831)`,
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('cloned', 'user', 'hi', 1780632832), ('own', 'user', 'hi', 1780632833)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	statePath := filepath.Join(dir, "hermes.json")
	state := &hermessession.State{Version: hermessession.StateVersion, Sessions: map[string]*hermessession.Cursor{
		"cloned": {Started: true, LastMessageID: 1, KeyedByHome: true},
		"own":    {Started: true, LastMessageID: 2},
	}}
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}

	savedHermes, savedEndpoint := endpointHermesOpts, endpointOpts
	t.Cleanup(func() { endpointHermesOpts, endpointOpts = savedHermes, savedEndpoint })
	endpointHermesOpts.dbPath, endpointHermesOpts.statePath = dbPath, statePath
	endpointOpts.userMode, endpointOpts.systemMode = true, false

	run := func(jsonOutput bool) string {
		t.Helper()
		endpointOpts.jsonOutput = jsonOutput
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		if err := runEndpointHermesStatus(cmd, nil); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	var report hermesStatusReport
	if err := json.Unmarshal([]byte(run(true)), &report); err != nil {
		t.Fatal(err)
	}
	keyed := map[string]bool{}
	for _, session := range report.Sessions {
		keyed[session.SessionID] = session.IDsKeyedByHome
	}
	if len(keyed) != 2 || !keyed["cloned"] || keyed["own"] {
		t.Fatalf("ids_keyed_by_home = %v, want only the cloned session", keyed)
	}
	text := run(false)
	if !strings.Contains(text, "- cloned [collected, ids keyed by home]") || strings.Contains(text, "- own [collected, ids keyed by home]") {
		t.Fatalf("status text = %q, want only the cloned session marked keyed", text)
	}
}

// Which home keeps a session's unkeyed event ids is decided by the session home file beside the
// cursor files. Every home a default sweep can read must therefore resolve to one such file per
// mode, or a cloned profile would never see the claim of the home it came from.
func TestHermesHomesShareOneSessionHomeFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	others := []string{
		filepath.Join(home, ".hermes", "profiles", "work", "state.db"),
		filepath.Join(home, "custom-hermes", "state.db"),
		filepath.Join(home, "custom-hermes", "profiles", "work", "state.db"),
	}
	for _, userMode := range []bool{true, false} {
		want := hermessession.SessionHomesPath(resolveHermesStatePath("", "", userMode))
		for _, db := range others {
			if got := hermessession.SessionHomesPath(resolveHermesStatePath("", db, userMode)); got != want {
				t.Errorf("user mode %v: %s uses session home file %q, want %q", userMode, db, got, want)
			}
		}
	}
}
