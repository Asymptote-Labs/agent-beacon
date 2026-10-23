// Package sessionsynctest checks that a session collector's sweep keeps what it writes: a sweep
// larger than the runtime log's rotation window stops before rotating its own output away, and
// repeated sweeps, with the log shipped in between, collect exactly the events one sweep with room
// for everything collects (#619, #973).
package sessionsynctest

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
)

// SmallWindow is a rotation size a few dozen fixture sessions overflow many times over.
const SmallWindow = 4096

// roomyWindow holds any fixture in one sweep.
const roomyWindow = 1 << 30

// Result is what one sweep reported.
type Result struct {
	EventsEmitted int
	Errors        int
	sessionsync.Retention
}

// Sweep runs one write sweep over the fixture store with its cursor file at statePath, appending to
// logPath with the runtime log rotating at rotateBytes.
type Sweep func(statePath, logPath string, rotateBytes int64) (Result, error)

// RetainedEvents reads every file the rotation contract keeps for logPath.
func RetainedEvents(t *testing.T, logPath string) []schema.Event {
	t.Helper()
	var out []schema.Event
	for _, p := range writer.RetainedLogPaths(logPath) {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1<<20), 1<<24)
		for scanner.Scan() {
			if len(scanner.Bytes()) == 0 {
				continue
			}
			var ev schema.Event
			if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
				f.Close()
				t.Fatalf("decode %s: %v", p, err)
			}
			out = append(out, ev)
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// shipAway moves the retained log files aside, standing in for a forwarder that has shipped them,
// and returns what they held.
func shipAway(t *testing.T, logPath string, batch int) []schema.Event {
	t.Helper()
	events := RetainedEvents(t, logPath)
	for _, p := range writer.RetainedLogPaths(logPath) {
		if err := os.Rename(p, p+".shipped"+strconv.Itoa(batch)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return events
}

// CheckSweepKeepsItsOutput runs sweep over a fixture store too large for SmallWindow and checks
// that:
//   - the first sweep at SmallWindow stops with writer.ErrRetentionWindowFull, reports it, counts
//     no session as failed, and every event it reports emitted is still on disk;
//   - repeated sweeps at SmallWindow, with the log shipped away in between, end up with exactly the
//     event ids one sweep with room for everything writes. A sweep that stops inside a source
//     record may write that record's events again on the next one, under the same deterministic
//     ids the forwarder and ingest deduplicate on; an event lost, or written again under a
//     different id, fails.
func CheckSweepKeepsItsOutput(t *testing.T, sweep Sweep) {
	t.Helper()
	CheckSweepKeepsItsOutputBy(t, EventID, sweep)
}

// EventID identifies an event by its deterministic event.id.
func EventID(ev schema.Event) string { return ev.Event.ID }

// IgnoringTimestamp identifies an event by everything but its timestamp and the id derived from
// it, for a collector whose records carry no time of their own, so each sweep stamps its events
// with the time it read them.
func IgnoringTimestamp(ev schema.Event) string {
	ev.Timestamp = ""
	ev.Event.ID = ""
	data, err := json.Marshal(ev)
	if err != nil {
		return ""
	}
	return string(data)
}

// CheckSweepKeepsItsOutputBy is CheckSweepKeepsItsOutput comparing events by key.
func CheckSweepKeepsItsOutputBy(t *testing.T, key func(schema.Event) string, sweep Sweep) {
	t.Helper()
	dir := t.TempDir()

	refLog := filepath.Join(dir, "reference", "runtime.jsonl")
	ref, err := sweep(filepath.Join(dir, "reference", "state.json"), refLog, roomyWindow)
	if err != nil {
		t.Fatalf("reference sweep: %v", err)
	}
	want := map[string]bool{}
	for _, ev := range RetainedEvents(t, refLog) {
		want[key(ev)] = true
	}
	if ref.RetentionLimited || len(want) == 0 || len(want) != ref.EventsEmitted {
		t.Fatalf("reference sweep: emitted=%d distinct events=%d limited=%v", ref.EventsEmitted, len(want), ref.RetentionLimited)
	}

	statePath := filepath.Join(dir, "limited", "state.json")
	logPath := filepath.Join(dir, "limited", "runtime.jsonl")
	got := map[string]bool{}
	limited := 0
	for batch := 0; batch < 200; batch++ {
		result, err := sweep(statePath, logPath, SmallWindow)
		if err != nil && !errors.Is(err, writer.ErrRetentionWindowFull) {
			t.Fatalf("sweep %d: %v", batch, err)
		}
		if result.RetentionLimited != errors.Is(err, writer.ErrRetentionWindowFull) {
			t.Fatalf("sweep %d: RetentionLimited = %v with err %v", batch, result.RetentionLimited, err)
		}
		if result.Errors != 0 {
			t.Fatalf("sweep %d: Errors = %d, want 0: stopping at the window is not a failed session", batch, result.Errors)
		}
		if result.EventsRotatedOut != 0 {
			t.Fatalf("sweep %d: EventsRotatedOut = %d, want 0", batch, result.EventsRotatedOut)
		}
		shipped := shipAway(t, logPath, batch)
		if result.EventsRetained != result.EventsEmitted || len(shipped) != result.EventsEmitted {
			t.Fatalf("sweep %d: emitted=%d retained=%d on disk=%d, want all equal",
				batch, result.EventsEmitted, result.EventsRetained, len(shipped))
		}
		for _, ev := range shipped {
			got[key(ev)] = true
		}
		if batch == 0 && !result.RetentionLimited {
			t.Fatal("the first sweep did not fill the window; the fixture proves nothing")
		}
		if !result.RetentionLimited {
			break
		}
		limited++
		if result.EventsEmitted == 0 {
			t.Fatalf("sweep %d stopped at the window without writing anything; it can never finish", batch)
		}
	}
	if limited == 0 {
		t.Fatal("no sweep was retention limited")
	}
	var missing, extra []string
	for id := range want {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	for id := range got {
		if !want[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("%d limited sweeps collected %d distinct events, one sweep %d: %d missing %v, %d under ids one sweep never writes %v",
			limited+1, len(got), len(want), len(missing), head(missing), len(extra), head(extra))
	}
}

func head(ids []string) []string {
	if len(ids) > 5 {
		return ids[:5]
	}
	return ids
}
