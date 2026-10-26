package hermessession

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// clonedHermesHomes makes the native root and a profile cloned from it: both hold the fixture
// session under the same id, as `hermes profile create --clone` leaves them. Each home syncs into
// its own log, because the writer drops an event that repeats another session's action at the same
// moment, which the clone's copied history does, and these tests are about ids rather than that.
func clonedHermesHomes(t *testing.T) (root, profile string) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	root = filepath.Join(home, ".hermes")
	profile = filepath.Join(root, "profiles", "work")
	for _, dir := range []string{root, profile} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		createHermesFixtureDB(t, filepath.Join(dir, "state.db"))
	}
	return root, profile
}

// homeLog is the runtime log a test syncs dir into.
func homeLog(dir string) string { return filepath.Join(dir, "runtime.jsonl") }

func syncHermesHome(t *testing.T, dir string) Summary {
	t.Helper()
	summary, err := CollectOnce(CollectOptions{DBPath: filepath.Join(dir, "state.db"), StatePath: DefaultStatePath(dir), LogPath: homeLog(dir), Write: true, UserMode: true})
	if err != nil {
		t.Fatalf("sync %s: %v", dir, err)
	}
	return summary
}

// addHermesPrompt appends a user message to the fixture session, the way each copy of a cloned
// profile goes on independently: both stores give the new row the same message id.
func addHermesPrompt(t *testing.T, dir, text string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO messages (id, session_id, role, content, timestamp, active) VALUES (4, 'hermes-session-1', 'user', ?, 1780633000.0, 1)`, text); err != nil {
		t.Fatal(err)
	}
}

func loadCursor(t *testing.T, dir string) *Cursor {
	t.Helper()
	state, err := LoadState(DefaultStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	cursor := state.Sessions["hermes-session-1"]
	if cursor == nil {
		t.Fatalf("no cursor for the fixture session in %s", DefaultStatePath(dir))
	}
	return cursor
}

func eventIDsByAction(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, ev := range readEvents(t, homeLog(dir)) {
		out[ev.Event.Action] = ev.Event.ID
	}
	return out
}

// assertNoSharedEventIDs fails when two events the homes wrote carry one id, which is what ingest,
// the backfill restage and share upsert would collapse into a single event.
func assertNoSharedEventIDs(t *testing.T, dirs ...string) {
	t.Helper()
	seen := map[string]schema.Event{}
	for _, dir := range dirs {
		for _, ev := range readEvents(t, homeLog(dir)) {
			if first, dup := seen[ev.Event.ID]; dup {
				a, _ := json.Marshal(first)
				b, _ := json.Marshal(ev)
				t.Fatalf("event id %s names two events:\n%s\n%s", ev.Event.ID, a, b)
			}
			seen[ev.Event.ID] = ev
		}
	}
}

// Issue #974: a cloned profile starts with a copy of state.db, so it holds the same session id as
// the home it was cloned from, and the copies' message ids count up independently once they
// diverge. The first home to sync the session keeps the unkeyed ids; the clone keys its own.
func TestCollectOnceKeysEventIDsOfAClonedProfile(t *testing.T) {
	root, profile := clonedHermesHomes(t)
	syncHermesHome(t, root)
	syncHermesHome(t, profile)

	assertFixtureEventIDs(t, eventIDsByAction(t, root))
	if got := len(readEvents(t, homeLog(profile))); got != len(fixtureEventIDs) {
		t.Fatalf("clone wrote %d events, want %d", got, len(fixtureEventIDs))
	}
	assertNoSharedEventIDs(t, root, profile)
	if loadCursor(t, root).KeyedByHome || !loadCursor(t, profile).KeyedByHome {
		t.Fatalf("root keyed = %v, clone keyed = %v, want only the clone keyed", loadCursor(t, root).KeyedByHome, loadCursor(t, profile).KeyedByHome)
	}

	// Both copies go on with a different message 4.
	addHermesPrompt(t, root, "deploy to staging")
	addHermesPrompt(t, profile, "rewrite the readme")
	if s := syncHermesHome(t, root); s.EventsEmitted != 1 {
		t.Fatalf("root's second sync wrote %d events, want the new prompt", s.EventsEmitted)
	}
	if s := syncHermesHome(t, profile); s.EventsEmitted != 1 {
		t.Fatalf("clone's second sync wrote %d events, want the new prompt", s.EventsEmitted)
	}
	assertNoSharedEventIDs(t, root, profile)

	if s := syncHermesHome(t, profile); s.EventsEmitted != 0 {
		t.Fatalf("clone's third sync wrote %d events, want none", s.EventsEmitted)
	}
}

// Whichever home syncs a session first keeps the unkeyed ids, so a clone synced before the home it
// came from is the one that keeps them.
func TestCollectOnceKeysTheSecondHomeToSyncASession(t *testing.T) {
	root, profile := clonedHermesHomes(t)
	syncHermesHome(t, profile)
	syncHermesHome(t, root)

	assertFixtureEventIDs(t, eventIDsByAction(t, profile))
	assertNoSharedEventIDs(t, root, profile)
	if !loadCursor(t, root).KeyedByHome || loadCursor(t, profile).KeyedByHome {
		t.Fatal("want only the home synced second keyed")
	}
}

// Homes that both collected a session before the session home file existed have already written
// it under the unkeyed ids, and share refresh matches on them. They keep those ids for the rest of
// the session rather than rename what is already uploaded.
func TestCollectOnceKeepsUnkeyedIDsOfSessionsCollectedBeforeHomesWereRecorded(t *testing.T) {
	root, profile := clonedHermesHomes(t)
	syncHermesHome(t, root)
	syncHermesHome(t, profile)
	// Undo what this change recorded, leaving both cursors as a sync before it would have.
	if err := os.Remove(SessionHomesPath(DefaultStatePath(root))); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(DefaultStatePath(profile))
	if err != nil {
		t.Fatal(err)
	}
	state.Sessions["hermes-session-1"].KeyedByHome = false
	if err := state.Save(DefaultStatePath(profile)); err != nil {
		t.Fatal(err)
	}

	addHermesPrompt(t, profile, "rewrite the readme")
	addHermesPrompt(t, root, "deploy to staging")
	syncHermesHome(t, profile)
	syncHermesHome(t, root)

	rootEvents, profileEvents := readEvents(t, homeLog(root)), readEvents(t, homeLog(profile))
	rootPrompt, profilePrompt := rootEvents[len(rootEvents)-1], profileEvents[len(profileEvents)-1]
	if rootPrompt.Event.Action != "prompt.submitted" || rootPrompt.Event.ID != profilePrompt.Event.ID {
		t.Fatalf("new prompts = %s %s / %s %s, want both under the unkeyed id they always had",
			rootPrompt.Event.Action, rootPrompt.Event.ID, profilePrompt.Event.Action, profilePrompt.Event.ID)
	}
	if loadCursor(t, profile).KeyedByHome || loadCursor(t, root).KeyedByHome {
		t.Fatal("a cursor that already wrote unkeyed ids became keyed")
	}
	homes, err := sessionHomesFile.Load(SessionHomesPath(DefaultStatePath(root)))
	if err != nil {
		t.Fatal(err)
	}
	if owner := homes.Sessions["hermes-session-1"]; owner != canonicalHome(profile) {
		t.Fatalf("session claimed by %q, want the first home to sync it after the upgrade, %q", owner, canonicalHome(profile))
	}
}

// A dry run reads no cursor and claims nothing, so it neither keys ids nor leaves a file behind.
func TestCollectOncePrintClaimsNothing(t *testing.T) {
	root, _ := clonedHermesHomes(t)
	if _, err := CollectOnce(CollectOptions{DBPath: filepath.Join(root, "state.db"), Print: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SessionHomesPath(DefaultStatePath(root))); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the session home file: %v", err)
	}
}
