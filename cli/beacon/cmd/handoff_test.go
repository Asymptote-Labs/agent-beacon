package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
)

type stubHandoffSource struct {
	harness  string
	sessions []handoff.Session
	err      error
}

func (s stubHandoffSource) Harness() string                  { return s.harness }
func (s stubHandoffSource) List() ([]handoff.Session, error) { return s.sessions, s.err }

func stubHandoffSources(t *testing.T, sources ...handoff.Source) *handoff.StoreDirs {
	t.Helper()
	var seen handoff.StoreDirs
	prev := handoffSources
	handoffSources = func(dirs handoff.StoreDirs) []handoff.Source {
		seen = dirs
		return sources
	}
	t.Cleanup(func() { handoffSources = prev })
	return &seen
}

// runHandoff executes `beacon handoff ...` through the root command with fresh flag values.
func runHandoff(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	handoffOpts = handoffOptions{}
	for _, cmd := range handoffCmd.Commands() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"handoff"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

func handoffFixtureSessions() []handoff.Source {
	now := time.Now()
	return []handoff.Source{
		stubHandoffSource{harness: handoff.HarnessClaude, sessions: []handoff.Session{
			{Harness: handoff.HarnessClaude, ID: "claude-1", Title: "Add a health endpoint", Directory: "/work/api", UpdatedAt: now.Add(-5 * time.Minute)},
			{Harness: handoff.HarnessClaude, ID: "claude-sub", Directory: "/work/api", UpdatedAt: now.Add(-4 * time.Minute), Subagent: true, ParentID: "claude-1"},
		}},
		stubHandoffSource{harness: handoff.HarnessCodex, sessions: []handoff.Session{
			{Harness: handoff.HarnessCodex, ID: "codex-1", Title: strings.Repeat("long title ", 10), Directory: "/work/web", UpdatedAt: now.Add(-3 * time.Hour)},
		}},
	}
}

func TestHandoffListCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"handoff", "list"})
	if err != nil || cmd == nil || cmd.Name() != "list" {
		t.Fatalf("handoff list not registered: %v %#v", err, cmd)
	}
	for _, flag := range []string{"json", "harness", "here", "dir", "subagents", "limit", "claude-projects-dir", "codex-dir", "opencode-dir", "cline-dir"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("handoff list missing --%s", flag)
		}
	}
}

func TestHandoffListPrintsATable(t *testing.T) {
	stubHandoffSources(t, handoffFixtureSessions()...)
	out, _, err := runHandoff(t, "list")
	if err != nil {
		t.Fatalf("handoff list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and two sessions (subagent hidden), got:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "RUNTIME") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "claude-1 ") || !strings.Contains(lines[1], "5m ago") || !strings.Contains(lines[1], "/work/api") {
		t.Fatalf("newest session row = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "codex-1 ") || !strings.Contains(lines[2], "3h ago") || !strings.HasSuffix(lines[2], "…") {
		t.Fatalf("codex row = %q; long titles are cut with an ellipsis", lines[2])
	}
}

func TestHandoffListJSONAndSubagents(t *testing.T) {
	stubHandoffSources(t, handoffFixtureSessions()...)
	out, _, err := runHandoff(t, "list", "--json", "--subagents")
	if err != nil {
		t.Fatalf("handoff list --json: %v", err)
	}
	var result handoffListResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(result.Sessions) != 3 || result.Sessions[0].ID != "claude-sub" || !result.Sessions[0].Subagent {
		t.Fatalf("sessions = %+v", result.Sessions)
	}
	if result.Warnings != nil {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
}

func TestHandoffListJSONIsAnEmptyArrayNotNull(t *testing.T) {
	stubHandoffSources(t)
	out, _, err := runHandoff(t, "list", "--json")
	if err != nil {
		t.Fatalf("handoff list --json: %v", err)
	}
	if !strings.Contains(out, `"sessions": []`) {
		t.Fatalf("empty listing must encode sessions as [], got %s", out)
	}
	text, _, _ := runHandoff(t, "list")
	if strings.TrimSpace(text) != "No resumable sessions found." {
		t.Fatalf("empty text listing = %q", text)
	}
}

func TestHandoffListFilters(t *testing.T) {
	stubHandoffSources(t, handoffFixtureSessions()...)
	out, _, err := runHandoff(t, "list", "--harness", "codex")
	if err != nil || !strings.Contains(out, "codex-1") || strings.Contains(out, "claude-1") {
		t.Fatalf("--harness codex = %v\n%s", err, out)
	}
	out, _, err = runHandoff(t, "list", "--dir", "/work/api")
	if err != nil || !strings.Contains(out, "claude-1") || strings.Contains(out, "codex-1") {
		t.Fatalf("--dir /work/api = %v\n%s", err, out)
	}
	out, _, err = runHandoff(t, "list", "--limit", "1")
	if err != nil || strings.Count(strings.TrimSpace(out), "\n") != 1 {
		t.Fatalf("--limit 1 = %v\n%s", err, out)
	}
}

func TestHandoffListHereUsesTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	here, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	stubHandoffSources(t, stubHandoffSource{harness: handoff.HarnessCline, sessions: []handoff.Session{
		{Harness: handoff.HarnessCline, ID: "in-here", Directory: filepath.Join(here, "pkg")},
		{Harness: handoff.HarnessCline, ID: "elsewhere", Directory: "/other"},
	}})
	out, _, err := runHandoff(t, "list", "--here")
	if err != nil {
		t.Fatalf("handoff list --here: %v", err)
	}
	if !strings.Contains(out, "in-here") || strings.Contains(out, "elsewhere") {
		t.Fatalf("--here listing:\n%s", out)
	}
	if _, _, err := runHandoff(t, "list", "--here", "--dir", "/x"); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--here with --dir err = %v", err)
	}
}

func TestHandoffListWarnsAboutUnreadableStores(t *testing.T) {
	stubHandoffSources(t,
		stubHandoffSource{harness: handoff.HarnessClaude, err: errors.New("permission denied")},
		stubHandoffSource{harness: handoff.HarnessCodex, sessions: []handoff.Session{{Harness: handoff.HarnessCodex, ID: "codex-1"}}},
	)
	out, stderr, err := runHandoff(t, "list")
	if err != nil {
		t.Fatalf("an unreadable store must not fail the listing: %v", err)
	}
	if !strings.Contains(out, "codex-1") || !strings.Contains(stderr, "warning: could not read claude_code: permission denied") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", out, stderr)
	}
	out, _, err = runHandoff(t, "list", "--json")
	if err != nil || !strings.Contains(out, `"claude_code: permission denied"`) {
		t.Fatalf("--json should carry the warning: %v\n%s", err, out)
	}
}

func TestHandoffListRejectsUnknownRuntime(t *testing.T) {
	stubHandoffSources(t)
	if _, _, err := runHandoff(t, "list", "--harness", "cursor"); err == nil || !strings.Contains(err.Error(), "unsupported runtime") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandoffListPassesStoreDirectories(t *testing.T) {
	seen := stubHandoffSources(t)
	if _, _, err := runHandoff(t, "list", "--claude-projects-dir", "/c", "--codex-dir", "/x", "--opencode-dir", "/o", "--cline-dir", "/l"); err != nil {
		t.Fatal(err)
	}
	want := handoff.StoreDirs{ClaudeProjects: "/c", Codex: "/x", OpenCode: "/o", Cline: "/l"}
	if *seen != want {
		t.Fatalf("store dirs = %+v, want %+v", *seen, want)
	}
}

func TestHandoffAge(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		when time.Time
		want string
	}{
		{time.Time{}, "-"},
		{now.Add(-10 * time.Second), "just now"},
		{now.Add(-59 * time.Minute), "59m ago"},
		{now.Add(-47 * time.Hour), "47h ago"},
	} {
		if got := handoffAge(tc.when, now); got != tc.want {
			t.Fatalf("handoffAge(%s) = %q, want %q", tc.when, got, tc.want)
		}
	}
	if got := handoffAge(now.Add(-72*time.Hour), now); len(got) != len("2006-01-02") {
		t.Fatalf("old sessions show a date, got %q", got)
	}
}
