package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

type stubHandoffSource struct {
	harness   string
	sessions  []handoff.Session
	err       error
	events    []schema.Event
	eventsErr error
}

func (s stubHandoffSource) Harness() string                  { return s.harness }
func (s stubHandoffSource) List() ([]handoff.Session, error) { return s.sessions, s.err }
func (s stubHandoffSource) Events(handoff.Session) ([]schema.Event, error) {
	return s.events, s.eventsErr
}

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
	handoffResumeOpts = handoffResumeOptions{}
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

func stubHandoffClock(t *testing.T) {
	t.Helper()
	prev := handoffNow
	handoffNow = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { handoffNow = prev })
}

func exportFixtureSource() stubHandoffSource {
	return stubHandoffSource{
		harness:  handoff.HarnessClaude,
		sessions: []handoff.Session{{Harness: handoff.HarnessClaude, ID: "claude-1", Directory: "/work/api"}},
		events: []schema.Event{{
			Timestamp: "2026-09-25T10:00:00Z",
			Event:     schema.EventInfo{Action: "prompt.submitted"},
			Prompt:    &schema.PromptInfo{Text: "add a health endpoint"},
		}},
	}
}

// handoffLog writes a runtime log holding one prompt for session id.
func handoffLog(t *testing.T, harness, id, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"timestamp":"2026-09-25T09:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted","category":"prompt"},"severity":"info","endpoint":{"hostname":"h","os":"linux"},"harness":{"name":"` + harness + `"},"session":{"id":"` + id + `","working_directory":"/work/x"},"prompt":{"text":"` + text + `"}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHandoffExportCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"handoff", "export"})
	if err != nil || cmd == nil || cmd.Name() != "export" {
		t.Fatalf("handoff export not registered: %v", err)
	}
	for _, flag := range []string{"print", "output-dir", "log-path", "json", "harness", "claude-projects-dir", "codex-dir", "opencode-dir", "cline-dir"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("handoff export missing --%s", flag)
		}
	}
	if _, _, err := runHandoff(t, "export"); err == nil {
		t.Fatal("export without a session id must fail")
	}
}

func TestHandoffExportPrint(t *testing.T) {
	stubHandoffClock(t)
	stubHandoffSources(t, exportFixtureSource())
	out, _, err := runHandoff(t, "export", "claude-1", "--print")
	if err != nil {
		t.Fatalf("export --print: %v", err)
	}
	if !strings.HasPrefix(out, "# Handoff brief") || !strings.Contains(out, "add a health endpoint") || !strings.Contains(out, "Brief written: 2026-09-25T12:00:00Z") {
		t.Fatalf("printed brief:\n%s", out)
	}
}

func TestHandoffExportWritesAPrivateFile(t *testing.T) {
	stubHandoffClock(t)
	stubHandoffSources(t, exportFixtureSource())
	dir := filepath.Join(t.TempDir(), "briefs")
	out, _, err := runHandoff(t, "export", "claude-1", "--output-dir", dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	path := strings.TrimSpace(out)
	if filepath.Dir(path) != dir {
		t.Fatalf("export printed %q, want a path in %s", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "add a health endpoint") {
		t.Fatalf("brief file: %v\n%s", err, data)
	}
	if testenv.HasPOSIXFileModes() {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("brief mode = %o, want 600", info.Mode().Perm())
		}
	}

	jsonOut, _, err := runHandoff(t, "export", "claude-1", "--output-dir", filepath.Join(t.TempDir(), "json"), "--json")
	if err != nil {
		t.Fatalf("export --json: %v", err)
	}
	var result handoffExportResult
	if err := json.Unmarshal([]byte(jsonOut), &result); err != nil || result.From != handoff.FromSessionStore || result.Session.ID != "claude-1" || result.Path == "" {
		t.Fatalf("export --json = %+v, %v\n%s", result, err, jsonOut)
	}
}

func TestHandoffExportDefaultsUnderTheBeaconDirectory(t *testing.T) {
	stubHandoffClock(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	stubHandoffSources(t, exportFixtureSource())
	out, _, err := runHandoff(t, "export", "claude-1")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if want := filepath.Join(home, ".beacon", "endpoint", "handoffs"); filepath.Dir(strings.TrimSpace(out)) != want {
		t.Fatalf("brief written to %q, want under %s", out, want)
	}
}

func TestHandoffExportFallsBackToTheRuntimeLog(t *testing.T) {
	stubHandoffClock(t)
	stubHandoffSources(t, stubHandoffSource{harness: handoff.HarnessClaude})
	logPath := handoffLog(t, "cursor", "cursor-conv-1", "rename the module")
	out, stderr, err := runHandoff(t, "export", "cursor-conv-1", "--log-path", logPath, "--output-dir", t.TempDir())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := os.ReadFile(strings.TrimSpace(out))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "rename the module") || !strings.Contains(string(data), "comes from Beacon's runtime log") {
		t.Fatalf("log brief:\n%s", data)
	}
	if !strings.Contains(stderr, "no longer in its runtime's store") {
		t.Fatalf("stderr should say where the brief came from: %q", stderr)
	}
}

func TestHandoffExportFallsBackWhenTheStoreLosesTheSession(t *testing.T) {
	stubHandoffClock(t)
	source := exportFixtureSource()
	source.eventsErr = handoff.ErrNotFound
	stubHandoffSources(t, source)
	logPath := handoffLog(t, handoff.HarnessClaude, "claude-1", "from the log")
	out, _, err := runHandoff(t, "export", "claude-1", "--print", "--log-path", logPath)
	if err != nil || !strings.Contains(out, "from the log") {
		t.Fatalf("export = %v\n%s", err, out)
	}
}

func TestHandoffExportErrors(t *testing.T) {
	stubHandoffClock(t)
	emptyLog := filepath.Join(t.TempDir(), "runtime.jsonl")

	stubHandoffSources(t, stubHandoffSource{harness: handoff.HarnessClaude})
	_, _, err := runHandoff(t, "export", "nope-123", "--log-path", emptyLog)
	if !errors.Is(err, handoff.ErrNotFound) || !strings.Contains(err.Error(), "beacon handoff list") {
		t.Fatalf("unknown session err = %v", err)
	}

	logPath := handoffLog(t, "cursor", "cursor-conv-1", "x")
	if _, _, err := runHandoff(t, "export", "cursor-conv-1", "--harness", "codex", "--log-path", logPath); !errors.Is(err, handoff.ErrNotFound) {
		t.Fatalf("--harness must also scope the log fallback, got %v", err)
	}

	broken := exportFixtureSource()
	broken.eventsErr = errors.New("transcript is unreadable")
	stubHandoffSources(t, broken)
	if _, _, err := runHandoff(t, "export", "claude-1", "--print"); err == nil || !strings.Contains(err.Error(), "transcript is unreadable") {
		t.Fatalf("a store read error must surface, not fall back silently: %v", err)
	}

	if _, _, err := runHandoff(t, "export", "claude-1", "--print", "--output-dir", "/tmp/x"); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--print with --output-dir err = %v", err)
	}
}
