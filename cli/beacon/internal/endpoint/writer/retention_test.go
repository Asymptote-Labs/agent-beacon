package writer

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func retentionEvent(i int) schema.Event {
	return schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "retention event " + strconv.Itoa(i),
	})
}

// retainedText is everything the rotation contract keeps for path, for asserting which events
// survived.
func retainedText(t *testing.T, path string, archives int) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i <= archives; i++ {
		p := path
		if i > 0 {
			p = path + "." + strconv.Itoa(i)
		}
		data, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

// A guarded writer stops at the one append whose rotation would discard the file holding the
// guard's first event. Every file here holds one event (RotateSize 1), so with two archives the
// guard can place three events and the fourth is refused.
func TestRetentionGuardRefusesTheRotationThatWouldDiscardItsOwnOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	guard := &RetentionGuard{}
	opts := Options{Path: path, RotateSize: 1, RotateArchives: 2, Guard: guard}

	for i := 0; i < 3; i++ {
		if _, err := AppendEvent(retentionEvent(i), opts); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	before := retainedText(t, path, 2)

	_, err := AppendEvent(retentionEvent(3), opts)
	if !errors.Is(err, ErrRetentionWindowFull) {
		t.Fatalf("fourth AppendEvent err = %v, want ErrRetentionWindowFull", err)
	}
	after := retainedText(t, path, 2)
	if after != before {
		t.Fatalf("the refused append changed the log:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(after, "retention event "+strconv.Itoa(i)+`"`) {
			t.Errorf("event %d was rotated out of the window:\n%s", i, after)
		}
	}
	if strings.Contains(after, "retention event 3") {
		t.Errorf("the refused event was written anyway")
	}
	if got := guard.Written(); got != 3 {
		t.Errorf("Written() = %d, want 3", got)
	}
	if got := guard.Retained(); got != 3 {
		t.Errorf("Retained() = %d, want 3", got)
	}
}

// Rotating data that was already in the log before the guard wrote anything is ordinary rotation,
// not the guard's output being lost, so it is allowed and does not use up the window.
func TestRetentionGuardAllowsRotatingPreexistingDataBeforeItsFirstWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(path, []byte("pre-existing line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	guard := &RetentionGuard{}
	opts := Options{Path: path, RotateSize: 1, RotateArchives: 1, Guard: guard}

	for i := 0; i < 2; i++ {
		if _, err := AppendEvent(retentionEvent(i), opts); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	if _, err := AppendEvent(retentionEvent(2), opts); !errors.Is(err, ErrRetentionWindowFull) {
		t.Fatalf("third AppendEvent err = %v, want ErrRetentionWindowFull", err)
	}
	text := retainedText(t, path, 1)
	if !strings.Contains(text, "retention event 0") || !strings.Contains(text, "retention event 1") {
		t.Fatalf("guarded events missing from the window:\n%s", text)
	}
	if got := guard.Retained(); got != 2 {
		t.Errorf("Retained() = %d, want 2", got)
	}
}

// The guard cannot stop another process from rotating the log, but it must notice when that
// discarded what it wrote, so a caller never reports lost events as retained.
func TestRetentionGuardCountsEventsAnotherWriterRotatedOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	guard := &RetentionGuard{}
	for i := 0; i < 2; i++ {
		if _, err := AppendEvent(retentionEvent(i), Options{Path: path, RotateArchives: 2, Guard: guard}); err != nil {
			t.Fatalf("guarded AppendEvent %d: %v", i, err)
		}
	}
	other := Options{Path: path, RotateSize: 1, RotateArchives: 2}

	if _, err := AppendEvent(retentionEvent(100), other); err != nil {
		t.Fatal(err)
	}
	if got := guard.Retained(); got != 2 {
		t.Fatalf("after one foreign rotation Retained() = %d, want 2 (the events are in .1)", got)
	}
	for i := 101; i < 104; i++ {
		if _, err := AppendEvent(retentionEvent(i), other); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(retainedText(t, path, 2), "retention event 0") {
		t.Fatal("fixture did not rotate the guarded events out")
	}
	if got := guard.Retained(); got != 0 {
		t.Errorf("after the window rolled over Retained() = %d, want 0", got)
	}
	if got := guard.Written(); got != 2 {
		t.Errorf("Written() = %d, want 2", got)
	}
}

// An append the writer suppresses as a duplicate writes nothing, so it is neither written nor
// retained -- otherwise a caller would count it as lost.
func TestRetentionGuardDoesNotCountSuppressedDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "mcp.tool_invoked",
		Harness: schema.HarnessInfo{Name: "cursor"},
		Message: "Tool execution observed",
	})
	event.Timestamp = "2026-06-18T21:11:24Z"
	event.Session = &schema.SessionInfo{ID: "s1"}
	event.MCP = &schema.MCPInfo{Server: "clickhouse", Tool: "execute_sql"}
	duplicate := event
	duplicate.Harness.Name = "claude"
	duplicate.Timestamp = "2026-06-18T21:11:25Z"

	guard := &RetentionGuard{}
	for _, ev := range []schema.Event{event, duplicate} {
		if _, err := AppendEvent(ev, Options{Path: path, Guard: guard}); err != nil {
			t.Fatal(err)
		}
	}
	if w, r := guard.Written(), guard.Retained(); w != 1 || r != 1 {
		t.Fatalf("Written()=%d Retained()=%d, want 1 and 1", w, r)
	}
}

// A guard that never wrote has nothing to report and nothing to look at.
func TestRetentionGuardWithNoWritesRetainsNothing(t *testing.T) {
	guard := &RetentionGuard{}
	if w, r := guard.Written(), guard.Retained(); w != 0 || r != 0 {
		t.Fatalf("Written()=%d Retained()=%d, want 0 and 0", w, r)
	}
}

// Other writers take the log's lock between guarded appends and can rotate several times while a
// sweep reads its next session. The guard must decide from where its first file actually is now,
// not from how many files it has written into: here it has written one file, which another writer
// has already pushed to the last retained slot, so the guarded rotation would delete it.
func TestRetentionGuardRefusesWhenAnotherWriterMovedItsFirstFileToTheLastSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	guard := &RetentionGuard{}
	guarded := Options{Path: path, RotateSize: 1, RotateArchives: 2, Guard: guard}
	other := Options{Path: path, RotateSize: 1, RotateArchives: 2}

	if _, err := AppendEvent(retentionEvent(0), guarded); err != nil {
		t.Fatal(err)
	}
	for i := 100; i < 102; i++ {
		if _, err := AppendEvent(retentionEvent(i), other); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(path + ".2"); err != nil || !strings.Contains(string(data), "retention event 0\"") {
		t.Fatalf("fixture: guarded event is not at .2 (err=%v): %q", err, data)
	}

	if _, err := AppendEvent(retentionEvent(1), guarded); !errors.Is(err, ErrRetentionWindowFull) {
		t.Fatalf("guarded AppendEvent err = %v, want ErrRetentionWindowFull", err)
	}
	if !strings.Contains(retainedText(t, path, 2), "retention event 0\"") {
		t.Fatal("the guarded append rotated out the guard's own first event")
	}
	if got := guard.Retained(); got != 1 {
		t.Errorf("Retained() = %d, want 1", got)
	}
}

// Once another writer has rotated the guard's first file out entirely, there is nothing left of it
// to protect: the guard keeps writing, reports the loss, and protects what it wrote afterwards.
func TestRetentionGuardProtectsWhatSurvivesAfterAnotherWriterRotatedItsFirstFileOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	guard := &RetentionGuard{}
	guarded := Options{Path: path, RotateSize: 1, RotateArchives: 2, Guard: guard}
	other := Options{Path: path, RotateSize: 1, RotateArchives: 2}

	if _, err := AppendEvent(retentionEvent(0), guarded); err != nil {
		t.Fatal(err)
	}
	for i := 100; i < 103; i++ {
		if _, err := AppendEvent(retentionEvent(i), other); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AppendEvent(retentionEvent(1), guarded); err != nil {
		t.Fatalf("guarded append after the first file was already gone: %v", err)
	}
	if w, r := guard.Written(), guard.Retained(); w != 2 || r != 1 {
		t.Fatalf("Written()=%d Retained()=%d, want 2 and 1", w, r)
	}
	for i := 103; i < 105; i++ {
		if _, err := AppendEvent(retentionEvent(i), other); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AppendEvent(retentionEvent(2), guarded); !errors.Is(err, ErrRetentionWindowFull) {
		t.Fatalf("guarded AppendEvent err = %v, want ErrRetentionWindowFull (event 1 is at .2)", err)
	}
	if !strings.Contains(retainedText(t, path, 2), "retention event 1\"") {
		t.Fatal("the guarded append rotated out the guard's surviving event")
	}
}
