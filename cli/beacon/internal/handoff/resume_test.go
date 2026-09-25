package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// installed resolves the named executables to /bin/<name>, as if they were on PATH.
func installed(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/bin/" + name, nil
			}
		}
		return "", errors.New("executable file not found in $PATH")
	}
}

var allRuntimes = installed("claude", "codex", "opencode", "cline")

// resumableSession is a session whose directory and session file both exist.
func resumableSession(t *testing.T, harness, id string) Session {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), id+".jsonl")
	writeFixture(t, file, "{}\n")
	s := Session{Harness: harness, ID: id, Directory: dir, SourcePath: file}
	if harness == HarnessCline {
		s.Store = "messages"
	}
	return s
}

func TestPlanResumeReopensEachRuntimeNatively(t *testing.T) {
	for _, tc := range []struct {
		harness, exe string
		args         []string
	}{
		{HarnessClaude, "claude", []string{"--resume", "s-1"}},
		{HarnessCodex, "codex", []string{"resume", "s-1"}},
		{HarnessOpenCode, "opencode", []string{"--session", "s-1"}},
		{HarnessCline, "cline", []string{"--tui", "--auto-approve", "false", "--id", "s-1"}},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			session := resumableSession(t, tc.harness, "s-1")
			plan, err := PlanResume(session, PlanOptions{LookPath: allRuntimes})
			if err != nil {
				t.Fatalf("PlanResume: %v", err)
			}
			if plan.Mode != ModeNative || plan.Reason != "" || plan.BriefPath != "" {
				t.Fatalf("plan = %+v, want a native resume with no brief", plan)
			}
			if plan.Executable != "/bin/"+tc.exe || !reflect.DeepEqual(plan.Args, tc.args) {
				t.Fatalf("command = %s %q, want /bin/%s %q", plan.Executable, plan.Args, tc.exe, tc.args)
			}
			if plan.Dir != session.Directory || plan.Target != tc.harness {
				t.Fatalf("dir/target = %q/%q", plan.Dir, plan.Target)
			}
		})
	}
}

func TestPlanResumeStartsANewSessionInEachRuntime(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "handoffs", "brief.md")
	for _, tc := range []struct {
		target, exe string
		prefix      []string
	}{
		{HarnessClaude, "claude", nil},
		{HarnessCodex, "codex", nil},
		{HarnessOpenCode, "opencode", []string{"--prompt"}},
		{HarnessCline, "cline", []string{"--tui", "--auto-approve", "false"}},
	} {
		t.Run(tc.target, func(t *testing.T) {
			source := HarnessCodex
			if tc.target == HarnessCodex {
				source = HarnessClaude
			}
			session := resumableSession(t, source, "s-1")
			plan, err := PlanResume(session, PlanOptions{Target: tc.target, BriefPath: brief, LookPath: allRuntimes})
			if err != nil {
				t.Fatalf("PlanResume: %v", err)
			}
			if plan.Mode != ModeNewSession || plan.Reason != ReasonOtherRuntime || plan.BriefPath != brief {
				t.Fatalf("plan = %+v", plan)
			}
			want := append(append([]string{}, tc.prefix...), NewSessionPrompt(session, brief))
			if plan.Executable != "/bin/"+tc.exe || !reflect.DeepEqual(plan.Args, want) {
				t.Fatalf("command = %s %q\nwant /bin/%s %q", plan.Executable, plan.Args, tc.exe, want)
			}
		})
	}
}

func TestNewSessionPromptPointsAtTheBriefWithoutInliningIt(t *testing.T) {
	prompt := NewSessionPrompt(Session{Harness: HarnessClaude}, "/tmp/b.md")
	for _, want := range []string{"earlier Claude Code session", "/tmp/b.md", "Read that file first", "wait for me to confirm"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q is missing %q", prompt, want)
		}
	}
}

func TestPlanResumeFallsBackToANewSession(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "brief.md")
	cases := []struct {
		name   string
		edit   func(*Session, *PlanOptions)
		reason string
	}{
		{"requested", func(s *Session, o *PlanOptions) { o.ForceNew = true }, ReasonRequested},
		{"session file deleted", func(s *Session, o *PlanOptions) { os.Remove(s.SourcePath) }, ReasonSessionGone},
		{"no session file recorded", func(s *Session, o *PlanOptions) { s.SourcePath = "" }, ReasonSessionGone},
		{"known only from the runtime log", func(s *Session, o *PlanOptions) { o.FromRuntimeLog = true }, ReasonFromRuntimeLog},
		{"claude subagent", func(s *Session, o *PlanOptions) { s.Subagent = true }, ReasonNotResumable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := resumableSession(t, HarnessClaude, "s-1")
			opts := PlanOptions{BriefPath: brief, LookPath: allRuntimes}
			tc.edit(&session, &opts)
			plan, err := PlanResume(session, opts)
			if err != nil {
				t.Fatalf("PlanResume: %v", err)
			}
			if plan.Mode != ModeNewSession || plan.Reason != tc.reason || plan.Target != HarnessClaude {
				t.Fatalf("plan = %+v, want a new claude session because %s", plan, tc.reason)
			}
			if ReasonText(plan.Reason) == plan.Reason {
				t.Fatalf("reason %q has no human text", plan.Reason)
			}
		})
	}
}

func TestPlanResumeClineOnlyReopensCLISessions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Session)
	}{
		{"task history", func(s *Session) { s.Store = "history" }},
		{"subagent thread", func(s *Session) { s.Subagent = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := resumableSession(t, HarnessCline, "c-1")
			tc.edit(&session)
			plan, err := PlanResume(session, PlanOptions{BriefPath: filepath.Join(t.TempDir(), "b.md"), LookPath: allRuntimes})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Mode != ModeNewSession || plan.Reason != ReasonNotResumable {
				t.Fatalf("plan = %+v", plan)
			}
			if !reflect.DeepEqual(plan.Args[:3], []string{"--tui", "--auto-approve", "false"}) {
				t.Fatalf("a new Cline session must also turn auto-approve off: %q", plan.Args)
			}
		})
	}
}

// Every command Beacon builds for Cline must turn auto-approve off, because Cline defaults to
// approving every tool call.
func TestEveryClineCommandTurnsAutoApproveOff(t *testing.T) {
	command := runtimeCommands[HarnessCline]
	resume, _ := command.Resume(Session{ID: "x", Store: "messages"})
	for _, args := range [][]string{resume, command.NewSession("p")} {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--auto-approve false") {
			t.Fatalf("cline args %q do not turn auto-approve off", args)
		}
	}
}

func TestPlanResumeErrors(t *testing.T) {
	session := resumableSession(t, HarnessClaude, "s-1")

	if _, err := PlanResume(session, PlanOptions{LookPath: installed("codex")}); !errors.Is(err, ErrRuntimeNotInstalled) || !strings.Contains(err.Error(), "claude was not found") {
		t.Fatalf("missing runtime err = %v", err)
	}
	if _, err := PlanResume(session, PlanOptions{Target: HarnessCodex, BriefPath: filepath.Join(t.TempDir(), "b.md"), LookPath: installed("claude")}); !errors.Is(err, ErrRuntimeNotInstalled) {
		t.Fatalf("missing target runtime err = %v", err)
	}
	if _, err := PlanResume(session, PlanOptions{Target: "cursor", LookPath: allRuntimes}); err == nil || !strings.Contains(err.Error(), "cannot be started") {
		t.Fatalf("unsupported target err = %v", err)
	}
	if _, err := PlanResume(Session{Harness: "cursor", ID: "x", Directory: t.TempDir()}, PlanOptions{LookPath: allRuntimes}); err == nil {
		t.Fatal("a session from an unsupported runtime needs --agent")
	}
	if _, err := PlanResume(session, PlanOptions{ForceNew: true, LookPath: allRuntimes}); err == nil || !strings.Contains(err.Error(), "brief path") {
		t.Fatalf("a new session without a brief path err = %v", err)
	}
	if _, err := PlanResume(session, PlanOptions{ForceNew: true, BriefPath: filepath.Join("handoffs", "b.md"), LookPath: allRuntimes}); err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("a relative brief path names another file from the runtime's directory; err = %v", err)
	}
}

func TestPlanResumeDirectory(t *testing.T) {
	session := resumableSession(t, HarnessCodex, "s-1")

	other := t.TempDir()
	plan, err := PlanResume(session, PlanOptions{Dir: other, LookPath: allRuntimes})
	if err != nil || plan.Dir != other {
		t.Fatalf("--cwd override = %q, %v", plan.Dir, err)
	}

	gone := session
	gone.Directory = filepath.Join(t.TempDir(), "deleted")
	if _, err := PlanResume(gone, PlanOptions{LookPath: allRuntimes}); err == nil || !strings.Contains(err.Error(), "--cwd") {
		t.Fatalf("a missing directory must not silently start elsewhere: %v", err)
	}
	unknown := session
	unknown.Directory = ""
	if _, err := PlanResume(unknown, PlanOptions{LookPath: allRuntimes}); err == nil || !strings.Contains(err.Error(), "no recorded directory") {
		t.Fatalf("no directory err = %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	writeFixture(t, file, "x")
	if _, err := PlanResume(session, PlanOptions{Dir: file, LookPath: allRuntimes}); err == nil {
		t.Fatal("a file is not a directory to start in")
	}
}

func TestLaunchRunsInTheSessionDirectoryAndReturnsTheExitCode(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	bin := t.TempDir()
	script := filepath.Join(bin, "fake-agent")
	writeFixture(t, script, "#!/bin/sh\npwd\nprintf '%s|' \"$@\"\necho\nread line\necho \"got $line\"\necho oops >&2\nexit 7\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var stdout, stderr strings.Builder
	code, err := Launch(Plan{Executable: script, Args: []string{"--resume", "a b"}, Dir: dir}, strings.NewReader("hello\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want the runtime's own", code)
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 || (lines[0] != dir && lines[0] != realDir) || lines[1] != "--resume|a b|" || lines[2] != "got hello" {
		t.Fatalf("stdout = %q; the runtime runs in the plan directory with its arguments intact and the terminal's stdin", stdout.String())
	}
	if strings.TrimSpace(stderr.String()) != "oops" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestLaunchReportsAMissingExecutable(t *testing.T) {
	code, err := Launch(Plan{Executable: filepath.Join(t.TempDir(), "missing"), Dir: t.TempDir()}, nil, nil, nil)
	if err == nil || code != -1 {
		t.Fatalf("Launch = %d, %v", code, err)
	}
}
