package claudesession

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// backfillFixture writes sessions Claude sessions of one prompt each, far more output than a
// rotation window of a few kilobytes holds, so a sweep over them has to confront the window.
func backfillFixture(t *testing.T, sessions int) (projects, statePath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	projects = filepath.Join(dir, "projects")
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		writeFile(t, filepath.Join(projects, "-tmp-repo", id+".jsonl"), userLine(id, "u-"+id, "prompt "+id)+"\n")
	}
	return projects, filepath.Join(dir, "state", "claude.json"), filepath.Join(dir, "logs", "runtime.jsonl")
}

// retainedEvents reads every file the rotation contract keeps for logPath.
func retainedEvents(t *testing.T, logPath string) []schema.Event {
	t.Helper()
	var out []schema.Event
	for _, p := range writer.RetainedLogPaths(logPath) {
		out = append(out, readLog(t, p)...)
	}
	return out
}

// shipAway moves the retained log files aside, standing in for a forwarder that has shipped them,
// and returns what they held.
func shipAway(t *testing.T, logPath string, batch int) []schema.Event {
	t.Helper()
	events := retainedEvents(t, logPath)
	for _, p := range writer.RetainedLogPaths(logPath) {
		if err := os.Rename(p, p+".shipped"+strconv.Itoa(batch)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return events
}

const smallWindow = 4096

// #619: a backfill larger than the rotation window used to rotate its own output away and report
// errors: 0. The sweep now stops before the rotation that would discard what it wrote, says so,
// and every event it reports as emitted is still on disk.
func TestCollectOnceStopsAtTheRotationWindowInsteadOfDiscardingItsOwnOutput(t *testing.T) {
	projects, statePath, logPath := backfillFixture(t, 60)
	opts := CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, RotateBytes: smallWindow}

	summary, err := CollectOnce(opts)
	if !errors.Is(err, writer.ErrRetentionWindowFull) {
		t.Fatalf("CollectOnce err = %v, want ErrRetentionWindowFull", err)
	}
	if !summary.RetentionLimited {
		t.Error("summary does not report that the window stopped the sweep")
	}
	if summary.SessionsPending == 0 || summary.SessionsPending >= summary.Sessions {
		t.Errorf("SessionsPending = %d of %d, want some but not all", summary.SessionsPending, summary.Sessions)
	}
	if summary.Errors != 0 {
		t.Errorf("Errors = %d, want 0: stopping at the window is not a failed session", summary.Errors)
	}
	onDisk := len(retainedEvents(t, logPath))
	if summary.EventsEmitted == 0 || summary.EventsRetained != summary.EventsEmitted || onDisk != summary.EventsEmitted {
		t.Fatalf("emitted=%d retained=%d on disk=%d, want all equal and non-zero",
			summary.EventsEmitted, summary.EventsRetained, onDisk)
	}
	if summary.EventsRotatedOut != 0 {
		t.Errorf("EventsRotatedOut = %d, want 0", summary.EventsRotatedOut)
	}
}

// The cursor must not run ahead of what was written: sessions the sweep stopped before stay
// pending, so repeated sweeps (with the log shipped in between) collect every prompt exactly once.
func TestCollectOnceLeavesPendingSessionsForTheNextSweep(t *testing.T) {
	const sessions = 60
	projects, statePath, logPath := backfillFixture(t, sessions)
	opts := CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, RotateBytes: smallWindow}

	seen := map[string]int{}
	limited := 0
	for batch := 0; batch < 20; batch++ {
		summary, err := CollectOnce(opts)
		if err != nil && !errors.Is(err, writer.ErrRetentionWindowFull) {
			t.Fatalf("sweep %d: %v", batch, err)
		}
		for _, ev := range shipAway(t, logPath, batch) {
			if ev.Event.Action == "prompt.submitted" && ev.Session != nil {
				seen[ev.Session.ID]++
			}
		}
		if !summary.RetentionLimited {
			break
		}
		limited++
	}
	if limited == 0 {
		t.Fatal("fixture never filled the window; the test proves nothing")
	}
	if len(seen) != sessions {
		t.Errorf("collected prompts from %d sessions, want %d", len(seen), sessions)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("session %s prompt collected %d times, want 1", id, n)
		}
	}
}

// A window large enough for the backfill is the supported way to do it in one run.
func TestCollectOnceWithALargeEnoughWindowCollectsEverythingInOneSweep(t *testing.T) {
	projects, statePath, logPath := backfillFixture(t, 60)
	opts := CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, RotateBytes: 1 << 20}

	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	if summary.RetentionLimited || summary.SessionsPending != 0 {
		t.Errorf("summary = %+v, want no retention limit", summary)
	}
	if summary.SessionsChanged != 60 {
		t.Errorf("SessionsChanged = %d, want 60", summary.SessionsChanged)
	}
	if onDisk := len(retainedEvents(t, logPath)); summary.EventsRetained != summary.EventsEmitted || onDisk != summary.EventsEmitted {
		t.Errorf("emitted=%d retained=%d on disk=%d, want all equal", summary.EventsEmitted, summary.EventsRetained, onDisk)
	}
}

// --print writes nothing, so it has nothing to retain and no window to fill.
func TestCollectOncePrintIsNotRetentionLimited(t *testing.T) {
	projects, _, _ := backfillFixture(t, 60)
	summary, err := CollectOnce(CollectOptions{ProjectsDir: projects, Print: true, RotateBytes: smallWindow})
	if err != nil {
		t.Fatalf("CollectOnce --print: %v", err)
	}
	if summary.RetentionLimited || summary.EventsRetained != 0 || summary.SessionsChanged != 60 {
		t.Errorf("summary = %+v, want every session printed and nothing retained", summary)
	}
}

// Pending means records left to read. Sessions later in the sweep that were already collected have
// none, so a stop in one growing session must not report the whole history as pending.
func TestCollectOncePendingCountsOnlySessionsWithRecordsLeft(t *testing.T) {
	projects, statePath, logPath := backfillFixture(t, 60)
	full := CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, RotateBytes: 1 << 20}
	if _, err := CollectOnce(full); err != nil {
		t.Fatalf("initial sweep: %v", err)
	}

	// Grow one session far past the small window and make it the oldest, so the sweep reaches it
	// first and stops inside it with 59 already-collected sessions after it.
	grown := filepath.Join(projects, "-tmp-repo", "sess-000.jsonl")
	body := userLine("sess-000", "u-sess-000", "prompt sess-000") + "\n"
	for i := 0; i < 200; i++ {
		body += userLine("sess-000", fmt.Sprintf("u-more-%d", i), fmt.Sprintf("more %d", i)) + "\n"
	}
	writeFile(t, grown, body)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(grown, old, old); err != nil {
		t.Fatal(err)
	}

	summary, err := CollectOnce(CollectOptions{ProjectsDir: projects, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, RotateBytes: smallWindow})
	if !errors.Is(err, writer.ErrRetentionWindowFull) {
		t.Fatalf("err = %v, want ErrRetentionWindowFull", err)
	}
	if summary.SessionsPending != 1 {
		t.Errorf("SessionsPending = %d, want 1 (only the grown session has records left)", summary.SessionsPending)
	}
}
