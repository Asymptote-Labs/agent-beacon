package harness2session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

type sweepEnv struct {
	root, statePath, logPath string
}

func newSweepEnv(t *testing.T) sweepEnv {
	t.Helper()
	// Keep the defaults off the real machine: an empty home and no HARNESS2_RUNS_DIR.
	testenv.SetHome(t, t.TempDir())
	t.Setenv("HARNESS2_RUNS_DIR", "")
	dir := t.TempDir()
	return sweepEnv{
		root:      copyRuns(t),
		statePath: filepath.Join(dir, "state", "harness2.json"),
		logPath:   filepath.Join(dir, "logs", "runtime.jsonl"),
	}
}

func (e sweepEnv) sweep(t *testing.T, mutate ...func(*CollectOptions)) Summary {
	t.Helper()
	opts := CollectOptions{RunsDirs: []string{e.root}, StatePath: e.statePath, LogPath: e.logPath, Write: true, UserMode: true}
	for _, m := range mutate {
		m(&opts)
	}
	summary, err := CollectOnce(opts)
	if err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	return summary
}

func (e sweepEnv) logged(t *testing.T) []schema.Event {
	t.Helper()
	return sessionsynctest.RetainedEvents(t, e.logPath)
}

func countBy(events []schema.Event, pred func(schema.Event) bool) int {
	n := 0
	for _, ev := range events {
		if pred(ev) {
			n++
		}
	}
	return n
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	// A growing file's size changes, but bump the time too so the test does not depend on the
	// filesystem's timestamp resolution.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestCollectOnceWritesEveryTrajectoryOnce(t *testing.T) {
	env := newSweepEnv(t)
	first := env.sweep(t)
	if first.Trajectories != 3 || first.FilesChanged != 3 || first.Errors != 0 || first.EventsEmitted != 21 {
		t.Fatalf("first sweep = %+v, want 3 trajectories and 21 events", first)
	}
	if first.MalformedLines != 2 {
		t.Fatalf("malformed = %d, want the WorkBuddy banner's two lines", first.MalformedLines)
	}
	events := env.logged(t)
	if len(events) != 21 {
		t.Fatalf("log holds %d events, want 21", len(events))
	}
	for _, ev := range events {
		if ev.Harness.Name != Harness || ev.Harness.CollectionMethod != schema.CollectionMethodPoll || ev.Event.ID == "" {
			t.Fatalf("logged event = %+v", ev.Harness)
		}
	}
	if got := countBy(events, func(ev schema.Event) bool { return ev.Event.Action == "session.started" }); got != 3 {
		t.Fatalf("session.started = %d, want one per trajectory", got)
	}

	second := env.sweep(t)
	if second.FilesChanged != 0 || second.EventsEmitted != 0 {
		t.Fatalf("second sweep = %+v, want nothing new", second)
	}
	if len(env.logged(t)) != 21 {
		t.Fatal("an unchanged runs directory wrote events again")
	}
}

// A LAB or WorkBuddy stream is written while the executor runs, so a sweep can see it half done. The
// next sweep writes only what was added, and a line still being written waits until it is whole.
func TestCollectOnceFollowsAGrowingStream(t *testing.T) {
	env := newSweepEnv(t)
	env.sweep(t)
	path := filepath.Join(env.root, filepath.FromSlash(labRel))

	appendLines(t, path,
		`{"type":"turn.started"}`+"\n",
		`{"type":"item.completed","item":{"id":"item_7","type":"agent_message","text":"Second turn."}}`+"\n",
		`{"type":"turn.completed","usage":{"input_tokens":8000,"cached_input_tokens":4500,"output_tokens":900,"reasoning_output_tokens":350}}`+"\n",
		`{"type":"item.completed","item":{"id":"item_8","type":"agent_mes`)
	summary := env.sweep(t)
	if summary.EventsEmitted != 2 || summary.FilesChanged != 1 || summary.PartialFiles != 1 {
		t.Fatalf("growth sweep = %+v, want the message and the turn's usage, with the partial line left", summary)
	}
	events := env.logged(t)
	last := events[len(events)-1]
	// The second turn's usage is its own, not the thread total.
	if last.GenAI == nil || last.GenAI.Usage == nil || *last.GenAI.Usage.InputTokens != 1500 || *last.GenAI.Usage.OutputTokens != 200 {
		t.Fatalf("second turn usage = %+v", last.GenAI)
	}

	appendLines(t, path, `sage","text":"Done."}}`+"\n")
	summary = env.sweep(t)
	if summary.EventsEmitted != 1 || summary.PartialFiles != 0 {
		t.Fatalf("completion sweep = %+v, want the finished line only", summary)
	}
	if got := countBy(env.logged(t), func(ev schema.Event) bool { return ev.Event.Action == "session.started" }); got != 3 {
		t.Fatalf("session.started = %d after growth, want it written once per trajectory", got)
	}
}

// Harness² re-rolls a run it cannot reuse into the same path. The new stream is a new session, read
// from its start, even though it is shorter than what the cursor had passed.
func TestCollectOnceReadsARerolledRunAsANewSession(t *testing.T) {
	env := newSweepEnv(t)
	env.sweep(t)
	path := filepath.Join(env.root, filepath.FromSlash(labRel))
	body := `{"type":"thread.started","thread_id":"rerolled-thread"}` + "\n" +
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Again."}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(path, later, later)
	summary := env.sweep(t)
	if summary.EventsEmitted != 2 {
		t.Fatalf("re-roll sweep = %+v, want session.started and the message", summary)
	}
	var rerolled []string
	for _, ev := range env.logged(t) {
		if ev.Session != nil && ev.Session.ID == "rerolled-thread" {
			rerolled = append(rerolled, ev.Event.Action)
		}
	}
	if strings.Join(rerolled, ",") != "session.started,agent.message" {
		t.Fatalf("re-rolled session events = %v", rerolled)
	}
}

// --print is a dry run in both directions: nothing is written and no cursor moves.
func TestCollectOncePrintWritesNothing(t *testing.T) {
	env := newSweepEnv(t)
	var out bytes.Buffer
	summary := env.sweep(t, func(o *CollectOptions) { o.Print, o.Write, o.Out = true, false, &out })
	if summary.EventsEmitted != 21 {
		t.Fatalf("print sweep = %+v", summary)
	}
	if lines := strings.Count(out.String(), "\n"); lines != 21 {
		t.Fatalf("printed %d lines, want 21", lines)
	}
	if _, err := os.Stat(env.statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("print wrote a cursor file (err=%v)", err)
	}
	if _, err := os.Stat(env.logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("print wrote the runtime log (err=%v)", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var ev schema.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Harness.Name != Harness {
			t.Fatalf("printed line %q: %v", line, err)
		}
	}
}

// A checkout Beacon would not find by itself is named once; scheduled syncs and the install
// backfill, which name no directory, keep reading it.
func TestCollectOnceRemembersTheRunsDirectoriesItWasGiven(t *testing.T) {
	env := newSweepEnv(t)
	env.sweep(t, func(o *CollectOptions) { o.Write, o.Print = true, false })
	state, err := LoadState(env.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Roots) != 1 || state.Roots[0] != env.root {
		t.Fatalf("remembered roots = %v, want [%s]", state.Roots, env.root)
	}
	appendLines(t, filepath.Join(env.root, filepath.FromSlash(labRel)),
		`{"type":"item.completed","item":{"id":"item_9","type":"agent_message","text":"later"}}`+"\n")
	summary := env.sweep(t, func(o *CollectOptions) { o.RunsDirs = nil })
	if summary.EventsEmitted != 1 || len(summary.Roots) != 2 {
		t.Fatalf("sweep without a directory = %+v, want the remembered root read beside the default", summary)
	}
}

// The install backfill reads only recent trajectories, newest first, and leaves the rest -- cursors
// untouched -- for a manual sync.
func TestCollectOnceSinceLeavesOlderTrajectories(t *testing.T) {
	env := newSweepEnv(t)
	old := time.Now().Add(-90 * 24 * time.Hour)
	for _, rel := range []string{jbRel, wbRel} {
		if err := os.Chtimes(filepath.Join(env.root, filepath.FromSlash(rel)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	summary := env.sweep(t, func(o *CollectOptions) { o.Since = time.Now().Add(-30 * 24 * time.Hour) })
	if summary.Trajectories != 1 || summary.EventsEmitted != 10 {
		t.Fatalf("windowed sweep = %+v, want only the recent LAB stream", summary)
	}
	summary = env.sweep(t)
	if summary.EventsEmitted != 11 {
		t.Fatalf("follow-up sweep = %+v, want the two older trajectories", summary)
	}
}

// A budget refusal stops the sweep with cursors past only what was written; the next sweep writes
// the rest and nothing twice.
func TestCollectOnceStopsOnTheBudgetAndResumes(t *testing.T) {
	env := newSweepEnv(t)
	opts := CollectOptions{RunsDirs: []string{env.root}, StatePath: env.statePath, LogPath: env.logPath, Write: true, UserMode: true, Budget: writer.NewBudget(6000)}
	summary, err := CollectOnce(opts)
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("err = %v, want the budget refusal", err)
	}
	if summary.EventsEmitted == 0 || summary.EventsEmitted >= 21 {
		t.Fatalf("budgeted sweep wrote %d events, want some but not all", summary.EventsEmitted)
	}
	env.sweep(t)
	events := env.logged(t)
	ids := map[string]bool{}
	for _, ev := range events {
		if ids[ev.Event.ID] {
			t.Fatalf("event %s (%s) written twice", ev.Event.ID, ev.Event.Action)
		}
		ids[ev.Event.ID] = true
	}
	if len(events) != 21 {
		t.Fatalf("log holds %d events after resuming, want 21", len(events))
	}
}

func TestCommitStopsAtTheFirstUnwrittenLine(t *testing.T) {
	mapped := []MappedEvent{{SourceLine: 2}, {SourceLine: 5}, {SourceLine: 5}, {SourceLine: 9}}
	for _, tc := range []struct {
		written, want int
	}{
		{0, 1}, // nothing written: lines before the first event mapped to nothing
		{1, 4}, // line 5 has an unwritten event, so it stays behind the cursor
		{2, 4}, // half of line 5 written is not line 5 written
		{3, 8},
		{4, 12}, // everything written: the whole file, trailing lines included
	} {
		cursor := &Cursor{}
		commit(cursor, mapped, tc.written, 12)
		if cursor.Line != tc.want {
			t.Errorf("written=%d: cursor line = %d, want %d", tc.written, cursor.Line, tc.want)
		}
	}
}

// A sync larger than the rotation window must not rotate its own first events away while moving
// every cursor past them (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("HARNESS2_RUNS_DIR", "")
	root := filepath.Join(t.TempDir(), "runs")
	src, err := os.ReadFile(filepath.Join("testdata", "runs", filepath.FromSlash(jbRel)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		dir := filepath.Join(root, "jb", "dataset", "p", fmt.Sprintf("task%d", i), "model_traj", "l")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := strings.ReplaceAll(string(src), "ses_7a1f00c0ffee", fmt.Sprintf("ses_%03d", i))
		if err := os.WriteFile(filepath.Join(dir, "t_l_attempt1_x.jsonl"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := CollectOptions{RunsDirs: []string{root}, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}

// A remembered --runs-dir that later broke must not stop every scheduled sync: the sweep reports it
// and still collects the other roots.
func TestCollectOnceKeepsCollectingPastABrokenRoot(t *testing.T) {
	env := newSweepEnv(t)
	bad := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := CollectOnce(CollectOptions{RunsDirs: []string{bad, env.root}, StatePath: env.statePath, LogPath: env.logPath, Write: true, UserMode: true})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want the broken root reported", err)
	}
	if summary.Errors != 1 || summary.EventsEmitted != 21 {
		t.Fatalf("summary = %+v, want one error and the good root's 21 events", summary)
	}
	if len(env.logged(t)) != 21 {
		t.Fatal("the good root was not collected")
	}
}

// Extra roots are read beside the defaults but never remembered: they belong to another cursor file.
func TestCollectOnceReadsExtraRootsWithoutRememberingThem(t *testing.T) {
	env := newSweepEnv(t)
	userState := filepath.Join(t.TempDir(), "user-harness2.json")
	if err := (&State{Version: StateVersion, Roots: []string{env.root}, Files: map[string]*Cursor{}}).Save(userState); err != nil {
		t.Fatal(err)
	}
	extra := UserRememberedRoots(userState)
	if len(extra) != 1 || extra[0] != env.root {
		t.Fatalf("UserRememberedRoots = %v", extra)
	}
	summary := env.sweep(t, func(o *CollectOptions) { o.RunsDirs, o.ExtraRoots = nil, extra })
	if summary.EventsEmitted != 21 {
		t.Fatalf("sweep with extra roots = %+v", summary)
	}
	state, err := LoadState(env.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Roots) != 0 {
		t.Fatalf("extra roots were remembered in this cursor: %v", state.Roots)
	}
	for _, bad := range []string{filepath.Join(t.TempDir(), "missing.json"), env.logPath} {
		if got := UserRememberedRoots(bad); got != nil {
			t.Fatalf("UserRememberedRoots(%s) = %v, want nothing", bad, got)
		}
	}
}
