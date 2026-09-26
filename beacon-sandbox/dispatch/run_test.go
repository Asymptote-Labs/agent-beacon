package dispatch

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitOrFail blocks on ch, failing loudly rather than hanging the suite if the interleaving a test
// forces never happens.
func waitOrFail(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Errorf("timed out waiting for %s", what)
	}
}

// #681. Two callers dispatch at nearly the same time. Both snapshot the same floor before either
// dispatch lands, then both runs become visible before either caller looks for its own. Selecting
// "the smallest id above the floor" hands both callers the same run: one judges the other's
// artifacts, and the other's own run is never matched.
//
// The interleaving is forced, not hoped for. The fake GitHub holds each dispatch until both
// snapshots are taken, and holds every post-dispatch listing until both runs exist -- the exact
// window the issue traces. Each caller must come back with the run its own dispatch created and
// the artifacts that run produced, and never the other's.
func TestConcurrentDispatchesEachFollowTheirOwnRun(t *testing.T) {
	var snapshots, dispatches atomic.Int32
	snapshotsTaken := make(chan struct{})
	bothDispatched := make(chan struct{})
	var closeSnap, closeDisp sync.Once
	gh := newFakeGitHub(t, func(gh *fakeGitHub) {
		gh.beforeList = func() {
			if dispatches.Load() == 0 {
				// A listing before any dispatch has completed is a caller taking its floor.
				if snapshots.Add(1) == 2 {
					closeSnap.Do(func() { close(snapshotsTaken) })
				}
				return
			}
			waitOrFail(t, bothDispatched, "both dispatches to land")
		}
		gh.beforeDispatch = func() { waitOrFail(t, snapshotsTaken, "both callers to snapshot the floor") }
		gh.afterDispatch = func(*fakeRun) {
			if dispatches.Add(1) == 2 {
				closeDisp.Do(func() { close(bothDispatched) })
			}
		}
	})

	scenarios := []string{"w00-probe", "w03-hook-capture"}
	results := make([]Result, len(scenarios))
	errs := make([]error, len(scenarios))
	var wg sync.WaitGroup
	for i, sc := range scenarios {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Run(fastOptions(t, sc))
		}()
	}
	wg.Wait()

	for i, sc := range scenarios {
		if errs[i] != nil {
			t.Fatalf("caller %s: %v", sc, errs[i])
		}
		want := gh.runFor(sc)
		if want == 0 {
			t.Fatalf("the fake never saw a dispatch for %s", sc)
		}
		if results[i].RunID != strconv.FormatInt(want, 10) {
			t.Errorf("caller %s followed run %s, but its own dispatch created run %d", sc,
				results[i].RunID, want)
		}
		assertOwnArtifacts(t, results[i], sc)
	}
	if results[0].RunID == results[1].RunID {
		t.Errorf("both callers attached to run %s; one of them judged the other's artifacts",
			results[0].RunID)
	}
}

// assertOwnArtifacts checks the caller brought home exactly the run directory its own dispatch
// produced. This is the consequence that matters: a wrong run id alone is a bookkeeping error,
// but judging another invocation's artifacts is a wrong verdict.
func assertOwnArtifacts(t *testing.T, res Result, scenario string) {
	t.Helper()
	if len(res.RunDirs) != 1 {
		t.Errorf("caller %s collected %d run dirs, want 1: %v", scenario, len(res.RunDirs), res.RunDirs)
		return
	}
	if got, want := filepath.Base(res.RunDirs[0]), scenario+"-"+res.RunID; got != want {
		t.Errorf("caller %s collected %s, want its own run's %s", scenario, got, want)
	}
	data, err := os.ReadFile(filepath.Join(res.RunDirs[0], "runtime.jsonl"))
	if err != nil {
		t.Errorf("caller %s: %v", scenario, err)
		return
	}
	if want := `"scenario":"` + scenario + `"`; !strings.Contains(string(data), want) {
		t.Errorf("caller %s judged another dispatch's log: %s", scenario, data)
	}
}

func fastOptions(t *testing.T, scenario string) Options {
	return Options{
		Repo:          "fake/fake",
		Ref:           "work",
		Scenario:      scenario,
		ClaudeVersion: "2.1.220",
		OutDir:        t.TempDir(),
		PollInterval:  10 * time.Millisecond,
		Timeout:       10 * time.Second,
		newRunPoll:    10 * time.Millisecond,
		newRunTimeout: 10 * time.Second,
	}
}

// The single-caller path, which every dispatch took before #681 and must still take: the run is
// not listed for the first few polls, then runs through its states, and the caller comes back
// with it and its artifacts. Also pins what the dispatch sends: the correlation id rides as a
// workflow input alongside the existing ones.
func TestSingleDispatchFollowsItsRunToCompletion(t *testing.T) {
	gh := newFakeGitHub(t, func(gh *fakeGitHub) {
		gh.hideNewRunsFor = 3
		gh.runListsUntilDone = 3
	})

	res, err := Run(fastOptions(t, "w00-probe"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := strconv.FormatInt(gh.runFor("w00-probe"), 10); res.RunID != want {
		t.Errorf("followed run %s, want %s", res.RunID, want)
	}
	if res.Conclusion != "success" {
		t.Errorf("conclusion = %q, want success", res.Conclusion)
	}
	if res.URL != "https://github.com/fake/fake/actions/runs/"+res.RunID {
		t.Errorf("URL = %q", res.URL)
	}
	assertOwnArtifacts(t, res, "w00-probe")

	sent := gh.dispatches()
	if len(sent) != 1 {
		t.Fatalf("expected one dispatch, got %d", len(sent))
	}
	if sent[0]["scenario"] != "w00-probe" || sent[0]["claude_version"] != "2.1.220" {
		t.Errorf("the existing inputs must still be sent unchanged, got %v", sent[0])
	}
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(sent[0][correlationInput]) {
		t.Errorf("the dispatch must carry a correlation id, got %q", sent[0][correlationInput])
	}
}

// A run that appears first but is not ours -- another invocation's, or a manual dispatch from the
// Actions tab -- is waited past, not taken. Sequential counterpart of the race: the foreign runs
// are the earliest above the floor, which is exactly what the old rule selected.
func TestDispatchWaitsPastRunsThatAreNotItsOwn(t *testing.T) {
	gh := newFakeGitHub(t, func(gh *fakeGitHub) {
		gh.hideNewRunsFor = 2
		gh.beforeDispatch = func() {
			gh.addForeignRun("Windows sandbox " + correlationMarker("fedcba9876543210fedcba98"))
			gh.addForeignRun("Windows sandbox")
		}
	})

	res, err := Run(fastOptions(t, "w03-hook-capture"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	foreign := gh.foreignRuns()
	if len(foreign) != 2 {
		t.Fatalf("the fake should have added two foreign runs, got %v", foreign)
	}
	for _, id := range foreign {
		if res.RunID == strconv.FormatInt(id, 10) {
			t.Fatalf("attached to foreign run %d instead of our own", id)
		}
	}
	if want := strconv.FormatInt(gh.runFor("w03-hook-capture"), 10); res.RunID != want {
		t.Errorf("followed run %s, want our own %s", res.RunID, want)
	}
	assertOwnArtifacts(t, res, "w03-hook-capture")
}

// If our id never shows up but other runs did, the dispatch cannot know which, if any, is ours --
// for instance the dispatched ref's workflow accepts the input but does not render it into its
// title. That must be an explicit ambiguity error naming the runs, and nothing may be downloaded
// or judged.
func TestDispatchReportsAmbiguityWhenNoRunCarriesItsID(t *testing.T) {
	gh := newFakeGitHub(t, func(gh *fakeGitHub) {
		gh.noRunName = true
		gh.beforeDispatch = func() { gh.addForeignRun("Windows sandbox") }
	})

	opts := fastOptions(t, "w00-probe")
	opts.newRunTimeout = 300 * time.Millisecond
	res, err := Run(opts)
	if err == nil {
		t.Fatalf("an uncorrelatable run must be an error, got run %s", res.RunID)
	}
	ours := strconv.FormatInt(gh.runFor("w00-probe"), 10)
	foreign := gh.foreignRuns()
	if len(foreign) != 1 {
		t.Fatalf("the fake should have added one foreign run, got %v", foreign)
	}
	for _, want := range []string{"refusing to guess", ours, strconv.FormatInt(foreign[0], 10),
		correlationInput, "run-name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the ambiguity error should mention %q: %v", want, err)
		}
	}
	if res.RunID != "" || len(res.RunDirs) != 0 {
		t.Errorf("nothing may be selected or collected on ambiguity, got %+v", res)
	}
	assertNothingCollected(t, opts.OutDir)
}

// No run at all within the wait is the plain timeout it always was, not an ambiguity.
func TestDispatchTimesOutWhenNoRunAppears(t *testing.T) {
	newFakeGitHub(t, func(gh *fakeGitHub) { gh.hideNewRunsFor = 1 << 30 })

	opts := fastOptions(t, "w00-probe")
	opts.newRunTimeout = 200 * time.Millisecond
	_, err := Run(opts)
	if err == nil {
		t.Fatal("a run that never appears must be an error")
	}
	if !strings.Contains(err.Error(), "no new "+Workflow+" run appeared") {
		t.Errorf("want the no-run timeout, got: %v", err)
	}
	if strings.Contains(err.Error(), "refusing to guess") {
		t.Errorf("nothing appeared, so there is nothing ambiguous: %v", err)
	}
}

// A listing that fails transiently while waiting is retried, as before, rather than abandoning a
// run that is proceeding fine.
func TestDispatchRetriesAFailedListing(t *testing.T) {
	var failed atomic.Bool
	gh := newFakeGitHub(t, func(gh *fakeGitHub) {
		gh.hideNewRunsFor = 1
		gh.afterDispatch = func(*fakeRun) {
			gh.mu.Lock()
			gh.failLists = 2
			gh.mu.Unlock()
			failed.Store(true)
		}
	})
	res, err := Run(fastOptions(t, "w00-probe"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !failed.Load() {
		t.Fatal("the fake never injected the failure")
	}
	gh.mu.Lock()
	left := gh.failLists
	gh.mu.Unlock()
	if left != 0 {
		t.Errorf("%d injected listing failures were never hit, so the retry was not exercised", left)
	}
	assertOwnArtifacts(t, res, "w00-probe")
}

// The completion wait's own timeout still reports the run it was following, so the reader can
// go and look at it.
func TestDispatchReportsARunThatDoesNotComplete(t *testing.T) {
	gh := newFakeGitHub(t, func(gh *fakeGitHub) { gh.runListsUntilDone = 1 << 30 })

	opts := fastOptions(t, "w00-probe")
	opts.Timeout = 200 * time.Millisecond
	res, err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "did not complete within") {
		t.Fatalf("want the completion timeout, got: %v", err)
	}
	if want := strconv.FormatInt(gh.runFor("w00-probe"), 10); res.RunID != want {
		t.Errorf("the timeout must still identify our run: got %q, want %s", res.RunID, want)
	}
}

// GitHub reads the workflow from the dispatched ref, so a branch cut before the correlation input
// existed rejects it with a 422. That is refused with the fix, not retried uncorrelated: an
// uncorrelated dispatch is precisely the one that can attach to someone else's run.
func TestDispatchRefusesARefWhoseWorkflowLacksTheCorrelationInput(t *testing.T) {
	gh := newFakeGitHub(t, func(gh *fakeGitHub) {
		gh.declared = map[string]bool{"scenario": true, "claude_version": true}
	})

	_, err := Run(fastOptions(t, "w00-probe"))
	if err == nil {
		t.Fatal("dispatching a ref without the correlation input must fail")
	}
	for _, want := range []string{correlationInput, "work", "rebase"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q: %v", want, err)
		}
	}
	if n := len(gh.dispatches()); n != 0 {
		t.Errorf("no uncorrelated fallback dispatch may be made, got %d", n)
	}
}

// displayTitle is what carries the correlation id, so a gh too old to report it cannot identify a
// run at all. That must fail before anything is dispatched, and say what to do.
func TestDispatchExplainsAGHTooOldForDisplayTitle(t *testing.T) {
	gh := newFakeGitHub(t, func(gh *fakeGitHub) { gh.oldGH = true })

	_, err := Run(fastOptions(t, "w00-probe"))
	if err == nil || !strings.Contains(err.Error(), "upgrade gh") {
		t.Fatalf("want an upgrade-gh error, got: %v", err)
	}
	if n := len(gh.dispatches()); n != 0 {
		t.Errorf("nothing may be dispatched when the run could never be identified, got %d", n)
	}
}

func assertNothingCollected(t *testing.T, outDir string) {
	t.Helper()
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("nothing should have been collected, found %s", e.Name())
	}
}
