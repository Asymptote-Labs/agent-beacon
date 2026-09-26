package dispatch

import (
	"os"
	"path/filepath"
	"regexp"
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

// Selection is pure given a listing, so it is tested directly rather than through gh. The rules
// it pins each fixed a confident wrong answer: judging a *historical* run (the listing is
// newest-first and the fresh run usually does not exist on the first poll), and judging another
// concurrent invocation's run (#681).
func TestOwnRunSelectionRequiresTheCorrelationID(t *testing.T) {
	const ours = "0123456789abcdef01234567"
	const theirs = "fedcba9876543210fedcba98"
	titled := func(id int64, correlation string) ghRun {
		title := "Windows sandbox"
		if correlation != "" {
			title += " " + correlationMarker(correlation)
		}
		return ghRun{DatabaseID: id, DisplayTitle: title}
	}
	pick := func(t *testing.T, runs []ghRun, before int64) int64 {
		t.Helper()
		got, err := pickOwnRun(runs, before, ours)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return got
	}

	// A realistic listing at the moment of dispatch: history only, newest first, none of it ours.
	history := []ghRun{titled(500, ""), titled(499, ""), titled(498, "")}
	if got := pick(t, history, 500); got != 0 {
		t.Errorf("with only pre-existing runs the selection must find nothing, got %d", got)
	}

	// Once ours appears it is selected.
	withOurs := append([]ghRun{titled(501, ours)}, history...)
	if got := pick(t, withOurs, 500); got != 501 {
		t.Errorf("the run carrying our id must be selected, got %d", got)
	}

	// #681: another dispatch's run landed first. The old rule took the earliest new run, 501,
	// which is theirs.
	withRace := append([]ghRun{titled(502, ours), titled(501, theirs)}, history...)
	if got := pick(t, withRace, 500); got != 502 {
		t.Errorf("with two new runs ours is the one carrying our id, not the earlier one; got %d", got)
	}

	// Only another invocation's run, or a manual dispatch, has appeared: not ours, so keep waiting.
	foreign := append([]ghRun{titled(502, ""), titled(501, theirs)}, history...)
	if got := pick(t, foreign, 500); got != 0 {
		t.Errorf("a run without our id must never be selected, got %d", got)
	}

	// The marker matches the whole id, never a fragment of a longer one.
	fragment := []ghRun{{DatabaseID: 501, DisplayTitle: "Windows sandbox [" + ours + "ff]"},
		{DatabaseID: 502, DisplayTitle: "Windows sandbox [ff" + ours + "]"}}
	if got := pick(t, fragment, 500); got != 0 {
		t.Errorf("an id embedded in a longer one must not match, got %d", got)
	}

	// Carrying our id is not enough on its own: a run at or below the floor predates the dispatch.
	if got := pick(t, []ghRun{titled(500, ours)}, 500); got != 0 {
		t.Errorf("a run at the floor predates the dispatch and must not be selected, got %d", got)
	}

	// A first-ever dispatch has no history, so the floor is zero and our run qualifies.
	if got := pick(t, []ghRun{titled(7, ours)}, 0); got != 7 {
		t.Errorf("a zero floor must accept our first run, got %d", got)
	}
}

// Two runs carrying one dispatch's random id means something reused it, and either could be the
// run whose verdict would be reported. That is an explicit error, not a choice.
func TestOwnRunSelectionRefusesTwoRunsCarryingTheID(t *testing.T) {
	const ours = "0123456789abcdef01234567"
	runs := []ghRun{
		{DatabaseID: 503, DisplayTitle: "Windows sandbox " + correlationMarker(ours)},
		{DatabaseID: 501, DisplayTitle: "Windows sandbox " + correlationMarker(ours)},
		{DatabaseID: 500, DisplayTitle: "Windows sandbox"},
	}
	got, err := pickOwnRun(runs, 500, ours)
	if err == nil {
		t.Fatalf("two runs carrying one correlation id must be refused, got run %d", got)
	}
	if got != 0 {
		t.Errorf("an ambiguous selection must not also return a run, got %d", got)
	}
	for _, want := range []string{"501", "503", ours, "refusing to guess"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q: %v", want, err)
		}
	}
}

// Each dispatch needs an id no concurrent one shares, in a form that survives a run title.
func TestCorrelationIDsAreUniqueHex(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id, err := newCorrelationID()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(id) {
			t.Fatalf("correlation id %q is not 24 lowercase hex characters", id)
		}
		if seen[id] {
			t.Fatalf("correlation id %q repeated", id)
		}
		seen[id] = true
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
