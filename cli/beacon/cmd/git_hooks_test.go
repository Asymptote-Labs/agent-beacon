package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
)

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

func TestGitHookPostCommitLinksHEAD(t *testing.T) {
	root := gitCommandRepo(t)
	top := mustGit(t, root, "rev-parse", "--show-toplevel")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeEvent(t, logPath, top, "cur-1", "x.go")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "x")
	out, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath)
	if err != nil || out != "" {
		t.Fatalf("hook: %v, printed %q (a hook must stay quiet)", err, out)
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", "HEAD"); note != "beacon:cursor/cur-1" {
		t.Fatalf("note %q", note)
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
	out, err := runGit(t, "status", "--cwd", root)
	if err != nil || !strings.Contains(out, "post-commit not installed") {
		t.Fatalf("status before: %v\n%s", err, out)
	}
	out, err = runGit(t, "setup", "--cwd", root)
	if err != nil || !strings.Contains(out, "created") || !strings.Contains(out, "notes.rewriteRef") {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	hook, _ := os.ReadFile(filepath.Join(root, ".git", "hooks", "beacon-post-commit"))
	if exe, _ := os.Executable(); !strings.Contains(string(hook), exe) && !strings.Contains(string(hook), "beacon") {
		t.Fatalf("script does not name a beacon binary:\n%s", hook)
	}
	out, err = runGit(t, "status", "--cwd", root, "--json")
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
	out, err = runGit(t, "remove", "--cwd", root)
	if err != nil || !strings.Contains(out, "removed") || !strings.Contains(out, "Links already written stay") {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatal("hook left behind")
	}
}

func TestGitSetupExplainsCoreHooksPath(t *testing.T) {
	root := gitCommandRepo(t)
	mustGit(t, root, "config", "core.hooksPath", ".husky/_")
	_, err := runGit(t, "setup", "--cwd", root)
	if err == nil || !strings.Contains(err.Error(), "core.hooksPath") || !strings.Contains(err.Error(), "beacon git hook post-commit || true") || !strings.Contains(err.Error(), "--hooks-path") {
		t.Fatalf("got %v", err)
	}
	if _, err := runGit(t, "setup", "--cwd", root, "--hooks-path"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".husky", "_", "beacon-post-commit")); err != nil {
		t.Fatal(err)
	}
}

func TestGitHookPostRewriteNormalizesRewrittenNotes(t *testing.T) {
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
		if json.Unmarshal([]byte(line), &e) == nil && e.Event.Action == gitlink.LinkAction {
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

func TestGitHookPostCommitRecordsTheLinkEvent(t *testing.T) {
	root := gitCommandRepo(t)
	top := mustGit(t, root, "rev-parse", "--show-toplevel")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeEvent(t, logPath, top, "cur-9", "x.go")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "x")
	if _, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil {
		t.Fatal(err)
	}
	if got := linkEventsIn(t, logPath); len(got) != 1 || got[0].Session.ID != "cur-9" {
		t.Fatalf("events %+v", got)
	}
}
