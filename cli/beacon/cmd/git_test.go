package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// runGit executes `beacon git ...` through the root command with fresh flag values.
func runGit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	gitOpts = gitOptions{}
	gitSetupOpts.allowHooksPath = false
	var reset func(*cobra.Command)
	reset = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		for _, sub := range c.Commands() {
			reset(sub)
		}
	}
	reset(gitCmd)
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"git"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return stdout.String(), err
}

// gitCommandRepo makes a repository the command can reach through the process environment,
// which is how a real invocation finds git's configuration.
func gitCommandRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, kv := range gitlink.IsolatedEnv(home) {
		key, value, _ := strings.Cut(kv, "=")
		t.Setenv(key, value)
	}
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "config", "commit.gpgsign", "false")
	return root
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitLinkAndNotesCommands(t *testing.T) {
	root := gitCommandRepo(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "Add main")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	top := mustGit(t, root, "rev-parse", "--show-toplevel")

	event := schema.NewEvent(schema.NewEventOptions{Action: "file.created", Harness: schema.HarnessInfo{Name: "claude_code"}})
	event.Timestamp = schema.FormatTimestamp(time.Now().Add(-5 * time.Minute))
	event.Session = &schema.SessionInfo{ID: "sess-1", WorkingDirectory: top}
	event.File = &schema.FileInfo{Path: filepath.Join(top, "main.go"), Operation: "create"}
	line, _ := json.Marshal(event)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runGit(t, "link", "--cwd", root, "--log-path", logPath, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would link") || !strings.Contains(out, "claude_code/sess-1") || !strings.Contains(out, "Dry run") {
		t.Fatalf("dry run output:\n%s", out)
	}
	if note := mustGitAllowFail(root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != "" {
		t.Fatalf("dry run wrote %q", note)
	}

	out, err = runGit(t, "link", "--cwd", root, "--log-path", logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Wrote 1 link(s)") {
		t.Fatalf("link output:\n%s", out)
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != "beacon:claude_code/sess-1" {
		t.Fatalf("note %q", note)
	}

	out, err = runGit(t, "notes", "--cwd", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var commits []gitlink.LinkedCommit
	if err := json.Unmarshal([]byte(out), &commits); err != nil {
		t.Fatalf("notes --json: %v\n%s", err, out)
	}
	if len(commits) != 1 || commits[0].SHA != sha || commits[0].Links[0] != (gitlink.Link{Harness: "claude_code", SessionID: "sess-1"}) {
		t.Fatalf("notes: %+v", commits)
	}
	out, err = runGit(t, "notes", "--cwd", root)
	if err != nil || !strings.Contains(out, "claude_code/sess-1") || !strings.Contains(out, "Add main") {
		t.Fatalf("notes text: %v\n%s", err, out)
	}
}

func TestGitNotesJSONIsAnArrayWhenEmpty(t *testing.T) {
	root := gitCommandRepo(t)
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "empty")
	out, err := runGit(t, "notes", "--cwd", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("got %q", out)
	}
}

func TestGitLinkOutsideRepository(t *testing.T) {
	gitCommandRepo(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := runGit(t, "link", "--cwd", dir); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("got %v", err)
	}
}

func mustGitAllowFail(dir string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

func TestPrintGitLinkResultLabelsTheLimit(t *testing.T) {
	var out bytes.Buffer
	a := gitlink.Link{Harness: "codex_cli", SessionID: "a"}
	b := gitlink.Link{Harness: "codex_cli", SessionID: "b"}
	c := gitlink.Link{Harness: "codex_cli", SessionID: "c"}
	printGitLinkResult(&out, gitlink.Result{
		ChangedFiles: 1,
		DryRun:       true,
		Candidates: []gitlink.Candidate{
			{Link: a, Files: []string{"x"}, AlreadyLinked: true},
			{Link: b, Files: []string{"x"}},
			{Link: c, Files: []string{"x"}},
		},
		Added: []gitlink.Link{b},
	})
	text := out.String()
	for _, want := range []string{"already linked  codex_cli/a", "would link      codex_cli/b", "skipped (limit) codex_cli/c"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}
