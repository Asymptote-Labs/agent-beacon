package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// resumeFixture is a Claude session whose directory and transcript exist, served by a stub store.
type resumeFixture struct {
	session    handoff.Session
	briefDir   string
	launched   []handoff.Plan
	exitCode   int
	exitCalled int
}

func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	stubHandoffClock(t)
	f := &resumeFixture{briefDir: filepath.Join(t.TempDir(), "handoffs")}
	transcript := filepath.Join(t.TempDir(), "claude-1.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.session = handoff.Session{Harness: handoff.HarnessClaude, ID: "claude-1", Directory: t.TempDir(), SourcePath: transcript}
	stubHandoffSources(t, stubHandoffSource{
		harness:  handoff.HarnessClaude,
		sessions: []handoff.Session{f.session},
		events: []schema.Event{{
			Timestamp: "2026-09-25T10:00:00Z",
			Event:     schema.EventInfo{Action: "prompt.submitted"},
			Prompt:    &schema.PromptInfo{Text: "add a health endpoint"},
		}},
	})

	prevLook, prevLaunch, prevTerm, prevStdin, prevExit := handoffLookPath, handoffLaunch, handoffIsTerminal, handoffStdin, handoffExit
	handoffLookPath = func(name string) (string, error) {
		if name == "claude" || name == "codex" {
			return "/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	handoffLaunch = func(plan handoff.Plan, _ io.Reader, _, _ io.Writer) (int, error) {
		f.launched = append(f.launched, plan)
		return f.exitCode, nil
	}
	handoffIsTerminal = func() bool { return true }
	handoffStdin = strings.NewReader("\n")
	handoffExit = func(code int) { f.exitCalled = code }
	t.Cleanup(func() {
		handoffLookPath, handoffLaunch, handoffIsTerminal, handoffStdin, handoffExit = prevLook, prevLaunch, prevTerm, prevStdin, prevExit
	})
	return f
}

func runResume(t *testing.T, f *resumeFixture, args ...string) (string, string, error) {
	t.Helper()
	handoffResumeOpts = handoffResumeOptions{}
	return runHandoff(t, append([]string{"resume", "--output-dir", f.briefDir}, args...)...)
}

func TestHandoffResumeCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"handoff", "resume"})
	if err != nil || cmd == nil || cmd.Name() != "resume" {
		t.Fatalf("handoff resume not registered: %v", err)
	}
	for _, flag := range []string{"agent", "new", "cwd", "print", "yes", "json", "output-dir", "log-path", "harness", "claude-projects-dir", "codex-dir", "opencode-dir", "cline-dir"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("handoff resume missing --%s", flag)
		}
	}
}

func TestHandoffResumeReopensNativelyAfterConfirmation(t *testing.T) {
	f := newResumeFixture(t)
	_, stderr, err := runResume(t, f, "claude-1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(f.launched) != 1 {
		t.Fatalf("launched %d times", len(f.launched))
	}
	plan := f.launched[0]
	if plan.Mode != handoff.ModeNative || plan.Executable != "/bin/claude" || strings.Join(plan.Args, " ") != "--resume claude-1" || plan.Dir != f.session.Directory {
		t.Fatalf("plan = %+v", plan)
	}
	if !strings.Contains(stderr, "Reopening Claude Code session claude-1") || !strings.Contains(stderr, "command:   /bin/claude --resume claude-1") || !strings.Contains(stderr, "Continue? [Y/n]") {
		t.Fatalf("stderr = %q", stderr)
	}
	if entries, _ := os.ReadDir(f.briefDir); len(entries) != 0 {
		t.Fatalf("a native resume must not write a brief, found %d files", len(entries))
	}
	if f.exitCalled != 0 {
		t.Fatalf("a clean exit must not call exit, got %d", f.exitCalled)
	}
}

func TestHandoffResumeInAnotherRuntimeWritesTheBriefFirst(t *testing.T) {
	f := newResumeFixture(t)
	var briefAtLaunch string
	handoffLaunch = func(plan handoff.Plan, _ io.Reader, _, _ io.Writer) (int, error) {
		data, err := os.ReadFile(plan.BriefPath)
		if err != nil {
			t.Fatalf("the brief must exist before the runtime starts: %v", err)
		}
		briefAtLaunch = string(data)
		f.launched = append(f.launched, plan)
		return 0, nil
	}
	_, stderr, err := runResume(t, f, "claude-1", "--agent", "codex", "--yes")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	plan := f.launched[0]
	if plan.Mode != handoff.ModeNewSession || plan.Target != handoff.HarnessCodex || plan.Executable != "/bin/codex" {
		t.Fatalf("plan = %+v", plan)
	}
	if filepath.Dir(plan.BriefPath) != f.briefDir || !strings.Contains(plan.Args[0], plan.BriefPath) {
		t.Fatalf("the prompt should name the brief in the output dir: %q / %q", plan.BriefPath, plan.Args)
	}
	if strings.Contains(plan.Args[0], "add a health endpoint") {
		t.Fatal("the brief must be passed by path, never inlined on the command line")
	}
	if !strings.Contains(briefAtLaunch, "add a health endpoint") || !strings.Contains(briefAtLaunch, "Brief written: 2026-09-25T12:00:00Z") {
		t.Fatalf("brief at launch:\n%s", briefAtLaunch)
	}
	if strings.Contains(stderr, "Continue?") {
		t.Fatal("--yes must not ask")
	}
	if !strings.Contains(stderr, "Starting a new Codex CLI session from Claude Code session claude-1 (it continues in a different runtime)") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestHandoffResumePrintWritesAndLaunchesNothing(t *testing.T) {
	f := newResumeFixture(t)
	out, _, err := runResume(t, f, "claude-1", "--new", "--print")
	if err != nil {
		t.Fatalf("resume --print: %v", err)
	}
	if len(f.launched) != 0 {
		t.Fatal("--print launched the runtime")
	}
	if _, err := os.Stat(f.briefDir); !os.IsNotExist(err) {
		t.Fatalf("--print must not write a brief or create its directory: %v", err)
	}
	if !strings.Contains(out, "(a new session was requested)") || !regexp.MustCompile(`command:   /bin/claude 'Continue the work from an earlier Claude Code session\. Beacon wrote a handoff brief of it at `).MatchString(out) {
		t.Fatalf("--print output:\n%s", out)
	}

	jsonOut, _, err := runResume(t, f, "claude-1", "--print", "--json")
	if err != nil {
		t.Fatalf("resume --print --json: %v", err)
	}
	var plan handoff.Plan
	if err := json.Unmarshal([]byte(jsonOut), &plan); err != nil || plan.Mode != handoff.ModeNative || plan.Source.ID != "claude-1" {
		t.Fatalf("plan json = %+v, %v\n%s", plan, err, jsonOut)
	}
	if _, _, err := runResume(t, f, "claude-1", "--json"); err == nil || !strings.Contains(err.Error(), "needs --print") {
		t.Fatalf("--json without --print err = %v", err)
	}
}

func TestHandoffResumeAsksAndCanBeDeclined(t *testing.T) {
	f := newResumeFixture(t)
	for _, answer := range []string{"n\n", "no\n", "later\n", ""} {
		handoffStdin = strings.NewReader(answer)
		_, stderr, err := runResume(t, f, "claude-1", "--new")
		if err != nil {
			t.Fatalf("declined resume returned an error: %v", err)
		}
		if !strings.Contains(stderr, "Cancelled.") {
			t.Fatalf("answer %q: stderr = %q", answer, stderr)
		}
	}
	if len(f.launched) != 0 {
		t.Fatal("a declined resume launched the runtime")
	}
	if _, err := os.Stat(f.briefDir); !os.IsNotExist(err) {
		t.Fatal("a declined resume must not write a brief")
	}
	for _, answer := range []string{"y\n", "YES\n", "\n"} {
		handoffStdin = strings.NewReader(answer)
		if _, _, err := runResume(t, f, "claude-1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.launched) != 3 {
		t.Fatalf("accepted resumes launched %d times, want 3", len(f.launched))
	}
}

func TestHandoffResumeRefusesToLaunchWithoutATerminal(t *testing.T) {
	f := newResumeFixture(t)
	handoffIsTerminal = func() bool { return false }
	if _, _, err := runResume(t, f, "claude-1"); err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v", err)
	}
	if len(f.launched) != 0 {
		t.Fatal("launched without a terminal or --yes")
	}
	if _, _, err := runResume(t, f, "claude-1", "--yes"); err != nil || len(f.launched) != 1 {
		t.Fatalf("--yes launches without a terminal: %v", err)
	}
}

func TestHandoffResumePassesTheRuntimeExitCodeThrough(t *testing.T) {
	f := newResumeFixture(t)
	f.exitCode = 3
	if _, _, err := runResume(t, f, "claude-1", "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.exitCalled != 3 {
		t.Fatalf("exit = %d, want the runtime's 3", f.exitCalled)
	}
	f.exitCode = -1
	if _, _, err := runResume(t, f, "claude-1", "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.exitCalled != 1 {
		t.Fatalf("a runtime killed by a signal should exit 1, got %d", f.exitCalled)
	}
}

func TestHandoffResumeErrors(t *testing.T) {
	f := newResumeFixture(t)
	if _, _, err := runResume(t, f, "claude-1", "--agent", "opencode", "--yes"); !errors.Is(err, handoff.ErrRuntimeNotInstalled) {
		t.Fatalf("uninstalled target err = %v", err)
	}
	if _, _, err := runResume(t, f, "claude-1", "--agent", "cursor"); err == nil || !strings.Contains(err.Error(), "--agent") {
		t.Fatalf("unsupported --agent err = %v", err)
	}
	handoffLaunch = func(handoff.Plan, io.Reader, io.Writer, io.Writer) (int, error) {
		return -1, errors.New("exec format error")
	}
	if _, _, err := runResume(t, f, "claude-1", "--yes"); err == nil || !strings.Contains(err.Error(), "start /bin/claude: exec format error") {
		t.Fatalf("launch failure err = %v", err)
	}
	if _, _, err := runResume(t, f, "nope-123", "--log-path", filepath.Join(t.TempDir(), "none.jsonl")); !errors.Is(err, handoff.ErrNotFound) {
		t.Fatalf("unknown session err = %v", err)
	}
}

func TestHandoffResumeFromTheRuntimeLogStartsANewSession(t *testing.T) {
	f := newResumeFixture(t)
	logPath := handoffLog(t, "cursor", "cursor-conv-1", "rename the module")
	dir := t.TempDir()
	_, _, err := runResume(t, f, "cursor-conv-1", "--log-path", logPath, "--agent", "claude", "--cwd", dir, "--yes")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	plan := f.launched[0]
	if plan.Mode != handoff.ModeNewSession || plan.Reason != handoff.ReasonOtherRuntime || plan.Dir != dir {
		t.Fatalf("plan = %+v", plan)
	}
	data, err := os.ReadFile(plan.BriefPath)
	if err != nil || !strings.Contains(string(data), "rename the module") || !strings.Contains(string(data), "runtime log") {
		t.Fatalf("log brief: %v\n%s", err, data)
	}
}

// Runs the real launcher against fake runtime executables on PATH, the way a person would run it.
func TestHandoffResumeEndToEndWithFakeRuntimes(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	f := newResumeFixture(t)
	handoffLookPath, handoffLaunch = exec.LookPath, handoff.Launch
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	for _, name := range []string{"claude", "codex"} {
		script := "#!/bin/sh\n{ echo \"exe=" + name + "\"; echo \"cwd=$(pwd)\"; for a in \"$@\"; do echo \"arg=$a\"; done; } > " + record + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	if _, _, err := runResume(t, f, "claude-1", "--yes"); err != nil {
		t.Fatalf("native resume: %v", err)
	}
	got := readRecord(t, record)
	realDir, _ := filepath.EvalSymlinks(f.session.Directory)
	if got["exe"][0] != "claude" || strings.Join(got["arg"], " ") != "--resume claude-1" || (got["cwd"][0] != f.session.Directory && got["cwd"][0] != realDir) {
		t.Fatalf("native run = %v", got)
	}

	if _, _, err := runResume(t, f, "claude-1", "--agent", "codex", "--yes"); err != nil {
		t.Fatalf("new session: %v", err)
	}
	got = readRecord(t, record)
	if got["exe"][0] != "codex" || len(got["arg"]) != 1 {
		t.Fatalf("new session run = %v", got)
	}
	path := regexp.MustCompile(`at (\S+\.md)\.`).FindStringSubmatch(got["arg"][0])
	if path == nil {
		t.Fatalf("prompt names no brief: %q", got["arg"][0])
	}
	data, err := os.ReadFile(path[1])
	if err != nil || !strings.Contains(string(data), "add a health endpoint") {
		t.Fatalf("the brief the runtime was pointed at: %v\n%s", err, data)
	}
	if testenv.HasPOSIXFileModes() {
		if info, _ := os.Stat(path[1]); info.Mode().Perm() != 0o600 {
			t.Fatalf("brief mode = %o", info.Mode().Perm())
		}
	}
}

func readRecord(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		out[key] = append(out[key], value)
	}
	return out
}

// The runtime starts in the session's directory, so a relative --output-dir or --cwd must reach it
// as the absolute path Beacon meant.
func TestHandoffResumeMakesRelativePathsAbsolute(t *testing.T) {
	f := newResumeFixture(t)
	work := t.TempDir()
	t.Chdir(work)
	if err := os.Mkdir("start-here", 0o755); err != nil {
		t.Fatal(err)
	}
	handoffResumeOpts = handoffResumeOptions{}
	if _, _, err := runHandoff(t, "resume", "claude-1", "--new", "--output-dir", "briefs", "--cwd", "start-here", "--yes"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	plan := f.launched[0]
	if !filepath.IsAbs(plan.BriefPath) || !filepath.IsAbs(plan.Dir) {
		t.Fatalf("brief %q and dir %q must be absolute", plan.BriefPath, plan.Dir)
	}
	if !strings.Contains(plan.Args[0], plan.BriefPath) {
		t.Fatalf("the prompt must carry the absolute brief path: %q", plan.Args[0])
	}
	if _, err := os.Stat(plan.BriefPath); err != nil {
		t.Fatalf("the brief is not where the prompt says: %v", err)
	}
	if filepath.Base(plan.Dir) != "start-here" || filepath.Base(filepath.Dir(plan.BriefPath)) != "briefs" {
		t.Fatalf("dir %q, brief %q", plan.Dir, plan.BriefPath)
	}
}
