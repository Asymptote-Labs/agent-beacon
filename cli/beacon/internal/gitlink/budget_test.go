package gitlink

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// testClock is a clock that moves only when a test moves it.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) advance(d time.Duration) { c.now = c.now.Add(d) }

var budgetEpoch = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

// expiredContext has a deadline that has already passed, so it reports DeadlineExceeded without
// any wall-clock wait.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	t.Cleanup(cancel)
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context not expired: %v", ctx.Err())
	}
	return ctx
}

func TestHookRunUnderBudgetRecordsNothing(t *testing.T) {
	clock := &testClock{now: budgetEpoch}
	run := &HookRun{Hook: "post-commit", Budget: 3 * time.Second, Now: clock.Now}
	ctx := run.Start(context.Background())
	for _, phase := range []string{"open_repository", "attribute", "record_link_event"} {
		run.Phase(phase)
		clock.advance(900 * time.Millisecond)
		// A step that failed for its own reasons is not a budget overrun.
		if run.Check(ctx, errors.New("not a repository")) {
			t.Fatalf("%s: a live context was reported as out of time", phase)
		}
	}
	if hook, ok := run.Overrun(); ok {
		t.Fatalf("a run within its budget recorded %+v", hook)
	}
	if _, ok := run.Event("/work/repo", "1.0.0"); ok {
		t.Fatal("a run within its budget produced an event")
	}
}

// The phase that ran out, the time the whole run took and the time spent in that phase all come
// from the injected clock, so they are exact.
func TestHookRunRecordsTheOverrunPhaseAndTimes(t *testing.T) {
	clock := &testClock{now: budgetEpoch}
	run := &HookRun{Hook: "post-commit", Budget: 3 * time.Second, Now: clock.Now}
	live := run.Start(context.Background())
	run.Phase("open_repository")
	clock.advance(40 * time.Millisecond)
	run.Check(live, nil)
	run.Phase("attribute")
	clock.advance(3250 * time.Millisecond)
	if !run.Check(expiredContext(t), context.DeadlineExceeded) {
		t.Fatal("an expired deadline was not reported")
	}
	clock.advance(10 * time.Millisecond) // the hook winds down before it records the run
	hook, ok := run.Overrun()
	if !ok {
		t.Fatal("the overrun was not recorded")
	}
	want := schema.VCSHookInfo{Name: "post-commit", Phase: "attribute", BudgetMS: 3000, ElapsedMS: 3300, PhaseElapsedMS: 3250, CutShort: true}
	if hook != want {
		t.Fatalf("overrun %+v, want %+v", hook, want)
	}
}

// A step that succeeded although its deadline passed finished late: the run is recorded, but not
// as cut short.
func TestHookRunThatFinishedLateIsNotCutShort(t *testing.T) {
	clock := &testClock{now: budgetEpoch}
	run := &HookRun{Hook: "post-rewrite", Budget: 3 * time.Second, Now: clock.Now}
	run.Start(context.Background())
	run.Phase("normalize_notes")
	clock.advance(3100 * time.Millisecond)
	run.Check(expiredContext(t), nil)
	hook, ok := run.Overrun()
	if !ok || hook.CutShort || hook.Phase != "normalize_notes" || hook.ElapsedMS != 3100 {
		t.Fatalf("overrun %+v (recorded %v), want normalize_notes finished late after 3100ms", hook, ok)
	}
}

// The first phase to run out is the one recorded, because the later ones only ran on what was
// left; any of them losing work marks the run cut short.
func TestHookRunKeepsTheFirstPhaseAndAccumulatesLoss(t *testing.T) {
	clock := &testClock{now: budgetEpoch}
	run := &HookRun{Hook: "pre-push", Budget: 10 * time.Second, Now: clock.Now}
	run.Start(context.Background())
	run.Phase("share_sessions")
	clock.advance(6 * time.Second)
	run.RanOut(false)
	run.Phase("push_notes")
	clock.advance(3 * time.Second)
	run.Check(expiredContext(t), errors.New("push failed"))
	hook, _ := run.Overrun()
	if hook.Phase != "share_sessions" || hook.PhaseElapsedMS != 6000 || hook.ElapsedMS != 9000 || !hook.CutShort {
		t.Fatalf("overrun %+v, want share_sessions after 6000ms of a 9000ms run, cut short", hook)
	}
}

// An interrupted hook (cancelled, not out of time) is not a budget overrun.
func TestHookRunIgnoresCancellation(t *testing.T) {
	run := &HookRun{Hook: "post-commit", Budget: 3 * time.Second}
	ctx, cancel := context.WithCancel(run.Start(context.Background()))
	cancel()
	run.Phase("attribute")
	if run.Check(ctx, ctx.Err()) {
		t.Fatal("cancellation was reported as running out of time")
	}
	if _, ok := run.Overrun(); ok {
		t.Fatal("cancellation was recorded as an overrun")
	}
}

// Every git process started under the run's context is counted, through derived contexts and
// detached ones too; a git that never starts, because its context has already ended, is not.
func TestHookRunCountsGitSpawns(t *testing.T) {
	r := newTestRepo(t)
	g := r.gitClient()
	run := &HookRun{Hook: "post-commit", Budget: 3 * time.Second}
	ctx := run.Start(context.Background())
	derived, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	detached, cancelDetached := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelDetached()
	for _, c := range []context.Context{ctx, derived, detached} {
		if _, err := g.Run(c, nil, "rev-parse", "--git-dir"); err != nil {
			t.Fatal(err)
		}
	}
	// A git that fails still started and counts.
	if _, err := g.Run(ctx, nil, "rev-parse", "--verify", "--quiet", "refs/heads/missing"); err == nil {
		t.Fatal("rev-parse of a missing ref succeeded")
	}
	ended, cancelEnded := context.WithCancel(ctx)
	cancelEnded()
	if _, err := g.Run(ended, nil, "rev-parse", "--git-dir"); err == nil {
		t.Fatal("git ran on an ended context")
	}
	// A git outside the run is not counted.
	if _, err := g.Run(context.Background(), nil, "rev-parse", "--git-dir"); err != nil {
		t.Fatal(err)
	}
	run.Phase("attribute")
	run.RanOut(true)
	hook, _ := run.Overrun()
	if hook.GitSpawns != 4 {
		t.Fatalf("counted %d git spawns, want 4", hook.GitSpawns)
	}
}

func TestHookRunEvent(t *testing.T) {
	clock := &testClock{now: budgetEpoch}
	run := &HookRun{Hook: "post-commit", Budget: 3 * time.Second, Now: clock.Now}
	run.Start(context.Background())
	run.Phase("attribute")
	clock.advance(3200 * time.Millisecond)
	run.Check(expiredContext(t), context.DeadlineExceeded)
	event, ok := run.Event("/work/repo", "1.2.3")
	if !ok {
		t.Fatal("no event for an overrun")
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("event does not validate: %v", err)
	}
	if event.Event.Action != asymptoteobserve.ActionGitHookBudgetExceeded || event.Event.Category != "system" || event.Event.Kind != "beacon_system" ||
		event.Event.Fidelity != asymptoteobserve.FidelityObserved || event.Harness.Name != "beacon" ||
		event.Severity != schema.SeverityLow || event.Repository != "/work/repo" || event.Endpoint.AgentVersion != "1.2.3" {
		t.Fatalf("event %+v", event)
	}
	if event.Timestamp != schema.FormatTimestamp(budgetEpoch.Add(3200*time.Millisecond)) {
		t.Fatalf("timestamp %s does not come from the run's clock", event.Timestamp)
	}
	if event.VCS == nil || event.VCS.Hook == nil || event.VCS.Hook.Phase != "attribute" || event.VCS.Hook.ElapsedMS != 3200 {
		t.Fatalf("vcs %+v", event.VCS)
	}
	if want := "post-commit hook ran out of time in attribute: 3.2s of its 3s budget, 0 git process(es); its work was cut short"; event.Message != want {
		t.Fatalf("message %q, want %q", event.Message, want)
	}
	// Only the hook's own record: no session, command, file or content fields.
	if event.Session != nil || event.Command != nil || event.File != nil || event.Tool != nil || event.Content != nil || event.Raw != nil {
		t.Fatalf("event carries more than the hook's record: %+v", event)
	}
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"hook":{"name":"post-commit","phase":"attribute","budget_ms":3000,"elapsed_ms":3200,"phase_elapsed_ms":3200,"cut_short":true}`} {
		if !strings.Contains(string(line), want) {
			t.Fatalf("event JSON %s lacks %s", line, want)
		}
	}

	late := &HookRun{Hook: "post-rewrite", Budget: 3 * time.Second, Now: clock.Now}
	late.Start(context.Background())
	late.Phase("normalize_notes")
	late.Check(expiredContext(t), nil)
	if event, _ := late.Event("", ""); event.Severity != schema.SeverityInfo || !strings.HasSuffix(event.Message, "it finished late") {
		t.Fatalf("finished-late event severity %s message %q", event.Severity, event.Message)
	}
}

func writeBudgetEvent(t *testing.T, logPath, hook, repoRoot string, at time.Time, cutShort bool) {
	t.Helper()
	clock := &testClock{now: at.Add(-3200 * time.Millisecond)}
	run := &HookRun{Hook: hook, Budget: 3 * time.Second, Now: clock.Now}
	run.Start(context.Background())
	run.Phase("attribute")
	clock.advance(3200 * time.Millisecond)
	var err error
	if cutShort {
		err = context.DeadlineExceeded
	}
	run.Check(expiredContext(t), err)
	event, _ := run.Event(repoRoot, "")
	line, _ := json.Marshal(event)
	f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestScanBudgetOverruns(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if got := ScanBudgetOverruns(logPath, "", budgetEpoch); got.Count != 0 || got.Last != nil {
		t.Fatalf("a missing log counted %+v", got)
	}
	since := budgetEpoch.Add(-7 * 24 * time.Hour)
	writeBudgetEvent(t, logPath, "post-commit", "/work/a", since.Add(-time.Minute), true) // too old
	writeBudgetEvent(t, logPath, "post-commit", "/work/a", budgetEpoch.Add(-2*time.Hour), true)
	writeBudgetEvent(t, logPath, "pre-push", "/work/b", budgetEpoch.Add(-time.Hour), false)
	writeBudgetEvent(t, logPath, "post-rewrite", "/work/a", budgetEpoch.Add(-3*time.Hour), false)
	// Lines that only mention the action, and a link event, count for nothing.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	link := schema.NewEvent(schema.NewEventOptions{Action: asymptoteobserve.ActionCommitLinked, Harness: schema.HarnessInfo{Name: "cursor"}, Message: asymptoteobserve.ActionGitHookBudgetExceeded})
	linkLine, _ := json.Marshal(link)
	_, _ = f.Write([]byte(`not json "` + asymptoteobserve.ActionGitHookBudgetExceeded + `"` + "\n" + string(linkLine) + "\n"))
	f.Close()

	all := ScanBudgetOverruns(logPath, "", since)
	if all.Count != 3 || all.CutShort != 1 || all.Last == nil || all.Last.Name != "pre-push" || all.Last.Repository != "/work/b" {
		t.Fatalf("all repositories: %+v (last %+v)", all, all.Last)
	}
	if all.Since != schema.FormatTimestamp(since) {
		t.Fatalf("since %q", all.Since)
	}
	a := ScanBudgetOverruns(logPath, "/work/a", since)
	if a.Count != 2 || a.CutShort != 1 || a.Last == nil || a.Last.Name != "post-commit" || a.Last.Phase != "attribute" || !a.Last.CutShort {
		t.Fatalf("repository a: %+v (last %+v)", a, a.Last)
	}
	if want := "post-commit in attribute, 3.2s of 3s, cut short"; a.Last.Describe() != want {
		t.Fatalf("describe %q, want %q", a.Last.Describe(), want)
	}
	if got := ScanBudgetOverruns(logPath, "/work/none", since); got.Count != 0 || got.Last != nil {
		t.Fatalf("an unrelated repository counted %+v", got)
	}
}

// A busy runtime log rotates within the window, so an overrun in a rotated archive still counts.
func TestScanBudgetOverrunsReadsRotatedArchives(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	since := budgetEpoch.Add(-7 * 24 * time.Hour)
	writeBudgetEvent(t, logPath+".2", "post-commit", "/work/a", budgetEpoch.Add(-3*24*time.Hour), true)
	writeBudgetEvent(t, logPath, "pre-push", "/work/a", budgetEpoch.Add(-time.Hour), false)

	got := ScanBudgetOverruns(logPath, "/work/a", since)
	if got.Count != 2 || got.CutShort != 1 || got.Last == nil || got.Last.Name != "pre-push" {
		t.Fatalf("overruns across the live log and an archive: %+v (last %+v)", got, got.Last)
	}
}
