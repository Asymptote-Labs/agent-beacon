package factorysession

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestAdvanceCursorPartialNeverSkipsALinesRemainingEvents(t *testing.T) {
	// Lines 2 and 3 map to two events each, as a message with two text blocks does.
	mapped := []MappedEvent{{SourceLine: 1}, {SourceLine: 2}, {SourceLine: 2}, {SourceLine: 3}, {SourceLine: 3}}
	for _, tc := range []struct {
		failed, start, want int
	}{
		{failed: 0, start: 0, want: 0},
		{failed: 1, start: 0, want: 1},
		{failed: 2, start: 0, want: 1}, // half of line 2 written: retry line 2
		{failed: 3, start: 0, want: 2},
		{failed: 4, start: 0, want: 2},
		{failed: 2, start: 5, want: 5}, // never moves the cursor backwards
	} {
		cursor := &Cursor{LastLine: tc.start}
		advanceCursorPartial(cursor, SessionRef{Path: "/s.jsonl"}, mapped, tc.failed)
		if cursor.LastLine != tc.want {
			t.Fatalf("failure at %d from %d: LastLine = %d, want %d", tc.failed, tc.start, cursor.LastLine, tc.want)
		}
	}
}

// A sweep whose write fails partway through one line's events retries that line, so the events
// after the failure are not lost.
func TestCollectOnceRetriesALineThatFailedPartway(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(project, "session-1.jsonl"),
		`{"type":"session_start","id":"session-1","cwd":"/repo"}`+"\n"+
			`{"type":"message","id":"u1","timestamp":"2026-05-16T22:11:04.611Z","message":{"role":"user","content":[{"type":"text","text":"first block"},{"type":"text","text":"second block"}]}}`+"\n")
	var out bytes.Buffer
	opts := CollectOptions{SessionsDir: root, StatePath: filepath.Join(t.TempDir(), "state.json"), Print: true, Out: &out}

	prev := emitEvent
	t.Cleanup(func() { emitEvent = prev })
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if event.Prompt != nil && event.Prompt.Text == "second block" {
			return errors.New("disk full")
		}
		return prev(event, opts)
	}
	if _, err := CollectOnce(opts); err == nil {
		t.Fatal("the failed write must surface")
	}
	if !strings.Contains(out.String(), "first block") {
		t.Fatalf("the first sweep should have written the first block before failing:\n%s", out.String())
	}
	emitEvent = prev
	out.Reset()
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := strings.Count(out.String(), `"prompt":{"text":"second block"}`); n != 1 {
		t.Fatalf("the retry wrote the second block %d times, want 1:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), `"session.started"`) {
		t.Fatalf("the retry must not repeat the session start it already wrote:\n%s", out.String())
	}
}
