package dispatch

// A fake GitHub, reached through a fake `gh`, so Run can be exercised end to end without an
// account, a network, or a real workflow.
//
// Run shells out to `gh` rather than calling the REST API, so the fake has to sit at that seam.
// The test binary doubles as the `gh` executable: it is copied into a temporary directory as `gh`,
// that directory is put first on PATH, and when it is invoked with fakeGHEnv set, TestMain
// forwards its arguments to an httptest server and replays the answer. The server holds the state
// a real repository would -- the run history, which inputs the workflow declares, the display title
// each run gets -- so concurrent callers observe one shared, consistent GitHub, exactly as two
// maintainers dispatching at once would.
//
// Nothing in the production code knows the fake exists. That is deliberate: the race this pins
// (#681) lives in the order of real gh calls, so the test drives those calls rather than a seam
// substituted for them.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const fakeGHEnv = "BEACON_SANDBOX_FAKE_GH_URL"

// fakeGHDir holds the one copy of this test binary installed as `gh`, made once per package run
// rather than once per test.
var fakeGHDir string

func TestMain(m *testing.M) {
	if url := os.Getenv(fakeGHEnv); url != "" {
		os.Exit(runFakeGH(url, os.Args[1:]))
	}
	dir, err := installFakeGH()
	if err != nil {
		fmt.Fprintln(os.Stderr, "install fake gh:", err)
		os.Exit(2)
	}
	fakeGHDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// installFakeGH copies this test binary into a fresh directory under the name gh.
func installFakeGH() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "beacon-sandbox-fake-gh-")
	if err != nil {
		return "", err
	}
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	return dir, dst.Close()
}

type fakeGHRequest struct {
	Args []string `json:"args"`
}

type fakeGHResponse struct {
	Stdout string `json:"stdout"`
	Exit   int    `json:"exit"`
	// Files are written under the --dir argument, which is how `gh run download` delivers.
	Files map[string]string `json:"files,omitempty"`
}

// runFakeGH is the whole of the fake executable: forward, replay, exit.
func runFakeGH(url string, args []string) int {
	body, _ := json.Marshal(fakeGHRequest{Args: args})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake gh:", err)
		return 2
	}
	defer resp.Body.Close()
	var out fakeGHResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fmt.Fprintln(os.Stderr, "fake gh: decode:", err)
		return 2
	}
	if len(out.Files) > 0 {
		dir := flagValue(args, "--dir")
		for rel, content := range out.Files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				fmt.Fprintln(os.Stderr, "fake gh:", err)
				return 2
			}
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "fake gh:", err)
				return 2
			}
		}
	}
	fmt.Fprint(os.Stdout, out.Stdout)
	return out.Exit
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func flagValues(args []string, name string) []string {
	var vals []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			vals = append(vals, args[i+1])
		}
	}
	return vals
}

// fakeRun is one workflow run as the fake repository records it.
type fakeRun struct {
	ID           int64
	Status       string
	Conclusion   string
	DisplayTitle string
	Inputs       map[string]string
	// hiddenFor counts the listings the run is still invisible to, standing in for the delay
	// between a dispatch being accepted and its run appearing in the API.
	hiddenFor int
	// listsUntilDone counts the listings that report it in progress before it completes.
	listsUntilDone int
}

// fakeGitHub is the shared state behind every fake gh invocation.
type fakeGitHub struct {
	mu     sync.Mutex
	nextID int64
	runs   []*fakeRun
	// declared is the set of workflow_dispatch inputs the dispatched workflow accepts. GitHub
	// rejects any other input with a 422, which is what dispatching a ref whose workflow predates
	// an input looks like.
	declared map[string]bool
	// dispatched records each accepted dispatch, in the order GitHub assigned ids.
	dispatched []*fakeRun
	foreign    []int64

	// Hooks run outside the lock, so a test can hold one caller at a point in its sequence until
	// another caller catches up -- which is how an interleaving is forced rather than hoped for.
	beforeList     func()
	beforeDispatch func()
	afterDispatch  func(run *fakeRun)

	// hideNewRunsFor and runListsUntilDone shape every run created by a dispatch.
	hideNewRunsFor    int
	runListsUntilDone int
	// noRunName models a dispatched ref whose workflow accepts the correlation input but does not
	// render it into run-name, so every run is titled with the bare workflow name.
	noRunName bool
	// failLists makes that many listings fail the way a network blip does.
	failLists int
	// oldGH models a gh release that predates the displayTitle JSON field.
	oldGH bool
}

// newFakeGitHub starts the fake and puts a `gh` that talks to it first on PATH.
//
// configure, if not nil, shapes the fake before its server starts: the hooks and knobs are read
// by the server's goroutines, and setting them afterwards would be a data race.
//
// The history is a few completed runs so that the floor Run snapshots is a real, non-zero id --
// the situation the race needs, since both callers read the same floor. By default the fake
// accepts exactly the inputs this checkout's workflow declares.
func newFakeGitHub(t *testing.T, configure func(*fakeGitHub)) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{nextID: 501, declared: map[string]bool{}}
	for _, in := range workflowInputs(t) {
		f.declared[in] = true
	}
	for id := int64(498); id <= 500; id++ {
		f.runs = append(f.runs, &fakeRun{ID: id, Status: "completed", Conclusion: "success",
			DisplayTitle: "Windows sandbox"})
	}
	if configure != nil {
		configure(f)
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)

	t.Setenv("PATH", fakeGHDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeGHEnv, srv.URL)
	// Under -race every gh invocation is a race-enabled copy of this binary, and those sleep a
	// second on exit by default. Harmless, but it turns a sub-second test into a slow one.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return f
}

// workflowInputs reads the inputs the real workflow declares, so the fake accepts exactly what
// GitHub would for a ref carrying this checkout's workflow.
func workflowInputs(t *testing.T) []string {
	t.Helper()
	wf := loadWorkflow(t)
	var names []string
	for name := range wf.On.WorkflowDispatch.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	var req fakeGHRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(f.handle(req.Args))
}

func (f *fakeGitHub) handle(args []string) fakeGHResponse {
	switch {
	case len(args) >= 2 && args[0] == "auth" && args[1] == "status":
		return fakeGHResponse{Stdout: "Logged in to github.com as fake\n"}
	case len(args) >= 3 && args[0] == "workflow" && args[1] == "run":
		return f.dispatch(args)
	case len(args) >= 2 && args[0] == "run" && args[1] == "list":
		return f.list(args)
	case len(args) >= 3 && args[0] == "run" && args[1] == "download":
		return f.download(args)
	}
	return fakeGHResponse{Stdout: fmt.Sprintf("fake gh: unsupported command %q\n", args), Exit: 1}
}

func (f *fakeGitHub) dispatch(args []string) fakeGHResponse {
	if args[2] != Workflow {
		return fakeGHResponse{Stdout: "could not find workflow " + args[2] + "\n", Exit: 1}
	}
	inputs := map[string]string{}
	var unexpected []string
	for _, kv := range flagValues(args, "-f") {
		k, v, _ := strings.Cut(kv, "=")
		inputs[k] = v
		if !f.declared[k] {
			unexpected = append(unexpected, k)
		}
	}
	if len(unexpected) > 0 {
		// The shape of GitHub's real refusal, as gh prints it.
		quoted, _ := json.Marshal(unexpected)
		return fakeGHResponse{Exit: 1, Stdout: "could not create workflow dispatch event: HTTP 422: " +
			"Unexpected inputs provided: " + string(quoted) + "\n"}
	}
	if f.beforeDispatch != nil {
		f.beforeDispatch()
	}
	f.mu.Lock()
	run := &fakeRun{
		ID:             f.nextID,
		Status:         "queued",
		DisplayTitle:   renderRunName(f.declared, inputs, f.noRunName),
		Inputs:         inputs,
		hiddenFor:      f.hideNewRunsFor,
		listsUntilDone: f.runListsUntilDone,
	}
	f.nextID++
	f.runs = append(f.runs, run)
	f.dispatched = append(f.dispatched, run)
	f.mu.Unlock()
	if f.afterDispatch != nil {
		f.afterDispatch(run)
	}
	return fakeGHResponse{Stdout: "Created workflow_dispatch event for " + Workflow + "\n"}
}

// addForeignRun records a run this process did not dispatch: another maintainer's, or a manual
// dispatch from the Actions tab.
func (f *fakeGitHub) addForeignRun(title string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := &fakeRun{ID: f.nextID, Status: "in_progress", DisplayTitle: title}
	f.nextID++
	f.runs = append(f.runs, run)
	f.foreign = append(f.foreign, run.ID)
	return run.ID
}

// foreignRuns reports the ids addForeignRun created. Read under the lock because the hooks that
// add them run on the server's goroutine.
func (f *fakeGitHub) foreignRuns() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.foreign...)
}

// renderRunName evaluates the workflow's run-name the way Actions would for these inputs. It only
// applies when the workflow declares the correlation input; a ref whose workflow does not has no
// run-name, and GitHub then titles a dispatched run with the workflow's name.
func renderRunName(declared map[string]bool, inputs map[string]string, noRunName bool) string {
	if !noRunName && declared[correlationInput] && inputs[correlationInput] != "" {
		return "Windows sandbox [" + inputs[correlationInput] + "]"
	}
	return "Windows sandbox"
}

func (f *fakeGitHub) list(args []string) fakeGHResponse {
	if flagValue(args, "--workflow") != Workflow {
		return fakeGHResponse{Stdout: "fake gh: wrong workflow\n", Exit: 1}
	}
	limit, err := strconv.Atoi(flagValue(args, "--limit"))
	if err != nil {
		return fakeGHResponse{Stdout: "fake gh: bad --limit\n", Exit: 1}
	}
	fields := strings.Split(flagValue(args, "--json"), ",")
	if f.beforeList != nil {
		f.beforeList()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failLists > 0 {
		f.failLists--
		return fakeGHResponse{Exit: 1, Stdout: "error connecting to api.github.com\ncheck your internet connection\n"}
	}
	var visible []*fakeRun
	for _, r := range f.runs {
		if r.hiddenFor > 0 {
			r.hiddenFor--
			continue
		}
		visible = append(visible, r)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].ID > visible[j].ID })
	if len(visible) > limit {
		visible = visible[:limit]
	}
	out := make([]map[string]any, 0, len(visible))
	for _, r := range visible {
		// Advanced after being reported, so a caller sees each state at least once.
		row := map[string]any{}
		for _, field := range fields {
			switch field {
			case "databaseId":
				row[field] = r.ID
			case "status":
				row[field] = r.Status
			case "conclusion":
				row[field] = r.Conclusion
			case "url":
				row[field] = fmt.Sprintf("https://github.com/fake/fake/actions/runs/%d", r.ID)
			case "displayTitle":
				if f.oldGH {
					return fakeGHResponse{Stdout: "Unknown JSON field: \"displayTitle\"\n", Exit: 1}
				}
				row[field] = r.DisplayTitle
			default:
				// gh rejects an unknown --json field outright; so does the fake, so a typo in the
				// production field list cannot pass here and fail against the real gh.
				return fakeGHResponse{Stdout: "Unknown JSON field: " + field + "\n", Exit: 1}
			}
		}
		out = append(out, row)
		if r.Status != "completed" && r.Inputs != nil {
			if r.listsUntilDone > 0 {
				r.listsUntilDone--
				r.Status = "in_progress"
			} else {
				r.Status, r.Conclusion = "completed", "success"
			}
		}
	}
	body, _ := json.Marshal(out)
	return fakeGHResponse{Stdout: string(body)}
}

func (f *fakeGitHub) download(args []string) fakeGHResponse {
	id, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return fakeGHResponse{Stdout: "fake gh: bad run id\n", Exit: 1}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.runs {
		if r.ID != id || r.Inputs == nil {
			continue
		}
		// Named the way the harness names a run directory, and carrying which dispatch produced
		// it, so a test can tell whose artifacts a caller brought home.
		dir := fmt.Sprintf("beacon-sandbox-runs/%s-%d", r.Inputs["scenario"], r.ID)
		return fakeGHResponse{Stdout: "", Files: map[string]string{
			dir + "/runtime.jsonl": fmt.Sprintf(`{"run":%d,"scenario":%q}`+"\n", r.ID, r.Inputs["scenario"]),
		}}
	}
	return fakeGHResponse{Stdout: "no artifacts found for run\n", Exit: 1}
}

// runFor reports the run GitHub created for the dispatch of this scenario.
func (f *fakeGitHub) runFor(scenario string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.dispatched {
		if r.Inputs["scenario"] == scenario {
			return r.ID
		}
	}
	return 0
}

// dispatches returns a copy of each accepted dispatch's inputs, in id order.
func (f *fakeGitHub) dispatches() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]string, len(f.dispatched))
	for i, r := range f.dispatched {
		cp := map[string]string{}
		for k, v := range r.Inputs {
			cp[k] = v
		}
		out[i] = cp
	}
	return out
}
