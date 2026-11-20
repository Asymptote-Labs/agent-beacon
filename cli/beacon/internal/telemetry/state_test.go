package telemetry

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateStateSerializesWriters(t *testing.T) {
	home := t.TempDir()
	const writers = 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each writer reads the state, adds one character and saves it: an update made from a
			// copy read without the lock would overwrite another's and lose a character.
			if !updateState(home, func(st *state) bool {
				st.Company += "x"
				return true
			}) {
				t.Error("an update could not take the lock")
			}
		}()
	}
	wg.Wait()
	if got := loadState(home).Company; got != strings.Repeat("x", writers) {
		t.Fatalf("company = %q after %d updates; an update was lost", got, writers)
	}
}

func TestUpdateStateKeepsAnotherWritersFields(t *testing.T) {
	home := t.TempDir()
	// The inventory job saving its position must not wipe the identity a sign-in saved meanwhile.
	updateState(home, func(st *state) bool { st.PHDistinctID = "person"; return true })
	updateState(home, func(st *state) bool { st.LiveTrace = &LiveTracePosition{Offset: 10}; return true })
	st := loadState(home)
	if st.PHDistinctID != "person" || st.LiveTrace == nil || st.LiveTrace.Offset != 10 {
		t.Fatalf("state = %+v", st)
	}
}

func TestUpdateStateSkipsTheWriteWhenTheLockIsHeld(t *testing.T) {
	home := t.TempDir()
	updateState(home, func(st *state) bool { st.PHDistinctID = "person"; return true })
	unlock, ok := lockHomeFile(home, stateLockPath)
	if !ok {
		t.Fatal("could not take the lock")
	}
	defer unlock()
	old := lockWait
	lockWait = 50 * time.Millisecond
	t.Cleanup(func() { lockWait = old })

	if updateState(home, func(st *state) bool { st.PHDistinctID = ""; st.Company = "clobbered"; return true }) {
		t.Fatal("an update went ahead without the lock")
	}
	if st := loadState(home); st.PHDistinctID != "person" || st.Company != "" {
		t.Fatalf("state changed without the lock: %+v", st)
	}
}
