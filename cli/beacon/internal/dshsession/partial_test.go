package dshsession

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestAdvanceCursorPartialNeverSkipsALinesRemainingEvents(t *testing.T) {
	// Lines 2 and 3 map to two events each, as an assistant message's text and usage do.
	mapped := []MappedEvent{{SourceLine: 1}, {SourceLine: 2}, {SourceLine: 2}, {SourceLine: 3}, {SourceLine: 3}}
	for _, tc := range []struct {
		failed, start, want int
	}{
		{failed: 0, start: 0, want: 0}, // nothing written
		{failed: 1, start: 0, want: 1}, // line 1 done, line 2 retried
		{failed: 2, start: 0, want: 1}, // half of line 2 written: retry line 2
		{failed: 3, start: 0, want: 2},
		{failed: 4, start: 0, want: 2}, // half of line 3 written: retry line 3
		{failed: 2, start: 5, want: 5}, // never moves the cursor backwards
	} {
		cursor := &Cursor{LastLine: tc.start}
		advanceCursorPartial(cursor, mapped, tc.failed)
		if cursor.LastLine != tc.want {
			t.Fatalf("failure at %d from %d: LastLine = %d, want %d", tc.failed, tc.start, cursor.LastLine, tc.want)
		}
	}
}

// A sweep whose write fails partway through one line's events retries that line, so the events
// after the failure are not lost.
func TestCollectOnceRetriesALineThatFailedPartway(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "sessions", "s1"), SessionFileJSON,
		record("session", map[string]interface{}{"id": "s1", "cwd": "/repo"}),
		record("assistant/message", map[string]interface{}{"message": map[string]interface{}{
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "done"}},
			"usage":   map[string]interface{}{"inputTokens": 11, "outputTokens": 7},
		}}),
	)
	var out bytes.Buffer
	opts := CollectOptions{DSHHome: root, StatePath: filepath.Join(t.TempDir(), "state.json"), Print: true, Out: &out}

	prev := emitEvent
	t.Cleanup(func() { emitEvent = prev })
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if event.Event.Action == "token.usage" {
			return errors.New("disk full")
		}
		return prev(event, opts)
	}
	if _, err := CollectOnce(opts); err == nil {
		t.Fatal("the failed write must surface")
	}
	if !strings.Contains(out.String(), `"agent.message"`) {
		t.Fatalf("the first sweep should have written the message before failing:\n%s", out.String())
	}
	emitEvent = prev
	out.Reset()
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := strings.Count(out.String(), `"token.usage"`); n != 1 {
		t.Fatalf("token.usage events after the retry = %d, want 1:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), `"session.started"`) {
		t.Fatalf("the retry must not repeat the session start it already wrote:\n%s", out.String())
	}
}
