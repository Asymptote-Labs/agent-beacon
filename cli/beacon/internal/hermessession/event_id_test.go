package hermessession

import (
	"path/filepath"
	"testing"
)

// fixtureEventIDs are the event ids a sync has always written for createHermesFixtureDB's session.
// Beacon Cloud ingest, the backfill restage and share refresh all match events by id, so a change
// here renames events that are already on disk or uploaded.
var fixtureEventIDs = map[string]string{
	"session.started":  "9cb4ead8-df8e-558e-b4c2-df1366305b07",
	"prompt.submitted": "e87a3e65-57fb-5fd5-9096-dc0410fa4387",
	"agent.reasoning":  "05b16741-580d-520e-9af5-e6e7aa9a0101",
	"tool.invoked":     "8f05ae20-f638-5d56-a575-87560b7d5856",
	"command.executed": "e62fa517-ffa4-54cb-b0a3-83d03be1ebc5",
	"token.usage":      "bc335649-a99a-5b38-a076-df55428ef4ab",
	"session.ended":    "36454ef9-0ba3-595b-a0aa-42cabd23244f",
}

func TestMapSessionEventIDsAreStable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	createHermesFixtureDB(t, dbPath)
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions, err := store.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %v, %v", sessions, err)
	}
	messages, err := store.ReadMessages(sessions[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := MapSession(sessions[0], messages, &Cursor{})
	assertFixtureEventIDs(t, mappedEventIDs(mapped))
}

func mappedEventIDs(mapped []MappedEvent) map[string]string {
	out := map[string]string{}
	for _, item := range mapped {
		out[item.Event.Event.Action] = item.Event.Event.ID
	}
	return out
}

func assertFixtureEventIDs(t *testing.T, got map[string]string) {
	t.Helper()
	if len(got) != len(fixtureEventIDs) {
		t.Fatalf("event ids = %v, want %v", got, fixtureEventIDs)
	}
	for action, want := range fixtureEventIDs {
		if got[action] != want {
			t.Errorf("%s id = %q, want %q", action, got[action], want)
		}
	}
}

// Keying by home renames every event, and two homes never share a keyed id, while the unkeyed ids
// are the ones MapSession has always derived.
func TestMapSessionKeysEventIDsByHome(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	createHermesFixtureDB(t, dbPath)
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions, err := store.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %v, %v", sessions, err)
	}
	messages, err := store.ReadMessages(sessions[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	unkeyed, _ := mapSession(sessions[0], messages, &Cursor{}, "")
	assertFixtureEventIDs(t, mappedEventIDs(unkeyed))

	seen := map[string]string{}
	for _, item := range unkeyed {
		seen[item.Event.Event.ID] = "unkeyed"
	}
	for _, home := range []string{"/home/a/.hermes", "/home/a/.hermes/profiles/work"} {
		keyed, _ := mapSession(sessions[0], messages, &Cursor{}, home)
		if len(keyed) != len(unkeyed) {
			t.Fatalf("keyed by %s mapped %d events, want %d", home, len(keyed), len(unkeyed))
		}
		for i, item := range keyed {
			if item.Event.Event.Action != unkeyed[i].Event.Event.Action {
				t.Fatalf("keyed by %s event %d is %s, want %s", home, i, item.Event.Event.Action, unkeyed[i].Event.Event.Action)
			}
			if other, dup := seen[item.Event.Event.ID]; dup {
				t.Fatalf("%s id %s keyed by %s repeats one keyed by %s", item.Event.Event.Action, item.Event.Event.ID, home, other)
			}
			seen[item.Event.Event.ID] = home
		}
		again, _ := mapSession(sessions[0], messages, &Cursor{}, home)
		for i := range again {
			if again[i].Event.Event.ID != keyed[i].Event.Event.ID {
				t.Fatalf("keyed ids for %s are not deterministic", home)
			}
		}
	}
}

// A cursor marked KeyedByHome keeps keying the session's ids by its home on every sweep, so events
// written later in the session never fall back to the unkeyed ids another home writes.
func TestCollectOnceKeysEventIDsForACursorMarkedKeyed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	statePath := filepath.Join(dir, "hermes-state.json")
	state := &State{Version: StateVersion, Sessions: map[string]*Cursor{"hermes-session-1": {KeyedByHome: true}}}
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "runtime.jsonl")
	if _, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range readEvents(t, logPath) {
		if ev.Event.ID == fixtureEventIDs[ev.Event.Action] {
			t.Fatalf("%s kept the unkeyed id %s", ev.Event.Action, ev.Event.ID)
		}
	}
	saved, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if cursor := saved.Sessions["hermes-session-1"]; cursor == nil || !cursor.KeyedByHome || !cursor.Started {
		t.Fatalf("cursor = %#v, want it collected and still keyed by home", cursor)
	}
}
