package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// The hook budget tests never wait on the wall clock (#859 exists because tests that did flaked on
// slow runners). The hook's clock is a fake the test moves, and its deadline is a context the test
// ends at the start of a chosen phase, so every step after that point sees exactly what a real
// deadline would give it.

// manualDeadline is a context whose deadline passes when the test says so.
type manualDeadline struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newManualDeadline(parent context.Context) *manualDeadline {
	return &manualDeadline{Context: parent, done: make(chan struct{})}
}

func (c *manualDeadline) Done() <-chan struct{} { return c.done }

func (c *manualDeadline) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *manualDeadline) expire() { c.once.Do(func() { close(c.done) }) }

type fakeHookClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeHookClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeHookClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var hookClockEpoch = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

// stubGitHookTime makes each phase of the next hook runs take steps[phase] on the hook's clock, and
// ends the hook's time as phase expireIn begins (never, when expireIn is empty). The budgets are
// the production ones; nothing here waits for them.
func stubGitHookTime(t *testing.T, steps map[string]time.Duration, expireIn string) {
	t.Helper()
	prev := startGitHook
	startGitHook = func(parent context.Context, hook string, budget time.Duration) (*gitlink.HookRun, context.Context, context.CancelFunc) {
		clock := &fakeHookClock{now: hookClockEpoch}
		ctx := newManualDeadline(parent)
		run := &gitlink.HookRun{Hook: hook, Budget: budget, Now: clock.Now}
		run.OnPhase = func(phase string) {
			clock.advance(steps[phase])
			if phase == expireIn {
				ctx.expire()
			}
		}
		return run, run.Start(ctx), func() {}
	}
	t.Cleanup(func() { startGitHook = prev })
}

func budgetEventsIn(t *testing.T, logPath string) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e schema.Event
		if json.Unmarshal([]byte(line), &e) == nil && e.Event.Action == asymptoteobserve.ActionGitHookBudgetExceeded {
			events = append(events, e)
		}
	}
	return events
}

// onlyBudgetEvent returns the one budget event in the log and checks the parts every one carries.
func onlyBudgetEvent(t *testing.T, logPath string) schema.VCSHookInfo {
	t.Helper()
	events := budgetEventsIn(t, logPath)
	if len(events) != 1 {
		t.Fatalf("got %d budget events, want 1: %+v", len(events), events)
	}
	e := events[0]
	if e.Harness.Name != "beacon" || e.Event.Category != "system" || e.VCS == nil || e.VCS.Hook == nil {
		t.Fatalf("budget event %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("budget event does not validate: %v", err)
	}
	return *e.VCS.Hook
}

// hookRepoRoot is the work tree as the hooks record it: gitlink's cleaned path, which on Windows
// uses backslashes where `git rev-parse --show-toplevel` prints forward slashes.
func hookRepoRoot(t *testing.T, root string) string {
	t.Helper()
	repo, err := gitlink.OpenRepo(context.Background(), gitlink.Git{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	return repo.Root
}

// postCommitFixture is a repository whose HEAD a session in the runtime log wrote. repoRoot is
// the work tree as the hooks record it.
func postCommitFixture(t *testing.T) (root, repoRoot, logPath string) {
	t.Helper()
	root = gitCommandRepo(t)
	top := mustGit(t, root, "rev-parse", "--show-toplevel")
	logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeEvent(t, logPath, top, "cur-1", "x.go")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "x")
	return root, hookRepoRoot(t, root), logPath
}

// A post-commit run inside its budget links the commit and records no budget event.
func TestGitHookPostCommitWithinBudgetRecordsNoOverrun(t *testing.T) {
	stubGitHookTime(t, map[string]time.Duration{"open_repository": 20 * time.Millisecond, "attribute": 400 * time.Millisecond, "record_link_event": 30 * time.Millisecond}, "")
	root, _, logPath := postCommitFixture(t)
	if out, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil || out != "" {
		t.Fatalf("hook: %v %q", err, out)
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", "HEAD"); note != "beacon:cursor/cur-1" {
		t.Fatalf("note %q", note)
	}
	if got := budgetEventsIn(t, logPath); len(got) != 0 {
		t.Fatalf("a run within its budget recorded %+v", got)
	}
}

// Running out of time while attributing leaves the commit unlinked, and the hook says so in the
// runtime log, with the phase, the budget, the time the run and the phase took, and the git
// processes it started, while staying silent on the terminal.
func TestGitHookPostCommitRecordsAnOverrunThatCutTheLinkShort(t *testing.T) {
	stubGitHookTime(t, map[string]time.Duration{"open_repository": 40 * time.Millisecond, "attribute": 3250 * time.Millisecond}, "attribute")
	root, top, logPath := postCommitFixture(t)
	var stderr bytes.Buffer
	if out, err := runGitStderr(t, &stderr, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil || out != "" || stderr.Len() != 0 {
		t.Fatalf("hook: %v, stdout %q, stderr %q (a hook must stay quiet)", err, out, stderr.String())
	}
	if note := mustGitAllowFail(root, "notes", "--ref="+gitlink.NotesRef, "show", "HEAD"); note != "" {
		t.Fatalf("a hook out of time still linked the commit: %q", note)
	}
	if got := linkEventsIn(t, logPath); len(got) != 0 {
		t.Fatalf("link events %+v", got)
	}
	hook := onlyBudgetEvent(t, logPath)
	if hook.Name != "post-commit" || hook.Phase != "attribute" || !hook.CutShort {
		t.Fatalf("overrun %+v, want post-commit cut short in attribute", hook)
	}
	if hook.BudgetMS != gitHookBudget.Milliseconds() || hook.BudgetMS != 3000 {
		t.Fatalf("budget %dms, want the production 3000ms", hook.BudgetMS)
	}
	if hook.ElapsedMS != 3290 || hook.PhaseElapsedMS != 3250 {
		t.Fatalf("elapsed %dms (phase %dms), want 3290ms and 3250ms", hook.ElapsedMS, hook.PhaseElapsedMS)
	}
	if hook.GitSpawns < 1 {
		t.Fatalf("counted %d git spawns; opening the repository alone spawns git", hook.GitSpawns)
	}
	if e := budgetEventsIn(t, logPath)[0]; e.Repository != top || e.Severity != schema.SeverityLow {
		t.Fatalf("event repository %q severity %s, want %q low", e.Repository, e.Severity, top)
	}
}

// A deadline that passes only in the last step, after the note is written, is recorded as a run
// that finished late, not one that was cut short: the commit is linked.
func TestGitHookPostCommitRecordsARunThatFinishedLate(t *testing.T) {
	stubGitHookTime(t, map[string]time.Duration{"attribute": 2900 * time.Millisecond, "record_link_event": 200 * time.Millisecond}, "record_link_event")
	root, _, logPath := postCommitFixture(t)
	if _, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil {
		t.Fatal(err)
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", "HEAD"); note != "beacon:cursor/cur-1" {
		t.Fatalf("note %q", note)
	}
	hook := onlyBudgetEvent(t, logPath)
	if hook.Phase != "record_link_event" || hook.CutShort || hook.ElapsedMS != 3100 || hook.PhaseElapsedMS != 200 {
		t.Fatalf("overrun %+v, want record_link_event finished late after 3100ms", hook)
	}
}

// Running out of time before the repository is even resolved is still recorded, without a
// repository.
func TestGitHookPostCommitRecordsAnOverrunBeforeTheRepository(t *testing.T) {
	stubGitHookTime(t, map[string]time.Duration{"open_repository": 3500 * time.Millisecond}, "open_repository")
	root, _, logPath := postCommitFixture(t)
	if _, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil {
		t.Fatal(err)
	}
	hook := onlyBudgetEvent(t, logPath)
	if hook.Phase != "open_repository" || !hook.CutShort || hook.GitSpawns != 0 || hook.ElapsedMS != 3500 {
		t.Fatalf("overrun %+v, want open_repository cut short with no git started", hook)
	}
	if e := budgetEventsIn(t, logPath)[0]; e.Repository != "" {
		t.Fatalf("repository %q for a run that never resolved one", e.Repository)
	}
}

func TestGitHookPostRewriteRecordsAnOverrun(t *testing.T) {
	stubGitHookTime(t, map[string]time.Duration{"normalize_notes": 3 * time.Second}, "normalize_notes")
	root := gitCommandRepo(t)
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "c")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:codex_cli/a\n\nbeacon:codex_cli/a", sha)
	rootCmd.SetIn(strings.NewReader("0000000000000000000000000000000000000000 " + sha + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	if _, err := runGit(t, "hook", "post-rewrite", "amend", "--cwd", root); err != nil {
		t.Fatal(err)
	}
	// After runGit, which resets the flags a previous test may have left set.
	hook := onlyBudgetEvent(t, gitRuntimeLogPath())
	if hook.Name != "post-rewrite" || hook.Phase != "normalize_notes" || !hook.CutShort || hook.BudgetMS != 3000 || hook.ElapsedMS != 3000 {
		t.Fatalf("overrun %+v, want post-rewrite cut short in normalize_notes", hook)
	}
}

// The pre-push hook records running out of time in its notes push against its own budget, and the
// push still never fails.
func TestGitHookPrePushRecordsAnOverrunInThePush(t *testing.T) {
	stubGitHookTime(t, map[string]time.Duration{"open_repository": 100 * time.Millisecond, "push_notes": 9900 * time.Millisecond}, "push_notes")
	root, remote := gitCommandTeam(t)
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:cursor/c1", sha)
	var stderr bytes.Buffer
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil {
		t.Fatalf("pre-push must not fail: %v", err)
	}
	if !strings.HasPrefix(stderr.String(), "beacon: commit links were not shared with origin") {
		t.Fatalf("stderr %q", stderr.String())
	}
	hook := onlyBudgetEvent(t, gitRuntimeLogPath())
	if hook.Name != "pre-push" || hook.Phase != "push_notes" || !hook.CutShort || hook.BudgetMS != gitShareBudget.Milliseconds() || hook.ElapsedMS != 10000 || hook.PhaseElapsedMS != 9900 {
		t.Fatalf("overrun %+v, want pre-push cut short in push_notes after 10000ms", hook)
	}
}

// A pre-push that shares nothing and pushes in time records no overrun.
func TestGitHookPrePushWithinBudgetRecordsNoOverrun(t *testing.T) {
	stubGitHookTime(t, nil, "")
	root, remote := gitCommandTeam(t)
	if _, err := runGit(t, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil {
		t.Fatal(err)
	}
	if got := budgetEventsIn(t, gitRuntimeLogPath()); len(got) != 0 {
		t.Fatalf("budget events %+v", got)
	}
}

func appendBudgetEvent(t *testing.T, logPath, hook, repoRoot string, at time.Time, cutShort bool) {
	t.Helper()
	at = at.Truncate(time.Millisecond)
	clock := &fakeHookClock{now: at.Add(-3200 * time.Millisecond)}
	run := &gitlink.HookRun{Hook: hook, Budget: gitHookBudget, Now: clock.Now}
	ctx := newManualDeadline(context.Background())
	run.Start(ctx)
	run.Phase("attribute")
	clock.advance(3200 * time.Millisecond)
	ctx.expire()
	var err error
	if cutShort {
		err = context.DeadlineExceeded
	}
	run.Check(ctx, err)
	event, _ := run.Event(repoRoot, "")
	line, _ := json.Marshal(event)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestGitStatusCountsRecentOverruns(t *testing.T) {
	root := gitCommandRepo(t)
	top := hookRepoRoot(t, root)
	out, err := runGit(t, "status", "--cwd", root)
	if err != nil || !strings.Contains(out, "overruns    none in the last 7 days\n") {
		t.Fatalf("status without overruns: %v\n%s", err, out)
	}
	logPath := gitRuntimeLogPath()
	now := time.Now()
	appendBudgetEvent(t, logPath, "post-commit", top, now.Add(-8*24*time.Hour), true) // too old to count
	appendBudgetEvent(t, logPath, "post-commit", top, now.Add(-2*time.Hour), true)
	last := now.Add(-time.Hour)
	appendBudgetEvent(t, logPath, "post-rewrite", top, last, false)
	appendBudgetEvent(t, logPath, "post-commit", filepath.Join(t.TempDir(), "elsewhere"), now.Add(-time.Minute), true) // another repository

	out, err = runGit(t, "status", "--cwd", root)
	if err != nil {
		t.Fatal(err)
	}
	want := "overruns    2 hook run(s) ran out of time in the last 7 days, 1 cut short; last " +
		schema.FormatTimestamp(last.Truncate(time.Millisecond)) + ": post-rewrite in attribute, 3.2s of 3s, finished late\n"
	if !strings.Contains(out, want) {
		t.Fatalf("status:\n%s\nwant line %q", out, want)
	}
	out, err = runGit(t, "status", "--cwd", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		BudgetOverruns gitlink.BudgetOverruns `json:"budget_overruns"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status json %v: %s", err, out)
	}
	if o := st.BudgetOverruns; o.Count != 2 || o.CutShort != 1 || o.Last == nil || o.Last.Name != "post-rewrite" || o.Last.Repository != top || o.Last.ElapsedMS != 3200 {
		t.Fatalf("budget_overruns %+v (last %+v)", o, o.Last)
	}
}

func TestDoctorReportsGitHookOverruns(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	now := hookClockEpoch.Add(24 * time.Hour)
	if _, ok := gitHookBudgetCheck(logPath, now); ok {
		t.Fatal("doctor reported git hook overruns on an empty log")
	}
	appendBudgetEvent(t, logPath, "post-rewrite", "/work/a", now.Add(-time.Hour), false)
	check, ok := gitHookBudgetCheck(logPath, now)
	if !ok || check.Status != diagnostics.StatusOK || check.Evidence != "git_hook_finished_late" || check.Action != "" {
		t.Fatalf("finished late only: %+v (%v), want ok with no action", check, ok)
	}
	appendBudgetEvent(t, logPath, "post-commit", "/work/b", now.Add(-time.Minute), true)
	check, ok = gitHookBudgetCheck(logPath, now)
	if !ok || check.Name != "git_hook_budget" || check.Status != diagnostics.StatusWarn || check.Severity != diagnostics.SeverityLow ||
		check.Evidence != "git_hook_cut_short" || check.Target != "/work/b" || !strings.Contains(check.Message, "2 hook run(s)") || !strings.Contains(check.Message, "1 cut short") ||
		!strings.Contains(check.Action, "beacon git link") {
		t.Fatalf("cut short: %+v (%v)", check, ok)
	}
	// --fix has nothing to repair, and says so rather than staying silent.
	plan := planDoctorFixes(doctorResult{Checks: []diagnostics.Check{check}}, lifecycle.Status{})
	if len(plan.Fixes) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Action != "manual_fix" || plan.Skipped[0].Message != check.Action {
		t.Fatalf("fix plan %+v", plan)
	}
	// Outside the window nothing counts.
	if _, ok := gitHookBudgetCheck(logPath, now.Add(8*24*time.Hour)); ok {
		t.Fatal("doctor counted overruns older than the window")
	}
}
