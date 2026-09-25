package codexsession

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestAdvanceCursorPartialNeverSkipsARecordsRemainingEvents(t *testing.T) {
	// Lines 2 and 3 map to two events each: a prompt and its session.handoff link.
	mapped := []MappedEvent{{SourceLine: 1}, {SourceLine: 2}, {SourceLine: 2}, {SourceLine: 3}, {SourceLine: 3}}
	for _, tc := range []struct {
		failed, start, want int
	}{
		{failed: 0, start: 0, want: 0}, // nothing written
		{failed: 1, start: 0, want: 1}, // line 1 done, line 2 retried
		{failed: 2, start: 0, want: 1}, // line 2's prompt written, its link failed: retry line 2
		{failed: 3, start: 0, want: 2},
		{failed: 4, start: 0, want: 2}, // line 3's prompt written, its link failed: retry line 3
		{failed: 2, start: 5, want: 5}, // never moves the cursor backwards
	} {
		cursor := &Cursor{LastLine: tc.start}
		advanceCursorPartial(cursor, mapped, tc.failed)
		if cursor.LastLine != tc.want {
			t.Fatalf("failure at %d from %d: LastLine = %d, want %d", tc.failed, tc.start, cursor.LastLine, tc.want)
		}
	}
}

// A sync whose session.handoff write fails after the prompt from the same rollout line was written
// retries that line, so the link is not lost.
func TestCollectOnceRetriesALineWhoseLinkFailedToWrite(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	writeFile(t, filepath.Join(codexDir, "sessions", "2026", "09", "25", "rollout-2026-09-25T09-00-00-new-1.jsonl"),
		sessionMetaLine("new-1", "/tmp/repo")+"\n"+messageLine("user", "u1", prompt)+"\n")
	opts := CollectOptions{CodexDir: codexDir, StatePath: filepath.Join(dir, "state.json"), LogPath: filepath.Join(dir, "runtime.jsonl"), Write: true, UserMode: true}

	prev := emitEvent
	t.Cleanup(func() { emitEvent = prev })
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if event.Event.Action == "session.handoff" {
			return errors.New("disk full")
		}
		return prev(event, opts)
	}
	if _, err := CollectOnce(opts); err == nil {
		t.Fatal("the failed link write must surface")
	}
	emitEvent = prev
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("retry: %v", err)
	}
	data, err := os.ReadFile(opts.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"session.handoff"`); n != 1 {
		t.Fatalf("session.handoff events after the retry = %d, want 1:\n%s", n, data)
	}
}
