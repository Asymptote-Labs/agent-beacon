// Package dispatch runs a scenario on a GitHub Actions Windows runner and brings the artifacts
// home.
//
// It is not a sandbox.Provider, and that is deliberate. Provider models a machine you can Exec
// against step by step; Actions is a batch substrate, and pretending otherwise would mean a
// remote-control channel and a relay to host it. Instead the whole harness runs *inside* the job
// against sandbox.LocalExec, and this package does the three things that have to happen outside
// it: start the job, wait for it, and fetch what it collected.
//
// Judging deliberately stays on this side. check is a pure function of what is on disk, so a
// dispatched Windows run is judged by the same code as a Modal Linux run -- and `verify`, `diff`
// and `--mutate` keep working on the result without knowing where it came from.
package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Workflow is the workflow file this package drives.
const Workflow = "windows-sandbox.yml"

// correlationInput is the workflow_dispatch input that ties a run to the dispatch that created it.
// The workflow puts it in its run-name, so the run's display title carries it from the moment the
// run exists.
const correlationInput = "correlation_id"

// Options configures a dispatch.
type Options struct {
	// Repo is owner/name. Empty lets gh infer it from the checkout.
	Repo string
	// Ref is the branch or tag to run.
	//
	// Empty is resolved to the current branch by Run, not left to gh -- `gh workflow run` with no
	// --ref uses the repository's *default* branch, so an unset value would silently run main while
	// the caller believed they were testing the branch they are on. That is not a hypothetical: the
	// doctor check that exists to catch exactly this class of mistake asserted the opposite.
	// Reported by Cursor Bugbot.
	Ref string
	// Scenario is the scenario id, or empty for every Windows scenario.
	Scenario string
	// ClaudeVersion pins the agent build.
	ClaudeVersion string
	// OutDir is where run directories are extracted, matching the local layout.
	OutDir string
	// PollInterval bounds how often the run is checked. Zero uses a sane default.
	PollInterval time.Duration
	// Timeout bounds the whole wait. Zero uses a sane default.
	Timeout time.Duration
	// Log receives progress lines.
	Log func(string, ...any)

	// newRunPoll and newRunTimeout bound the wait for the dispatched run to appear. Zero uses the
	// defaults; they are unexported because only tests have a reason to shorten them.
	newRunPoll    time.Duration
	newRunTimeout time.Duration
}

// Result describes a finished dispatch.
type Result struct {
	RunID string
	URL   string
	// Conclusion is GitHub's own word for the outcome: success, failure, cancelled, ...
	Conclusion string
	// RunDirs are the extracted run directories, ready for the check package.
	RunDirs []string
}

// Run dispatches the workflow, waits for it, and downloads its artifacts.
func Run(opts Options) (Result, error) {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 15 * time.Second
	}
	if opts.Timeout <= 0 {
		// The workflow's own timeout-minutes is 45; allow for queueing on top of it.
		opts.Timeout = 60 * time.Minute
	}
	if opts.newRunPoll <= 0 {
		opts.newRunPoll = 5 * time.Second
	}
	if opts.newRunTimeout <= 0 {
		opts.newRunTimeout = 5 * time.Minute
	}
	if err := requireGH(); err != nil {
		return Result{}, err
	}
	// Resolve the ref before dispatching so the run tests the branch the caller is on rather than
	// the default branch. Reported rather than silent, because which ref ran decides what the
	// verdict is about.
	if strings.TrimSpace(opts.Ref) == "" {
		branch, err := currentBranch()
		if err != nil {
			return Result{}, fmt.Errorf("could not determine the current branch to dispatch, and "+
				"leaving it unset would silently run the default branch instead: %w", err)
		}
		opts.Ref = branch
	}
	logf("dispatching against ref %s", opts.Ref)

	// `gh workflow run` prints nothing machine-readable and returns no id, which is the whole
	// difficulty here: the run this dispatch creates has to be recognized among runs other people
	// and other invocations create at the same time. So every dispatch carries an id of its own,
	// the workflow puts it in the run's title, and only a run bearing it is accepted as ours.
	//
	// Timing alone cannot do this. An earlier version took the earliest run created after a
	// snapshot of the newest id, and two callers that snapshot before either dispatch lands then
	// both pick the first run to appear: one judges the other's artifacts as its own verdict while
	// its own run goes unobserved (#681).
	correlationID, err := newCorrelationID()
	if err != nil {
		return Result{}, err
	}
	// The newest run id before dispatching is kept as a floor. It is no longer what identifies
	// our run, but a run older than the dispatch can never be ours, and the floor is what lets a
	// timeout report which runs appeared meanwhile without carrying our id.
	before, err := latestRunID(opts)
	if err != nil {
		return Result{}, err
	}

	args := []string{"workflow", "run", Workflow}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Ref != "" {
		args = append(args, "--ref", opts.Ref)
	}
	if opts.Scenario != "" {
		args = append(args, "-f", "scenario="+opts.Scenario)
	}
	if opts.ClaudeVersion != "" {
		args = append(args, "-f", "claude_version="+opts.ClaudeVersion)
	}
	args = append(args, "-f", correlationInput+"="+correlationID)
	if out, err := runGH(args...); err != nil {
		// GitHub reads the workflow from the dispatched ref, so a branch cut before the workflow
		// declared the correlation input rejects it. Refused rather than retried without it: an
		// uncorrelated dispatch is exactly the one that can attach to someone else's run.
		if strings.Contains(out, "Unexpected inputs") && strings.Contains(out, correlationInput) {
			return Result{}, fmt.Errorf("dispatch %s: the workflow on ref %s does not declare the "+
				"%s input, so its run could not be told apart from a concurrent dispatch's; "+
				"merge or rebase onto main so the ref carries the current workflow: %w\n%s",
				Workflow, opts.Ref, correlationInput, err, out)
		}
		return Result{}, fmt.Errorf("dispatch %s: %w\n%s", Workflow, err, out)
	}
	logf("dispatched %s (correlation id %s); waiting for the run to appear", Workflow, correlationID)

	runID, err := awaitNewRun(opts, before, correlationID, logf)
	if err != nil {
		return Result{}, err
	}
	res := Result{RunID: runID, URL: runURL(opts.Repo, runID)}
	logf("run %s: %s", runID, res.URL)

	conclusion, err := awaitCompletion(opts, runID, logf)
	res.Conclusion = conclusion
	if err != nil {
		return res, err
	}
	logf("run %s concluded: %s", runID, conclusion)

	dirs, err := download(opts, runID, logf)
	res.RunDirs = dirs
	if err != nil {
		return res, err
	}
	// A concluded run with no artifacts is not a pass. The job uploads unconditionally, so this
	// means it failed before collecting anything -- and reporting that as "nothing to judge, all
	// clear" is exactly the false green the tool exists to prevent.
	if len(dirs) == 0 {
		return res, fmt.Errorf("run %s produced no run directories (%s), so there is nothing to "+
			"judge; read the job log at %s", runID, conclusion, res.URL)
	}
	return res, nil
}

// currentBranch reports the checked-out branch name.
//
// A detached HEAD has no branch to dispatch, and is reported rather than guessed at: workflow_dispatch
// takes a ref by name, so there is nothing sensible to substitute.
func currentBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("HEAD is detached, so there is no branch to dispatch; pass --ref explicitly")
	}
	return branch, nil
}

// requireGH fails early with the fix rather than letting exec produce "file not found".
func requireGH() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is required to dispatch a Windows run; " +
			"install it and run `gh auth login`")
	}
	if out, err := runGH("auth", "status"); err != nil {
		return fmt.Errorf("gh is not authenticated: %w\n%s", err, out)
	}
	return nil
}

func runGH(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// firstLine keeps a retry message to one line. A gh failure can be several lines of connection
// advice, and repeating all of it on every poll would bury the progress it is interleaved with.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// runIDPattern guards against a malformed id reaching a URL or a path. gh returns numeric ids;
// anything else means the output shape changed and should be reported, not interpolated.
var runIDPattern = regexp.MustCompile(`^[0-9]+$`)

type ghRun struct {
	DatabaseID int64  `json:"databaseId"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	// DisplayTitle is the run's rendered run-name, which carries the correlation id.
	DisplayTitle string `json:"displayTitle"`
}

// newCorrelationID returns an id no other dispatch will carry.
//
// Random rather than derived from the time or the process: two callers on different machines can
// start in the same instant, and the id has to differ precisely then.
func newCorrelationID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate a dispatch correlation id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// correlationMarker is how the workflow's run-name renders a correlation id in the run's title.
// Bracketed so the match is on the whole id, never on a fragment of a longer word.
func correlationMarker(id string) string {
	return "[" + id + "]"
}

// listRuns returns the most recent runs of this workflow, newest first.
func listRuns(opts Options, limit int) ([]ghRun, error) {
	args := []string{"run", "list", "--workflow", Workflow,
		"--limit", fmt.Sprint(limit), "--json", "databaseId,status,conclusion,url,displayTitle"}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	out, err := runGH(args...)
	if err != nil {
		// displayTitle is what carries the correlation id, and a gh too old to know the field
		// rejects the whole listing. Said plainly, because gh's own message does not suggest it.
		if strings.Contains(out, "Unknown JSON field") && strings.Contains(out, "displayTitle") {
			return nil, fmt.Errorf("list runs of %s: this gh does not support the displayTitle "+
				"field that identifies a dispatch's run; upgrade gh: %w\n%s", Workflow, err, out)
		}
		return nil, fmt.Errorf("list runs of %s: %w\n%s", Workflow, err, out)
	}
	var runs []ghRun
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return nil, fmt.Errorf("parse gh run list output: %w\n%s", err, out)
	}
	return runs, nil
}

// latestRunID returns the highest existing run id, or 0 when there is no history.
//
// The maximum rather than the first listed: the ordering is documented as newest-first but this
// value is the floor every later comparison rests on, and taking the max is correct even if the
// ordering ever changes.
func latestRunID(opts Options) (int64, error) {
	runs, err := listRuns(opts, 10)
	if err != nil {
		return 0, err
	}
	// Zero is the correct floor when there is no history, which is normal on a first dispatch:
	// every real run id is greater than it.
	return maxRunID(runs), nil
}

// awaitNewRun waits for the run this dispatch created, identified by its correlation id.
//
// A run is ours only if its title carries our id; being new is necessary but not sufficient. Two
// earlier rules each produced a confident wrong answer rather than an error:
//
//   - Accepting the first listed run whose id merely differed from the pre-dispatch newest. The
//     listing is newest-first and the fresh run usually does not exist on the first poll, so a
//     *historical* run was returned and its stale verdict reported as this run's. Reported by
//     Cursor Bugbot. The floor below still rules that out.
//   - Accepting the earliest run above that floor. Two callers that snapshot the same floor before
//     either dispatch lands both select the first new run, so one of them judges the other's
//     artifacts while its own run is never matched (#681).
//
// Runs that appear without our id are someone else's and are waited past, not taken. If our id
// never shows up, the error names them rather than guessing which, if any, was ours.
func awaitNewRun(opts Options, before int64, correlationID string, logf func(string, ...any)) (string, error) {
	deadline := time.Now().Add(opts.newRunTimeout)
	var lastErr error
	var others []int64
	for time.Now().Before(deadline) {
		runs, err := listRuns(opts, 20)
		if err != nil {
			// Transient rather than fatal. A single failed API call used to abandon a run that was
			// proceeding perfectly well, and the dispatch reported failure while the workflow it had
			// just started went on to finish unobserved. Retried until the deadline, with the last
			// error kept so a persistent outage still reports the real cause rather than a timeout.
			lastErr = err
			logf("could not list runs (%v); retrying", firstLine(err.Error()))
			time.Sleep(opts.newRunPoll)
			continue
		}
		lastErr = nil
		found, err := pickOwnRun(runs, before, correlationID)
		if err != nil {
			return "", err
		}
		if found > 0 {
			return fmt.Sprint(found), nil
		}
		others = newerRuns(runs, before)
		time.Sleep(opts.newRunPoll)
	}
	if lastErr != nil {
		return "", fmt.Errorf("could not reach the GitHub API while waiting for the %s run with "+
			"correlation id %s: %w", Workflow, correlationID, lastErr)
	}
	if len(others) > 0 {
		// Explicitly ambiguous rather than resolved by timing. One of these may be ours with a
		// title the workflow failed to stamp, or all of them may be other dispatches while ours is
		// still not listed; nothing here can tell which, and picking one is how a caller ends up
		// judging another invocation's artifacts.
		return "", fmt.Errorf("no %s run carrying correlation id %s appeared within %s of "+
			"dispatching, but %d other run(s) did (%s); refusing to guess which, if any, is this "+
			"dispatch's. Check `gh run list --workflow %s` and that the dispatched ref's workflow "+
			"puts inputs.%s in its run-name", Workflow, correlationID, opts.newRunTimeout,
			len(others), joinIDs(others), Workflow, correlationInput)
	}
	return "", fmt.Errorf("no new %s run appeared within %s of dispatching; check "+
		"`gh run list --workflow %s`", Workflow, opts.newRunTimeout, Workflow)
}

// pickOwnRun returns the run created after the floor whose title carries the correlation id, or
// 0 if it has not appeared yet.
//
// Separated from the polling loop so the selection rule -- the part that was wrong, twice -- is
// testable without a GitHub account or a live workflow. More than one run carrying the id is
// reported rather than resolved: the id is random per dispatch, so that means something other
// than this process used it, and either run could be the one whose verdict we would report.
func pickOwnRun(runs []ghRun, before int64, correlationID string) (int64, error) {
	marker := correlationMarker(correlationID)
	var matches []int64
	for _, r := range runs {
		if r.DatabaseID <= before || !strings.Contains(r.DisplayTitle, marker) {
			continue
		}
		matches = append(matches, r.DatabaseID)
	}
	switch len(matches) {
	case 0:
		return 0, nil
	case 1:
		return matches[0], nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i] < matches[j] })
	return 0, fmt.Errorf("%d %s runs (%s) carry correlation id %s, which should belong to exactly "+
		"one dispatch; refusing to guess which one to judge", len(matches), Workflow,
		joinIDs(matches), correlationID)
}

// newerRuns lists the runs created after the floor, oldest first, for an ambiguity report.
func newerRuns(runs []ghRun, before int64) []int64 {
	var ids []int64
	for _, r := range runs {
		if r.DatabaseID > before {
			ids = append(ids, r.DatabaseID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ", ")
}

// maxRunID is the floor "created after our dispatch" is measured against.
func maxRunID(runs []ghRun) int64 {
	var newest int64
	for _, r := range runs {
		if r.DatabaseID > newest {
			newest = r.DatabaseID
		}
	}
	return newest
}

// awaitCompletion polls until the run finishes, returning GitHub's conclusion.
//
// A non-success conclusion is returned rather than raised: the job uploads artifacts even when
// the scenario fails, and those artifacts are exactly what a maintainer needs. The caller decides
// what a failure means after judging what came back.
func awaitCompletion(opts Options, runID string, logf func(string, ...any)) (string, error) {
	deadline := time.Now().Add(opts.Timeout)
	lastStatus := ""
	var lastErr error
	for time.Now().Before(deadline) {
		runs, err := listRuns(opts, 20)
		if err != nil {
			// Same reasoning as awaitNewRun: a blip on the way to api.github.com must not discard a
			// run that is doing fine. This loop can be waiting 20+ minutes, so the odds of hitting
			// one are correspondingly higher.
			lastErr = err
			logf("could not list runs (%v); retrying", firstLine(err.Error()))
			time.Sleep(opts.PollInterval)
			continue
		}
		lastErr = nil
		for _, r := range runs {
			if fmt.Sprint(r.DatabaseID) != runID {
				continue
			}
			if r.Status != lastStatus {
				logf("run %s is %s", runID, r.Status)
				lastStatus = r.Status
			}
			if r.Status == "completed" {
				return r.Conclusion, nil
			}
		}
		time.Sleep(opts.PollInterval)
	}
	if lastErr != nil {
		return "", fmt.Errorf("could not reach the GitHub API while waiting for run %s (still "+
			"running at %s): %w", runID, runURL(opts.Repo, runID), lastErr)
	}
	return "", fmt.Errorf("run %s did not complete within %s; it may still be going at %s",
		runID, opts.Timeout, runURL(opts.Repo, runID))
}

// download fetches the run's artifacts into OutDir and returns the extracted run directories.
//
// The workflow uploads `beacon-sandbox/runs/` as one artifact, so extraction yields the same
// `<scenario>-<token>/` directories a local run would have produced. That sameness is the point:
// check, verify and diff then work on a dispatched run without knowing it was dispatched.
func download(opts Options, runID string, logf func(string, ...any)) ([]string, error) {
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return nil, err
	}
	// Into a staging directory first. gh extracts artifact contents directly into --dir, and
	// unpacking straight into OutDir would interleave with existing run directories, making it
	// impossible to tell which ones this dispatch produced.
	stage, err := os.MkdirTemp(opts.OutDir, ".dispatch-"+runID+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	args := []string{"run", "download", runID, "--dir", stage}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if out, err := runGH(args...); err != nil {
		// A run that failed before uploading has no artifacts, and gh treats that as an error.
		// Reported as "nothing collected" rather than as a download fault, because the two send a
		// reader to completely different places.
		if strings.Contains(out, "no artifacts") || strings.Contains(out, "not found") {
			logf("run %s uploaded no artifacts", runID)
			return nil, nil
		}
		return nil, fmt.Errorf("download artifacts of run %s: %w\n%s", runID, err, out)
	}

	dirs, err := findRunDirs(stage)
	if err != nil {
		return nil, err
	}

	var moved []string
	for _, dir := range dirs {
		dest := filepath.Join(opts.OutDir, filepath.Base(dir))
		// Remove any previous extraction of the same run directory so a rename cannot fail
		// halfway and leave two partial copies.
		_ = os.RemoveAll(dest)
		if err := os.Rename(dir, dest); err != nil {
			return moved, fmt.Errorf("move collected run %s: %w", filepath.Base(dir), err)
		}
		moved = append(moved, dest)
		logf("collected %s", dest)
	}
	return moved, nil
}

// findRunDirs locates collected run directories by looking for the runtime log, at any depth.
//
// By content rather than by path shape. The artifact name forms one directory level and the
// uploaded path may add more, so hardcoding the nesting would break the first time the workflow's
// upload path changed -- and it would break silently, as "the run collected nothing", which is the
// one failure this tool must never report when something was in fact collected.
func findRunDirs(stage string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "runtime.jsonl" {
			return err
		}
		dirs = append(dirs, filepath.Dir(p))
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

func runURL(repo, runID string) string {
	if repo == "" {
		return "the run page (see `gh run view " + runID + " --web`)"
	}
	return fmt.Sprintf("https://github.com/%s/actions/runs/%s", repo, runID)
}
