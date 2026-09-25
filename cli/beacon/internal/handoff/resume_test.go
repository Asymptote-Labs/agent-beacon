package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
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

var allRuntimes = installed("claude", "codex", "opencode", "cline", "pi", "prime-agent", "droid",
	"omp", "gemini", "qwen", "kiro-cli", "agy", "devin", "muse", "openhands", "goose", "copilot",
	"grok")

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
		{HarnessFactory, "droid", []string{"--resume", "s-1"}},
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
		{HarnessPi, "pi", []string{"--"}},
		{HarnessPrime, "prime-agent", []string{"--"}},
		{HarnessFactory, "droid", nil},
		{HarnessCopilot, "copilot", []string{"-i"}},
		{HarnessGrok, "grok", []string{"--permission-mode", "default"}},
		{HarnessOhMyPi, "omp", []string{"--approval-mode=always-ask"}},
		{HarnessGemini, "gemini", []string{"--approval-mode", "default", "--prompt-interactive"}},
		{HarnessQwen, "qwen", []string{"--approval-mode", "default", "--prompt-interactive"}},
		{HarnessKiro, "kiro-cli", []string{"chat"}},
		{HarnessAntigravity, "agy", []string{"--prompt-interactive"}},
		{HarnessDevin, "devin", []string{"--permission-mode", "auto", "--"}},
		{HarnessMuse, "muse", []string{"--approval-mode", "on-request"}},
		{HarnessOpenHands, "openhands", []string{"--task"}},
		{HarnessGoose, "goose", []string{"run", "--interactive", "--text"}},
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

func TestNewSessionPromptCarriesTheHandoffMarker(t *testing.T) {
	prompt := NewSessionPrompt(Session{Harness: HarnessCodex, ID: "019a-thread"}, "/tmp/b.md")
	info, ok := asymptoteobserve.ParseHandoffMarker(prompt)
	if !ok || info.SourceHarness != HarnessCodex || info.SourceSessionID != "019a-thread" {
		t.Fatalf("prompt %q carries marker %+v, %v", prompt, info, ok)
	}
	if !strings.HasSuffix(prompt, "\n\n[beacon-handoff from=codex_cli session=019a-thread]") {
		t.Fatalf("the marker is the prompt's last line: %q", prompt)
	}
	if strings.Contains(NewSessionPrompt(Session{Harness: HarnessCline, ID: "has space"}, "/tmp/b.md"), "[beacon-handoff") {
		t.Fatal("an id the marker cannot carry gets no marker rather than a wrong one")
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
	command, _ := commandFor(HarnessCline)
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

func TestLaunchEnvSetsAndRemovesVariables(t *testing.T) {
	environ := []string{"PATH=/bin", "ALLOW_ALL=true", "MODE=auto", "KEEP=1"}
	got := launchEnv(environ, map[string]string{"ALLOW_ALL": "", "MODE": "approve", "NEW": "x"})
	want := []string{"PATH=/bin", "KEEP=1", "MODE=approve", "NEW=x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %q, want %q", got, want)
	}
	if got := launchEnv(environ, nil); !reflect.DeepEqual(got, environ) {
		t.Fatalf("no overrides changed the environment: %q", got)
	}
}

// withRuntime registers a runtime for the length of a test.
func withRuntime(t *testing.T, r Runtime) {
	t.Helper()
	prev := runtimes
	runtimes = append(append([]Runtime{}, runtimes...), r)
	t.Cleanup(func() { runtimes = prev })
}

func TestPlanResumeCarriesTheRuntimesEnvironment(t *testing.T) {
	withRuntime(t, Runtime{Harness: "test_agent", Label: "Test Agent", Command: &runtimeCommand{
		Executable: "test-agent",
		Resume:     func(s Session) ([]string, bool) { return []string{"--resume", s.ID}, true },
		NewSession: func(prompt string) []string { return []string{prompt} },
		Env:        map[string]string{"TEST_AGENT_MODE": "approve", "TEST_AGENT_ALLOW_ALL": ""},
	}})
	session := resumableSession(t, "test_agent", "s-1")
	plan, err := PlanResume(session, PlanOptions{LookPath: installed("test-agent")})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	want := map[string]string{"TEST_AGENT_MODE": "approve", "TEST_AGENT_ALLOW_ALL": ""}
	if !reflect.DeepEqual(plan.Env, want) {
		t.Fatalf("env = %v, want %v", plan.Env, want)
	}
	// The plan holds a copy: changing it cannot change what the next plan gets.
	plan.Env["TEST_AGENT_MODE"] = "yolo"
	again, _ := PlanResume(session, PlanOptions{LookPath: installed("test-agent")})
	if again.Env["TEST_AGENT_MODE"] != "approve" {
		t.Fatal("a plan's environment must not alias the registry's")
	}

	claude, err := PlanResume(resumableSession(t, HarnessClaude, "s-2"), PlanOptions{LookPath: allRuntimes})
	if err != nil || claude.Env != nil {
		t.Fatalf("a runtime with no overrides plans none: %v %v", claude.Env, err)
	}
}

func TestLaunchAppliesThePlansEnvironment(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	script := filepath.Join(t.TempDir(), "fake-agent")
	writeFixture(t, script, "#!/bin/sh\necho \"mode=$TEST_AGENT_MODE allow=${TEST_AGENT_ALLOW_ALL-unset}\"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_AGENT_MODE", "auto")
	t.Setenv("TEST_AGENT_ALLOW_ALL", "true")
	var stdout strings.Builder
	plan := Plan{Executable: script, Dir: t.TempDir(), Env: map[string]string{"TEST_AGENT_MODE": "approve", "TEST_AGENT_ALLOW_ALL": ""}}
	if _, err := Launch(plan, nil, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "mode=approve allow=unset" {
		t.Fatalf("runtime saw %q", got)
	}
}

// A runtime whose sessions Beacon knows only from the runtime log continues them in a new session
// of its own, with its approvals left on.
func TestPlanResumeContinuesARuntimeLogSessionInItsOwnRuntime(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "brief.md")
	session := Session{Harness: HarnessGoose, ID: "goose-1", Directory: t.TempDir()}
	plan, err := PlanResume(session, PlanOptions{FromRuntimeLog: true, BriefPath: brief, LookPath: allRuntimes})
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Mode != ModeNewSession || plan.Reason != ReasonFromRuntimeLog || plan.Target != HarnessGoose {
		t.Fatalf("plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.Env, map[string]string{"GOOSE_MODE": "approve"}) {
		t.Fatalf("env = %v, want goose held to approve mode", plan.Env)
	}
}

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]string{}
	for _, r := range runtimes {
		if r.Label == "" {
			t.Errorf("%s has no label", r.Harness)
		}
		// Events name a runtime by its normalized harness; one the schema spells differently
		// would never match its own sessions.
		if got := asymptoteobserve.NormalizeHarnessName(r.Harness); got != r.Harness {
			t.Errorf("%s normalizes to %s", r.Harness, got)
		}
		for _, name := range append([]string{r.Harness}, r.Aliases...) {
			if other, dup := seen[name]; dup {
				t.Errorf("%q names both %s and %s", name, other, r.Harness)
			}
			seen[name] = r.Harness
			if got, err := ParseHarness(name); err != nil || got != r.Harness {
				t.Errorf("ParseHarness(%q) = %q, %v; want %s", name, got, err, r.Harness)
			}
		}
		if r.NewSource == nil && r.Command == nil {
			t.Errorf("%s can neither be read nor started", r.Harness)
		}
		if c := r.Command; c != nil {
			if c.Executable == "" || (c.Resume == nil && c.NewSession == nil) {
				t.Errorf("%s has a command that starts nothing", r.Harness)
			}
			if r.NewSource == nil && c.Resume != nil {
				t.Errorf("%s reopens sessions by id but Beacon reads none of its sessions to reopen", r.Harness)
			}
			if c.NewSession != nil {
				args := c.NewSession("PROMPT")
				if len(args) == 0 || args[len(args)-1] != "PROMPT" {
					t.Errorf("%s does not pass the prompt as its last argument: %q", r.Harness, args)
				}
			}
		}
	}
}

// A new session is linked to the one it continues only through the marker in its first prompt, so
// every runtime's name has to fit in one.
func TestEveryRuntimeCanBeNamedInAHandoffMarker(t *testing.T) {
	for _, r := range runtimes {
		if asymptoteobserve.HandoffMarker(r.Harness, "s-1") == "" {
			t.Errorf("%s cannot be carried in a handoff marker", r.Harness)
		}
		prompt := NewSessionPrompt(Session{Harness: r.Harness, ID: "s-1"}, "/tmp/brief.md")
		if info, ok := asymptoteobserve.ParseHandoffMarker(prompt); !ok || info.SourceHarness != r.Harness || info.SourceSessionID != "s-1" {
			t.Errorf("%s: the new-session prompt's marker parses back as %+v, %v", r.Harness, info, ok)
		}
	}
}
