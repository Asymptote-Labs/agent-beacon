package harness2session

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyRuns copies testdata/runs into a fresh directory, so a test can grow, rewrite or remove files
// without touching the fixtures, and returns the copy's root.
func copyRuns(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "runs")
	err := filepath.WalkDir(filepath.Join("testdata", "runs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join("testdata", "runs"), path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixtures: %v", err)
	}
	return dst
}

func listFixtures(t *testing.T, root string) map[string]TrajectoryRef {
	t.Helper()
	store, err := NewStore([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byRel := map[string]TrajectoryRef{}
	for _, ref := range refs {
		byRel[ref.Rel] = ref
	}
	return byRel
}

const (
	jbRel  = "jb/dataset/accountant/task3/model_traj/h2-final-1/accountant_task3_h2-final-1_attempt2_20261026T101500.jsonl"
	labRel = "lab/results/cx/antitrust-competition__t07__h2-cand-dom-1/codex_session.jsonl"
	wbRel  = "wb/office/jobs/2026-10-26__10-00-00/xlsx-budget__Ab3dE/agent/oc-output.txt"
)

// A runs directory holds far more JSONL than trajectories: graded results, a proposer's CLI output,
// task inputs. Only the executor streams are listed, recognized by their first event rather than by
// name, and the benchmark data Harness² copies around is not walked at all.
func TestListFindsOnlyExecutorTrajectories(t *testing.T) {
	refs := listFixtures(t, copyRuns(t))
	if len(refs) != 3 {
		names := []string{}
		for rel := range refs {
			names = append(names, rel)
		}
		t.Fatalf("listed %d trajectories, want 3: %v", len(refs), names)
	}
	for rel, want := range map[string]Format{jbRel: FormatOpenCode, labRel: FormatCodexExec, wbRel: FormatOpenCode} {
		ref, ok := refs[rel]
		if !ok {
			t.Fatalf("%s was not listed", rel)
		}
		if ref.Format != want {
			t.Errorf("%s format = %q, want %q", rel, ref.Format, want)
		}
		if !filepath.IsAbs(ref.Path) || ref.SizeBytes == 0 || ref.ModTimeUnixMS == 0 {
			t.Errorf("%s ref = %+v, want an absolute path, size and modification time", rel, ref)
		}
	}
}

// Each benchmark's layout names a different part of the rollout, and the completion marker beside a
// run names the harness that produced it.
func TestListReadsRunContextFromTheLayout(t *testing.T) {
	refs := listFixtures(t, copyRuns(t))
	for rel, want := range map[string]RunContext{
		jbRel:  {Benchmark: "jb", TaskID: "accountant/task3", Label: "h2-final-1", Attempt: 2},
		labRel: {Benchmark: "lab", RunID: "antitrust-competition__t07__h2-cand-dom-1", HarnessFingerprint: "9f2c4e1a7b3d5f60"},
		wbRel:  {Benchmark: "wb", Subset: "office", Trial: "xlsx-budget__Ab3dE", RunID: "wb-office-xlsx-budget-base", HarnessFingerprint: "base"},
	} {
		if got := refs[rel].Context; got != want {
			t.Errorf("%s context = %+v, want %+v", rel, got, want)
		}
	}
}

// A marker above the run directory belongs to some other run, and must not be read as this one's.
func TestRunContextIgnoresAMarkerAboveTheRun(t *testing.T) {
	root := copyRuns(t)
	if err := os.WriteFile(filepath.Join(root, "jb", markerName), []byte(`{"run_id":"elsewhere","harness":"0000"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := listFixtures(t, root)[jbRel].Context; got.RunID != "" || got.HarnessFingerprint != "" {
		t.Fatalf("context = %+v, want no marker fields", got)
	}
}

func TestSniffFormat(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		body string
		want Format
	}{
		"opencode":         {`{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{}}` + "\n", FormatOpenCode},
		"opencode-banner":  {"migrating...\n\n" + `{"type":"text","timestamp":1,"sessionID":"ses_1","part":{}}` + "\n", FormatOpenCode},
		"codex":            {`{"type":"thread.started","thread_id":"t"}` + "\n", FormatCodexExec},
		"codex-error-only": {`{"type":"error","message":"unauthorized"}` + "\n", FormatCodexExec},
		// An OpenCode-shaped type without a session id is not OpenCode's stream.
		"no-session":   {`{"type":"text","part":{}}` + "\n", ""},
		"result":       {`{"type":"result","subtype":"success"}` + "\n", ""},
		"not-an-event": {`{"score":1}` + "\n", ""},
		"broken-json":  {`{"type":"thread.started"` + "\n", ""},
		"plain-text":   {"hello\nworld\n", ""},
		// A first event still being written decides nothing yet; the file is looked at again later.
		"partial": {`{"type":"thread.started","thread_id":"t"}`, ""},
	} {
		path := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := sniffFormat(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != tc.want {
			t.Errorf("%s: format = %q, want %q", name, got, tc.want)
		}
	}
}

// Two roots where one is inside the other must not list a file twice: two cursors over one file
// would write each of its events twice.
func TestListDeduplicatesNestedRoots(t *testing.T) {
	root := copyRuns(t)
	store, err := NewStore([]string{root, filepath.Join(root, "lab"), root + string(filepath.Separator)})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Roots) != 2 {
		t.Fatalf("roots = %v, want the runs root once and its lab directory", store.Roots)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 {
		t.Fatalf("listed %d trajectories, want 3", len(refs))
	}
}

func TestListToleratesAMissingRootAndRefusesAFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	store, _ := NewStore([]string{missing})
	if refs, err := store.List(); err != nil || len(refs) != 0 {
		t.Fatalf("missing root: refs=%v err=%v, want none and no error", refs, err)
	}
	if store.Exists() {
		t.Fatal("Exists() = true for a missing root")
	}
	file := filepath.Join(t.TempDir(), "runs")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ = NewStore([]string{file})
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root: err = %v, want not a directory", err)
	}
}

func TestDefaultRunsDirsFollowsHarness2RunsDir(t *testing.T) {
	t.Setenv("HARNESS2_RUNS_DIR", "/srv/h2/runs")
	if got := DefaultRunsDirs(); len(got) != 1 || got[0] != "/srv/h2/runs" {
		t.Fatalf("DefaultRunsDirs() = %v, want [/srv/h2/runs]", got)
	}
	t.Setenv("HARNESS2_RUNS_DIR", "")
	home, _ := os.UserHomeDir()
	if got := DefaultRunsDirs(); len(got) != 1 || got[0] != filepath.Join(home, "harness2", "runs") {
		t.Fatalf("DefaultRunsDirs() = %v, want the checkout under the home directory", got)
	}
}

// A live stream ends in a line the executor is still writing; it is neither counted nor read.
func TestReadLeavesAPartialTailForLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := `{"type":"thread.started","thread_id":"t"}` + "\n" + "not json\n\n" + `{"type":"turn.started"}` + "\n" + `{"type":"item.comp`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &Store{}
	got, stats, err := store.Read(TrajectoryRef{Path: path, Format: FormatCodexExec})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.PartialTail || stats.Malformed != 1 {
		t.Fatalf("stats = %+v, want a partial tail and one malformed line", stats)
	}
	if got.LineCount != 4 || len(got.Lines) != 2 || got.Lines[1].Number != 4 {
		t.Fatalf("read %d lines (%d decoded, last #%d), want 4 lines with 2 decoded and the last numbered 4",
			got.LineCount, len(got.Lines), got.Lines[len(got.Lines)-1].Number)
	}
}

// A runs directory is often a symlink to a bigger disk. The walk must follow it: filepath.WalkDir
// alone visits only the link and finds nothing, while the store still reports itself present.
func TestListFollowsASymlinkedRunsDirectory(t *testing.T) {
	target := copyRuns(t)
	link := filepath.Join(t.TempDir(), "runs")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	refs := listFixtures(t, link)
	if len(refs) != 3 {
		t.Fatalf("listed %d trajectories through the symlink, want 3", len(refs))
	}
	if ref := refs[labRel]; ref.Context.RunID == "" || ref.Format != FormatCodexExec {
		t.Fatalf("lab ref through the symlink = %+v", ref)
	}
}

// One root that cannot be read is reported, and the others are still listed.
func TestListReportsABrokenRootAndKeepsTheRest(t *testing.T) {
	good := copyRuns(t)
	bad := filepath.Join(t.TempDir(), "runs")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := NewStore([]string{bad, good})
	refs, err := store.List()
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v, want the broken root named", err)
	}
	if len(refs) != 3 {
		t.Fatalf("listed %d trajectories, want the good root's 3", len(refs))
	}
}
