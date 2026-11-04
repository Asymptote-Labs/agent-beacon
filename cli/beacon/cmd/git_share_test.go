package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
)

// gitCommandTeam returns a repository with an origin remote holding one commit.
func gitCommandTeam(t *testing.T) (root, remote string) {
	t.Helper()
	root = gitCommandRepo(t)
	remote = filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", remote)
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "first")
	mustGit(t, root, "remote", "add", "origin", remote)
	mustGit(t, root, "push", "-q", "-u", "origin", "main")
	return root, remote
}

// gitStatusJSON is `beacon git status --json` for root.
func gitStatusJSON(t *testing.T, root string) gitlink.Status {
	t.Helper()
	out, err := runGit(t, "status", "--cwd", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var st gitlink.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status json %v: %s", err, out)
	}
	return st
}

func TestGitNotesPushAndFetchCommands(t *testing.T) {
	root, remote := gitCommandTeam(t)
	sha := mustGit(t, root, "rev-parse", "HEAD")
	if _, err := runGit(t, "notes", "push", "--cwd", root); err != nil {
		t.Fatalf("empty push: %v", err)
	}
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:codex_cli/s1", sha)
	if _, err := runGit(t, "notes", "push", "--cwd", root); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); got != "beacon:codex_cli/s1" {
		t.Fatalf("remote note %q", got)
	}

	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, root, "clone", "-q", remote, other)
	if _, err := runGit(t, "notes", "fetch", "--cwd", other); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	out, err := runGit(t, "notes", "--cwd", other)
	if err != nil || !strings.Contains(out, "codex_cli/s1") {
		t.Fatalf("notes after fetch: %v %q", err, out)
	}
	// A revision argument still means a revision, not a subcommand.
	if out, err := runGit(t, "notes", "--cwd", other, "HEAD"); err != nil || !strings.Contains(out, "codex_cli/s1") {
		t.Fatalf("notes HEAD: %v %q", err, out)
	}
	if _, err := runGit(t, "notes", "fetch", "--cwd", other, "nowhere"); err == nil {
		t.Fatal("fetch from an unknown remote succeeded")
	}
}

// The pre-push hook pushes the links, within its budget, and never fails the push: an unreachable
// remote costs one line on stderr.
func TestGitHookPrePushSharesAndNeverFails(t *testing.T) {
	root, remote := gitCommandTeam(t)
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:cursor/c1", sha)
	if out, err := runGit(t, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil || out != "" {
		t.Fatalf("pre-push: %v %q", err, out)
	}
	if got := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); got != "beacon:cursor/c1" {
		t.Fatalf("remote note %q", got)
	}
	if got := budgetEventsIn(t, gitRuntimeLogPath()); len(got) != 0 {
		t.Fatalf("budget events %+v", got)
	}
	var stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "gone.git")
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, missing, missing); err != nil {
		t.Fatalf("pre-push must not fail: %v", err)
	}
	if got := stderr.String(); strings.Count(got, "\n") != 1 {
		t.Fatalf("stderr %q, want one line", got)
	}
	t.Setenv(gitlink.DisableEnv, "0")
	stderr.Reset()
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, missing); err != nil || stderr.Len() != 0 {
		t.Fatalf("disabled: %v %q", err, stderr.String())
	}
}

func TestGitSetupShareNotesFlag(t *testing.T) {
	root, _ := gitCommandTeam(t)
	if _, err := runGit(t, "setup", "--cwd", root, "--share-notes"); err != nil {
		t.Fatalf("setup --share-notes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "beacon-pre-push")); err != nil {
		t.Fatal(err)
	}
	if got := mustGitAllowFail(root, "config", "--get-all", "remote.origin.fetch"); !strings.Contains(got, "+refs/notes/beacon:refs/notes/beacon-remotes/origin") {
		t.Fatalf("remote.origin.fetch %q", got)
	}
	if st := gitStatusJSON(t, root); !st.Sharing || len(st.FetchRemotes) != 1 || st.FetchRemotes[0] != "origin" {
		t.Fatalf("status %+v", st)
	}
	if _, err := runGit(t, "setup", "--cwd", root); err != nil {
		t.Fatal(err)
	}
	if st := gitStatusJSON(t, root); !st.Sharing {
		t.Fatalf("a plain re-run changed sharing: %+v", st)
	}
	if _, err := runGit(t, "setup", "--cwd", root, "--share-notes=false"); err != nil {
		t.Fatalf("setup --share-notes=false: %v", err)
	}
	if got := mustGitAllowFail(root, "config", "--get-all", "remote.origin.fetch"); strings.Contains(got, "refs/notes/beacon") {
		t.Fatalf("remote.origin.fetch kept the notes refspec: %q", got)
	}
	if st := gitStatusJSON(t, root); st.Sharing {
		t.Fatalf("status after off: %+v", st)
	}
}
