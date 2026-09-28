package cmd

import (
	"bytes"
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

func TestGitNotesPushAndFetchCommands(t *testing.T) {
	root, remote := gitCommandTeam(t)
	sha := mustGit(t, root, "rev-parse", "HEAD")
	out, err := runGit(t, "notes", "push", "--cwd", root)
	if err != nil || !strings.Contains(out, "No commit links here to share") {
		t.Fatalf("empty push: %v %q", err, out)
	}
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:codex_cli/s1", sha)
	out, err = runGit(t, "notes", "push", "--cwd", root)
	if err != nil || !strings.Contains(out, "Pushed refs/notes/beacon to origin") {
		t.Fatalf("push: %v %q", err, out)
	}
	if got := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); got != "beacon:codex_cli/s1" {
		t.Fatalf("remote note %q", got)
	}

	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, root, "clone", "-q", remote, other)
	out, err = runGit(t, "notes", "fetch", "--cwd", other)
	if err != nil || !strings.Contains(out, "Fetched origin's links into refs/notes/beacon-remotes/origin") {
		t.Fatalf("fetch: %v %q", err, out)
	}
	out, err = runGit(t, "notes", "--cwd", other)
	if err != nil || !strings.Contains(out, "codex_cli/s1") {
		t.Fatalf("notes after fetch: %v %q", err, out)
	}
	// A revision argument still means a revision, not a subcommand.
	if out, err := runGit(t, "notes", "--cwd", other, "HEAD"); err != nil || !strings.Contains(out, "codex_cli/s1") {
		t.Fatalf("notes HEAD: %v %q", err, out)
	}
	out, err = runGit(t, "notes", "fetch", "--cwd", other, "nowhere")
	if err == nil || !strings.Contains(err.Error(), "fetch links from nowhere") {
		t.Fatalf("fetch from an unknown remote: %v %q", err, out)
	}
}

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
	// An unreachable remote: one warning line on stderr, and still no error for git to see.
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	missing := filepath.Join(t.TempDir(), "gone.git")
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, missing, missing); err != nil {
		t.Fatalf("pre-push must not fail: %v", err)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "beacon: commit links were not shared with "+missing) || strings.Count(got, "\n") != 1 {
		t.Fatalf("stderr %q", got)
	}
	t.Setenv(gitlink.DisableEnv, "0")
	stderr.Reset()
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, missing); err != nil || stderr.Len() != 0 {
		t.Fatalf("disabled: %v %q", err, stderr.String())
	}
}

func TestGitSetupShareNotesFlag(t *testing.T) {
	root, _ := gitCommandTeam(t)
	out, err := runGit(t, "setup", "--cwd", root, "--share-notes")
	if err != nil || !strings.Contains(out, "remote.origin.fetch += +refs/notes/beacon:refs/notes/beacon-remotes/origin") || !strings.Contains(out, "Commit links are shared") {
		t.Fatalf("setup --share-notes: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "beacon-pre-push")); err != nil {
		t.Fatal(err)
	}
	out, _ = runGit(t, "status", "--cwd", root)
	if !strings.Contains(out, "sharing     on") || !strings.Contains(out, "fetches from origin bring its links") {
		t.Fatalf("status:\n%s", out)
	}
	if _, err := runGit(t, "setup", "--cwd", root); err != nil {
		t.Fatal(err)
	}
	if out, _ := runGit(t, "status", "--cwd", root); !strings.Contains(out, "sharing     on") {
		t.Fatalf("a plain re-run changed sharing:\n%s", out)
	}
	out, err = runGit(t, "setup", "--cwd", root, "--share-notes=false")
	if err != nil || !strings.Contains(out, "unset     remote.origin.fetch") {
		t.Fatalf("setup --share-notes=false: %v\n%s", err, out)
	}
	if out, _ := runGit(t, "status", "--cwd", root); !strings.Contains(out, "sharing     off") {
		t.Fatalf("status after off:\n%s", out)
	}
}

func TestGitSetupShareNotesForeignPrePushMessage(t *testing.T) {
	root, _ := gitCommandTeam(t)
	hooks := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runGit(t, "setup", "--cwd", root, "--share-notes")
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"pre-push hook", `beacon git hook pre-push "$@" || true`, "without --share-notes", "nothing was installed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "hook post-commit") {
		t.Errorf("message names the wrong hook:\n%s", msg)
	}
	// Linking alone still installs.
	if _, err := runGit(t, "setup", "--cwd", root); err != nil {
		t.Fatal(err)
	}
}
