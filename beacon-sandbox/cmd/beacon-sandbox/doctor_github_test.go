package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The disposability gate is the one thing standing between `--provider local` and a scenario
// installing Beacon on somebody's workstation, so its three outcomes are pinned here.
func TestLocalChecksRefuseAnUnprovableHost(t *testing.T) {
	cases := []struct {
		name       string
		githubEnv  string
		runnerEnv  string
		wantStatus checkStatus
	}{
		// Not CI at all: a developer's machine, and a scenario would install for real there.
		{"workstation", "", "", statusFail},
		// Self-hosted runners persist between jobs, so an install outlives the run -- the same
		// escape the host guard exists to catch, just relocated.
		{"self-hosted runner", "true", "self-hosted", statusWarn},
		{"github-hosted runner", "true", "github-hosted", statusOK},
		// An unreported environment is the older Actions behavior; treated as acceptable because
		// GITHUB_ACTIONS is already the stronger signal.
		{"unreported environment", "true", "", statusOK},
	}
	for _, c := range cases {
		t.Setenv("GITHUB_ACTIONS", c.githubEnv)
		t.Setenv("RUNNER_ENVIRONMENT", c.runnerEnv)

		got := localChecks()
		if len(got) != 1 {
			t.Fatalf("%s: expected exactly one check, got %d", c.name, len(got))
		}
		if got[0].Status != c.wantStatus {
			t.Errorf("%s: status = %q, want %q (detail: %s)",
				c.name, got[0].Status, c.wantStatus, got[0].Detail)
		}
		// A failing or warning check with no fix is not actionable, which is the property the
		// whole doctor output rests on.
		if c.wantStatus != statusOK && got[0].Fix == "" {
			t.Errorf("%s: a non-ok check must carry a fix", c.name)
		}
	}
}

// A dispatch runs a pushed ref, not the working tree. That makes uncommitted work silently not
// under test -- the same class of wasted investigation the stale-collector warning prevents -- so
// it has to be reported, and reported as satisfiable by doing what it asks.
func TestDispatchRefCheckWarnsWhenTheWorkingTreeIsNotWhatWouldRun(t *testing.T) {
	root := newTestRepo(t)

	// Clean tree with no upstream: nothing is knowably missing from a dispatch.
	if got := dispatchRefCheck(root); got.Status != statusOK {
		t.Errorf("a clean tree should pass, got %q: %s", got.Status, got.Detail)
	}

	// An edit under a watched tree is not in the pushed ref.
	if err := os.WriteFile(filepath.Join(root, "cli", "beacon", "main.go"),
		[]byte("package main // edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := dispatchRefCheck(root)
	if got.Status != statusWarn {
		t.Errorf("an uncommitted change under cli/ must warn, got %q: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "NOT under test") {
		t.Errorf("the warning must say what is not covered, got %q", got.Detail)
	}
	if got.Fix == "" {
		t.Error("the warning must be satisfiable by doing what it asks")
	}
}

// Warning on every edit anywhere would make the check fire constantly and be ignored, which is
// worse than not having it. Only the trees a Windows run actually exercises count.
func TestDispatchRefCheckIgnoresUnrelatedEdits(t *testing.T) {
	root := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dispatchRefCheck(root); got.Status != statusOK {
		t.Errorf("an edit outside the watched trees must not warn, got %q: %s", got.Status, got.Detail)
	}
}

// A branch that was never pushed has no remote ref, so a dispatch cannot run it and fails outright.
// That is worse than being merely ahead of an upstream, not better -- and the first version of the
// check let the `rev-list @{upstream}..HEAD` error fall through to OK, reporting a branch that
// cannot be dispatched at all as ready to dispatch.
func TestDispatchRefCheckWarnsWhenTheBranchWasNeverPushed(t *testing.T) {
	root := newTestRepo(t, withoutUpstream)

	got := dispatchRefCheck(root)
	if got.Status == statusOK {
		t.Error("a branch with no upstream has no ref to dispatch and must not pass")
	}
	if !strings.Contains(got.Detail, "upstream") {
		t.Errorf("the warning should name the missing upstream, got %q", got.Detail)
	}
	if got.Fix == "" {
		t.Error("the warning must be satisfiable by doing what it asks")
	}
}

// A branch that is pushed but has local commits on top would dispatch the pushed commit, silently
// running code the contributor is not looking at while `git status` reads clean.
func TestDispatchRefCheckWarnsWhenAheadOfUpstream(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, root, filepath.Join("cli", "beacon", "extra.go"), "package main\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-qm", "local only")

	got := dispatchRefCheck(root)
	if got.Status != statusWarn {
		t.Errorf("being ahead of upstream must warn, got %q: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "ahead") {
		t.Errorf("the warning should say the branch is ahead, got %q", got.Detail)
	}
}

// repoOption tunes the fixture.
type repoOption func(*repoConfig)

type repoConfig struct{ upstream bool }

// withoutUpstream leaves the branch unpushed.
func withoutUpstream(c *repoConfig) { c.upstream = false }

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitIn runs git with a hermetic configuration.
//
// The global and system configs are neutralized because a contributor's own settings -- commit
// signing, a hooks path, a default branch name -- would otherwise fail these commits for reasons
// unrelated to what is being tested.
func gitIn(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// newTestRepo builds a committed git repo with the directory shape the check watches, pushed to a
// local bare remote so the branch has an upstream -- which is the normal state for a branch someone
// is about to dispatch.
func newTestRepo(t *testing.T, opts ...repoOption) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	cfg := repoConfig{upstream: true}
	for _, o := range opts {
		o(&cfg)
	}
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join("cli", "beacon"),
		"collector-builder",
		"beacon-sandbox",
		filepath.Join(".github", "workflows"),
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, root, filepath.Join("cli", "beacon", "main.go"), "package main\n")
	writeFile(t, root, "README.md", "# fixture\n")

	gitIn(t, root, "init", "-q", "-b", "work")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-qm", "initial")

	if cfg.upstream {
		// A local bare repo stands in for the remote. Real enough for the check, which only asks
		// git whether the branch has an upstream and how far ahead of it HEAD is.
		remote := t.TempDir()
		gitIn(t, remote, "init", "-q", "--bare")
		gitIn(t, root, "remote", "add", "origin", remote)
		gitIn(t, root, "push", "-q", "-u", "origin", "work")
	}
	return root
}
