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

// Options configures a dispatch.
type Options struct {
	// Repo is owner/name. Empty lets gh infer it from the checkout.
	Repo string
	// Ref is the branch or tag to run. Empty uses the current branch.
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
	if err := requireGH(); err != nil {
		return Result{}, err
	}

	// The newest run id *before* dispatching, so the new one can be identified without guessing.
	// `gh workflow run` prints nothing machine-readable and returns no id, which is the whole
	// difficulty here: polling for "the newest run" can otherwise latch onto a previous one that
	// is still in progress and report its result as this dispatch's.
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
	if out, err := runGH(args...); err != nil {
		return Result{}, fmt.Errorf("dispatch %s: %w\n%s", Workflow, err, out)
	}
	logf("dispatched %s; waiting for the run to appear", Workflow)

	runID, err := awaitNewRun(opts, before, logf)
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

// runIDPattern guards against a malformed id reaching a URL or a path. gh returns numeric ids;
// anything else means the output shape changed and should be reported, not interpolated.
var runIDPattern = regexp.MustCompile(`^[0-9]+$`)

type ghRun struct {
	DatabaseID int64  `json:"databaseId"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// listRuns returns the most recent runs of this workflow, newest first.
func listRuns(opts Options, limit int) ([]ghRun, error) {
	args := []string{"run", "list", "--workflow", Workflow,
		"--limit", fmt.Sprint(limit), "--json", "databaseId,status,conclusion,url"}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	out, err := runGH(args...)
	if err != nil {
		return nil, fmt.Errorf("list runs of %s: %w\n%s", Workflow, err, out)
	}
	var runs []ghRun
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return nil, fmt.Errorf("parse gh run list output: %w\n%s", err, out)
	}
	return runs, nil
}

func latestRunID(opts Options) (string, error) {
	runs, err := listRuns(opts, 1)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		// No history yet, which is normal on the first dispatch.
		return "", nil
	}
	return fmt.Sprint(runs[0].DatabaseID), nil
}

// awaitNewRun waits for a run id that differs from the one seen before dispatching.
//
// Identifying the run by "newest that is not the one already there" rather than by "newest" is
// what stops a still-running earlier dispatch being mistaken for this one -- which would report
// somebody else's verdict as yours.
func awaitNewRun(opts Options, before string, logf func(string, ...any)) (string, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		runs, err := listRuns(opts, 5)
		if err != nil {
			return "", err
		}
		for _, r := range runs {
			id := fmt.Sprint(r.DatabaseID)
			if id != before && runIDPattern.MatchString(id) {
				return id, nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return "", fmt.Errorf("no new %s run appeared within 5 minutes of dispatching; check "+
		"`gh run list --workflow %s`", Workflow, Workflow)
}

// awaitCompletion polls until the run finishes, returning GitHub's conclusion.
//
// A non-success conclusion is returned rather than raised: the job uploads artifacts even when
// the scenario fails, and those artifacts are exactly what a maintainer needs. The caller decides
// what a failure means after judging what came back.
func awaitCompletion(opts Options, runID string, logf func(string, ...any)) (string, error) {
	deadline := time.Now().Add(opts.Timeout)
	lastStatus := ""
	for time.Now().Before(deadline) {
		runs, err := listRuns(opts, 20)
		if err != nil {
			return "", err
		}
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
