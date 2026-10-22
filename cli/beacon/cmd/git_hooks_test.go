package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// relaxGitBudgets lifts the git hooks' wall-clock budgets for a test of what a hook writes. A hook
// spawns git a dozen or more times, and on a loaded Windows runner each spawn can cost a few hundred
// milliseconds, so under the production budgets such a test fails when the runner is slow rather
// than when the hook is wrong. Tests of the budgets themselves do not call it.
func relaxGitBudgets(t *testing.T) {
	t.Helper()
	prevHook, prevShare, prevUpload := gitHookBudget, gitShareBudget, gitShareUploadBudget
	gitHookBudget = time.Minute
	gitShareBudget = time.Minute
	gitShareUploadBudget = gitShareBudget - gitShareNotesReserve - gitShareNoteWriteBudget
	t.Cleanup(func() { gitHookBudget, gitShareBudget, gitShareUploadBudget = prevHook, prevShare, prevUpload })
}

func writeRuntimeEvent(t *testing.T, logPath, top, session, file string) {
	t.Helper()
	event := schema.NewEvent(schema.NewEventOptions{Action: "file.modified", Harness: schema.HarnessInfo{Name: "cursor"}})
	event.Timestamp = schema.FormatTimestamp(time.Now().Add(-time.Minute))
	event.Session = &schema.SessionInfo{ID: session, WorkingDirectory: top}
	event.File = &schema.FileInfo{Path: filepath.Join(top, file), Operation: "modify"}
	line, _ := json.Marshal(event)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestGitHookPostCommitIsQuietOutsideARepositoryAndWhenDisabled(t *testing.T) {
	gitCommandRepo(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if out, err := runGit(t, "hook", "post-commit", "--cwd", dir); err != nil || out != "" {
		t.Fatalf("outside a repository: %v %q", err, out)
	}
	t.Setenv(gitlink.DisableEnv, "0")
	if out, err := runGit(t, "hook", "post-commit", "--cwd", dir, "--json"); err != nil || out != "" {
		t.Fatalf("disabled: %v %q", err, out)
	}
}

func TestGitHookPostCommitSkipsReplays(t *testing.T) {
	relaxGitBudgets(t)
	root := gitCommandRepo(t)
	top := mustGit(t, root, "rev-parse", "--show-toplevel")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeEvent(t, logPath, top, "cur-1", "x.go")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "x")
	// Pretend a rebase is underway.
	if err := os.MkdirAll(filepath.Join(root, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil {
		t.Fatal(err)
	}
	if note := mustGitAllowFail(root, "notes", "--ref="+gitlink.NotesRef, "show", "HEAD"); note != "" {
		t.Fatalf("a replay was attributed: %q", note)
	}
}

func TestGitSetupStatusRemove(t *testing.T) {
	root := gitCommandRepo(t)
	if _, err := runGit(t, "setup", "--cwd", root); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := runGit(t, "status", "--cwd", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Installed  bool `json:"installed"`
		RewriteRef bool `json:"rewrite_ref"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil || !st.Installed || !st.RewriteRef {
		t.Fatalf("status json %v: %s", err, out)
	}
	if _, err := runGit(t, "remove", "--cwd", root); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatal("hook left behind")
	}
}

func TestGitSetupExplainsCoreHooksPath(t *testing.T) {
	root := gitCommandRepo(t)
	mustGit(t, root, "config", "core.hooksPath", ".husky/_")
	if _, err := runGit(t, "setup", "--cwd", root); err == nil {
		t.Fatal("setup wrote into a shared core.hooksPath without --hooks-path")
	}
	if _, err := runGit(t, "setup", "--cwd", root, "--hooks-path"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".husky", "_", "beacon-post-commit")); err != nil {
		t.Fatal(err)
	}
}

func TestGitHookPostRewriteNormalizesRewrittenNotes(t *testing.T) {
	relaxGitBudgets(t)
	root := gitCommandRepo(t)
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "c")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:codex_cli/a\n\nbeacon:codex_cli/a", sha)
	rootCmd.SetIn(strings.NewReader("0000000000000000000000000000000000000000 " + sha + "\nmalformed\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	if _, err := runGit(t, "hook", "post-rewrite", "amend", "--cwd", root); err != nil {
		t.Fatal(err)
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != "beacon:codex_cli/a" {
		t.Fatalf("note %q", note)
	}
}

func linkEventsIn(t *testing.T, logPath string) []schema.Event {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var events []schema.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e schema.Event
		if json.Unmarshal([]byte(line), &e) == nil && e.Event.Action == asymptoteobserve.ActionCommitLinked {
			events = append(events, e)
		}
	}
	return events
}

func TestGitLinkRecordsOneEventPerLink(t *testing.T) {
	root := gitCommandRepo(t)
	top := mustGit(t, root, "rev-parse", "--show-toplevel")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeEvent(t, logPath, top, "cur-1", "x.go")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "x")
	mustGit(t, root, "remote", "add", "origin", "https://ghp_token@github.com/org/repo.git")
	sha := mustGit(t, root, "rev-parse", "HEAD")

	if _, err := runGit(t, "link", "--cwd", root, "--log-path", logPath, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if got := linkEventsIn(t, logPath); len(got) != 0 {
		t.Fatalf("a dry run recorded %d events", len(got))
	}
	for run := 0; run < 2; run++ {
		if _, err := runGit(t, "link", "--cwd", root, "--log-path", logPath); err != nil {
			t.Fatal(err)
		}
	}
	got := linkEventsIn(t, logPath)
	if len(got) != 1 {
		t.Fatalf("got %d link events after two runs, want 1", len(got))
	}
	e := got[0]
	if e.Session.ID != "cur-1" || e.Harness.Name != "cursor" || e.VCS.Ref.Head.Revision != sha || e.Branch != "main" || e.Event.ID == "" {
		t.Fatalf("event %+v", e)
	}
	if strings.Contains(e.VCS.Repository.URL.Full, "ghp_token") {
		t.Fatalf("remote URL kept credentials: %q", e.VCS.Repository.URL.Full)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "ghp_token") {
		t.Fatal("the token reached the runtime log")
	}
}

// A self-hosted Beacon's share URL survives post-rewrite normalization: the hook registers the
// configured host before it reads notes, and merging keeps a link's line with trailing fields over
// the bare one cat_sort_uniq sorts first.
func TestGitHookPostRewriteKeepsSelfHostedShareURLs(t *testing.T) {
	relaxGitBudgets(t)
	t.Setenv(account.BaseURLEnv, "https://beacon.rewrite-test.example")
	testenv.SetHome(t, t.TempDir())
	root := gitCommandRepo(t)
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "c")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	url := "https://beacon.rewrite-test.example/s/abc123"
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:codex_cli/a\nbeacon:codex_cli/a "+url, sha)
	rootCmd.SetIn(strings.NewReader("0000000000000000000000000000000000000000 " + sha + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	if _, err := runGit(t, "hook", "post-rewrite", "amend", "--cwd", root); err != nil {
		t.Fatal(err)
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != "beacon:codex_cli/a "+url {
		t.Fatalf("note %q", note)
	}
	if !gitlink.ValidShareURL(url) {
		t.Fatal("post-rewrite did not register the configured Beacon host")
	}
}
