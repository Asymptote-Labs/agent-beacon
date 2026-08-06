package dispatch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Run directories are found by content, not by path shape. The artifact name forms one level and
// the uploaded path may add more, so a hardcoded nesting would break the first time the workflow's
// upload path changed -- and it would break as "the run collected nothing", which is the one thing
// this tool must never report when something was in fact collected.
func TestFindRunDirsLocatesLogsAtAnyDepth(t *testing.T) {
	stage := t.TempDir()
	want := []string{
		filepath.Join(stage, "beacon-sandbox-runs", "w00-probe-abc12345"),
		filepath.Join(stage, "beacon-sandbox-runs", "nested", "deeper", "w00-probe-def67890"),
	}
	for _, dir := range want {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with other artifacts but no runtime log is not a run directory: judging it would
	// fail on a missing log rather than reporting that nothing was collected.
	decoy := filepath.Join(stage, "beacon-sandbox-runs", "no-log")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := findRunDirs(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("found %d run dirs, want %d: %v", len(got), len(want), got)
	}
	// Sorted, because filesystem walk order is not guaranteed and a run's output should not depend
	// on it. Comparing against a sorted expectation is what pins that.
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("run dir %d = %q, want %q", i, got[i], want[i])
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("results must be sorted so a run is reproducible, got %v", got)
	}
}

// An empty staging directory must be an empty result rather than an error: a run that failed
// before uploading is a real outcome the caller reports specifically.
func TestFindRunDirsToleratesNothingCollected(t *testing.T) {
	got, err := findRunDirs(t.TempDir())
	if err != nil {
		t.Fatalf("an empty staging directory must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no run dirs, got %v", got)
	}
}

// The bug this pins is one that produced a confident wrong answer rather than an error: the run
// listing is newest-first, and the freshly dispatched run usually does not exist on the first poll,
// so accepting "the first id that differs from the pre-dispatch id" returned the second-newest
// *historical* run. The dispatch then judged a previous run's artifacts as this run's result.
//
// Selection is pure given a listing, so it is tested directly rather than through gh.
func TestNewRunSelectionRequiresAnIDGreaterThanTheFloor(t *testing.T) {
	// A realistic listing at the moment of dispatch: history only, newest first, none of it ours.
	history := []ghRun{{DatabaseID: 500}, {DatabaseID: 499}, {DatabaseID: 498}}
	if got := pickNewRun(history, 500); got != 0 {
		t.Errorf("with only pre-existing runs the selection must find nothing, got %d", got)
	}
	// The specific regression: 499 differs from 500 and is listed, but predates the dispatch.
	if got := pickNewRun(history, 500); got == 499 {
		t.Error("an older run must never be selected; that is the stale-verdict bug")
	}

	// Once ours appears it is selected.
	withOurs := append([]ghRun{{DatabaseID: 501}}, history...)
	if got := pickNewRun(withOurs, 500); got != 501 {
		t.Errorf("the run created after the floor must be selected, got %d", got)
	}

	// Two dispatches racing: follow the earlier new run, which is ours.
	withRace := append([]ghRun{{DatabaseID: 502}, {DatabaseID: 501}}, history...)
	if got := pickNewRun(withRace, 500); got != 501 {
		t.Errorf("with two new runs the earlier one is ours, got %d", got)
	}

	// A first-ever dispatch has no history, so the floor is zero and any real run qualifies.
	if got := pickNewRun([]ghRun{{DatabaseID: 7}}, 0); got != 7 {
		t.Errorf("a zero floor must accept the first run, got %d", got)
	}
}

// The floor is the maximum, not the first element: it is what every later comparison rests on, so
// it stays correct even if the listing order ever changes.
func TestRunFloorIsTheMaximumID(t *testing.T) {
	if got := maxRunID([]ghRun{{DatabaseID: 3}, {DatabaseID: 9}, {DatabaseID: 5}}); got != 9 {
		t.Errorf("floor = %d, want 9", got)
	}
	if got := maxRunID(nil); got != 0 {
		t.Errorf("no history must yield a zero floor, got %d", got)
	}
}

// gh returns numeric run ids. Anything else means the output shape changed, and interpolating it
// into a URL or a filesystem path would be worse than reporting it.
func TestRunIDPatternRejectsAnythingButDigits(t *testing.T) {
	for _, ok := range []string{"1", "18234567890"} {
		if !runIDPattern.MatchString(ok) {
			t.Errorf("%q should be accepted as a run id", ok)
		}
	}
	for _, bad := range []string{"", "abc", "12a", "../../etc", "12 34", "-1"} {
		if runIDPattern.MatchString(bad) {
			t.Errorf("%q must not be accepted as a run id", bad)
		}
	}
}

// The URL is what a reader follows to see why a run failed, so it must be a real link when the
// repository is known and an honest instruction when it is not -- never a half-built URL.
func TestRunURL(t *testing.T) {
	got := runURL("Asymptote-Labs/agent-beacon", "42")
	if got != "https://github.com/Asymptote-Labs/agent-beacon/actions/runs/42" {
		t.Errorf("runURL with a repo = %q", got)
	}
	got = runURL("", "42")
	if strings.Contains(got, "https://github.com//") {
		t.Errorf("an unknown repo must not produce a malformed URL, got %q", got)
	}
	if !strings.Contains(got, "42") {
		t.Errorf("the fallback must still identify the run, got %q", got)
	}
}
