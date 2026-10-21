package hermessession

import (
	"path/filepath"
	"testing"
)

func TestCollectOnceIdentifiesTheStoreWithoutChangingEventIDs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	logPath := filepath.Join(dir, "runtime.jsonl")
	if _, err := CollectOnce(CollectOptions{DBPath: dbPath, Write: true, LogPath: logPath}); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.ReadMessages(sessions[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := MapSession(sessions[0], messages, &Cursor{})
	events := readEvents(t, logPath)
	if len(events) != len(mapped) {
		t.Fatalf("emitted %d events, mapped %d", len(events), len(mapped))
	}
	for i, event := range events {
		if event.Harness.ConfigPath != store.Path() || event.Event.ID != mapped[i].Event.Event.ID {
			t.Errorf("%s: store=%q id=%q, want store=%q id=%q", event.Event.Action, event.Harness.ConfigPath, event.Event.ID, store.Path(), mapped[i].Event.Event.ID)
		}
	}
}
