package hermessession

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// hermesLongSession is the fixture database with its one session grown by turns turns of a prompt,
// an assistant message calling a tool, and the tool's result, so a sweep that stops between a call
// and its result has a call left open.
func hermesLongSession(t *testing.T, turns int) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	createHermesFixtureDB(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < turns; i++ {
		ts := 1780632840.0 + float64(i)
		callID := fmt.Sprintf("call_%03d", i)
		args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("echo %03d", i)})
		calls, _ := json.Marshal([]map[string]interface{}{{"id": callID, "function": map[string]string{"name": "terminal", "arguments": string(args)}}})
		for _, stmt := range []struct {
			query string
			args  []interface{}
		}{
			{`INSERT INTO messages (session_id, role, content, timestamp, active) VALUES ('hermes-session-1', 'user', ?, ?, 1)`, []interface{}{fmt.Sprintf("question %03d", i), ts}},
			{`INSERT INTO messages (session_id, role, content, tool_calls, timestamp, active) VALUES ('hermes-session-1', 'assistant', ?, ?, ?, 1)`, []interface{}{fmt.Sprintf("answer %03d", i), string(calls), ts + 0.25}},
			{`INSERT INTO messages (session_id, role, tool_call_id, tool_name, content, timestamp, active) VALUES ('hermes-session-1', 'tool', ?, 'terminal', ?, ?, 1)`, []interface{}{callID, fmt.Sprintf(`{"output":"%03d","exit_code":0}`, i), ts + 0.5}},
		} {
			if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dbPath
}

// eventsByID reads the runtime log at path into each event's JSON by event id.
func eventsByID(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, ev := range readEvents(t, path) {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		out[ev.Event.ID] = string(data)
	}
	return out
}

// A session larger than one sweep's budget is collected across sweeps: each one writes what fits,
// moves the cursor past the messages it wrote in full, and the next resumes there, until the log
// holds exactly what one sweep without a budget writes, tool results included, whose command comes
// from the call a stopped sweep left open (#1360).
func TestCollectOnceFinishesASessionLargerThanOneBudget(t *testing.T) {
	dbPath := hermesLongSession(t, 80)
	refDir := t.TempDir()
	refLog := filepath.Join(refDir, "runtime.jsonl")
	if _, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: filepath.Join(refDir, "state.json"), Write: true, UserMode: true, LogPath: refLog}); err != nil {
		t.Fatal(err)
	}
	want := eventsByID(t, refLog)

	// Different budgets stop the sweeps at different events: between a call and its result, inside
	// a message's events, before the session's token usage and before its end.
	for _, budget := range []int64{3 << 10, 5 << 10, 8 << 10, 13 << 10, 21 << 10} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "runtime.jsonl")
			statePath := filepath.Join(dir, "state.json")
			sweeps := 0
			for {
				sweeps++
				if sweeps > 200 {
					t.Fatalf("the session was not collected after %d sweeps", sweeps-1)
				}
				summary, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, Write: true, UserMode: true, LogPath: logPath, Budget: writer.NewBudget(budget)})
				if err == nil {
					break
				}
				if !errors.Is(err, writer.ErrBudgetSpent) {
					t.Fatalf("sweep %d: %v", sweeps, err)
				}
				if summary.EventsEmitted == 0 {
					t.Fatalf("sweep %d stopped without writing anything", sweeps)
				}
			}
			got := eventsByID(t, logPath)
			if len(got) != len(want) {
				t.Errorf("%d sweeps collected %d distinct events, one sweep without a budget writes %d", sweeps, len(got), len(want))
			}
			for id, ev := range want {
				if got[id] != ev {
					t.Errorf("event %s:\n got  %s\n want %s", id, got[id], ev)
				}
			}
			for id := range got {
				if _, ok := want[id]; !ok {
					t.Errorf("event %s is not one a sweep without a budget writes: %s", id, got[id])
				}
			}
			// Nothing is left: a further sweep writes nothing.
			summary, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, Write: true, UserMode: true, LogPath: logPath, Budget: writer.NewBudget(budget)})
			if err != nil || summary.EventsEmitted != 0 {
				t.Errorf("sweep after the session was collected: emitted %d, err %v", summary.EventsEmitted, err)
			}
		})
	}
}

// A budget too small for the session's first event writes nothing, and moves nothing either.
func TestCollectOnceMovesNoCursorWhenTheBudgetFitsNoEvent(t *testing.T) {
	dbPath := hermesLongSession(t, 3)
	statePath := filepath.Join(t.TempDir(), "state.json")
	_, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, Write: true, UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl"), Budget: writer.NewBudget(1)})
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("err = %v, want writer.ErrBudgetSpent", err)
	}
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if cursor := state.Sessions["hermes-session-1"]; cursor != nil && (cursor.Started || cursor.LastMessageID != 0 || len(cursor.PendingCalls) != 0 || cursor.InputTokens != 0) {
		t.Fatalf("cursor after a sweep that wrote nothing = %+v", cursor)
	}
}

// One session larger than the rotation window: the retention guard stops each sweep, and repeated
// sweeps still end with exactly what one sweep writes (#1360). The 60-session retention test passes
// with sessions of a few events each.
func TestCollectOnceKeepsOneSessionLargerThanTheRotationWindow(t *testing.T) {
	opts := CollectOptions{DBPath: hermesLongSession(t, 80), Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}

// A budgeted sweep may stop after any event, and the sweep after it must write exactly what one
// sweep without a budget does, timestamps included. Every stopping point is tried, for a session
// that has ended and for one still running: stopping just before session.ended must not lose it,
// and stopping just before a running session's token usage must not move that event to the
// session's start (#1361 review).
func TestCollectOnceResumesFromEveryStoppingPoint(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live=%v", live), func(t *testing.T) {
			dbPath := hermesLongSession(t, 3)
			if live {
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE sessions SET ended_at = NULL, end_reason = NULL`); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			refDir := t.TempDir()
			refLog := filepath.Join(refDir, "runtime.jsonl")
			if _, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: filepath.Join(refDir, "state.json"), Write: true, UserMode: true, LogPath: refLog}); err != nil {
				t.Fatal(err)
			}
			want := eventsByID(t, refLog)
			// The bytes each event takes on its own line, in the order one sweep writes them.
			var sizes []int64
			for _, ev := range readEvents(t, refLog) {
				sizes = append(sizes, int64(len(want[ev.Event.ID]))+1)
			}
			var budget int64
			for stop := 0; stop < len(sizes); stop++ {
				dir := t.TempDir()
				logPath := filepath.Join(dir, "runtime.jsonl")
				statePath := filepath.Join(dir, "state.json")
				_, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, Write: true, UserMode: true, LogPath: logPath, Budget: writer.NewBudget(budget)})
				if !errors.Is(err, writer.ErrBudgetSpent) {
					t.Fatalf("stop after %d events: err = %v, want writer.ErrBudgetSpent", stop, err)
				}
				if _, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, Write: true, UserMode: true, LogPath: logPath}); err != nil {
					t.Fatalf("stop after %d events, then a sweep without a budget: %v", stop, err)
				}
				got := eventsByID(t, logPath)
				for id, ev := range want {
					if got[id] != ev {
						t.Errorf("stop after %d events: event %s\n got  %s\n want %s", stop, id, got[id], ev)
					}
				}
				if len(got) != len(want) {
					t.Errorf("stop after %d events: %d distinct events, want %d", stop, len(got), len(want))
				}
				budget += sizes[stop]
			}
		})
	}
}
