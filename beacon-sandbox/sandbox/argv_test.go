package sandbox

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// buildArgv is the highest-risk pure function here. An earlier version used `su -`, which starts
// a *login* shell and resets the environment -- stripping the injected credential, so Claude Code
// could not authenticate, so no tool ran, so the resulting telemetry gap looked exactly like a
// Beacon capture bug. These tests exist to make that regression impossible to reintroduce
// silently.

func TestUserExecPreservesEnvironment(t *testing.T) {
	argv := buildArgv("echo hi", ExecOpts{User: "agent", PreserveEnv: true})

	if got := argv[0]; got != "su" {
		t.Fatalf("expected su, got %q (argv=%v)", got, argv)
	}
	if argv[1] != "-p" {
		t.Fatalf("PreserveEnv must use `su -p`, got %q -- `su -` resets the environment and "+
			"strips the injected credential", argv[1])
	}
	for _, a := range argv {
		if a == "-" {
			t.Fatal("`su -` must never appear when PreserveEnv is set")
		}
	}
}

func TestUserExecWithoutPreserveEnvUsesLoginShell(t *testing.T) {
	argv := buildArgv("echo hi", ExecOpts{User: "agent"})
	if argv[1] != "-" {
		t.Fatalf("without PreserveEnv a login shell is intended, got %q", argv[1])
	}
}

func TestRootExecSkipsSuEntirely(t *testing.T) {
	for _, user := range []string{"", "root"} {
		argv := buildArgv("echo hi", ExecOpts{User: user})
		if argv[0] != "bash" || argv[1] != "-c" {
			t.Errorf("user %q should run directly via bash -c, got %v", user, argv)
		}
		if strings.Contains(strings.Join(argv, " "), "su ") {
			t.Errorf("user %q must not switch user, got %v", user, argv)
		}
	}
}

// HOME, PATH, and cd must all be established before the script runs, and in that order: the
// script may reference $HOME, and cd may be relative to it.
func TestPreludeOrderingAndContent(t *testing.T) {
	argv := buildArgv("beacon version", ExecOpts{
		User: "agent", PreserveEnv: true,
		HomeDir: "/home/agent", Dir: "/home/agent/work",
		PathPrepend: []string{"/home/agent/.local/bin", "/opt/beacon/bin"},
	})
	script := argv[len(argv)-1]

	iHome := strings.Index(script, "export HOME=")
	iPath := strings.Index(script, "export PATH=")
	iCd := strings.Index(script, "cd ")
	iBody := strings.Index(script, "beacon version")

	for name, idx := range map[string]int{"HOME": iHome, "PATH": iPath, "cd": iCd, "body": iBody} {
		if idx < 0 {
			t.Fatalf("script is missing %s: %q", name, script)
		}
	}
	if !(iHome < iPath && iPath < iCd && iCd < iBody) {
		t.Errorf("expected HOME < PATH < cd < body, got %d < %d < %d < %d in %q",
			iHome, iPath, iCd, iBody, script)
	}
	// PATH must be prepended, not replaced, or the image's own tools disappear.
	if !strings.Contains(script, ":$PATH") {
		t.Errorf("PATH must be prepended to the existing value, got %q", script)
	}
}

func TestPreludeOmittedWhenUnset(t *testing.T) {
	argv := buildArgv("true", ExecOpts{User: "root"})
	script := argv[len(argv)-1]
	if script != "true" {
		t.Errorf("with no HOME/PATH/Dir the script should be passed through verbatim, got %q", script)
	}
}

// Paths come from scenario YAML and image constants, so a quote or space must not be able to
// break out of the command.
func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	cases := map[string]string{
		"plain":       `'plain'`,
		"with space":  `'with space'`,
		"it's":        `'it'\''s'`,
		"$(whoami)":   `'$(whoami)'`,
		"a'b'c":       `'a'\''b'\''c'`,
		"":            `''`,
		"semi;colon":  `'semi;colon'`,
		"back\\slash": `'back\slash'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuotedPathsSurviveInPrelude(t *testing.T) {
	argv := buildArgv("true", ExecOpts{
		User: "agent", PreserveEnv: true,
		HomeDir: "/home/o'brien", Dir: "/home/o'brien/my work",
	})
	script := argv[len(argv)-1]
	// The escaped form, not the raw apostrophe, is what must appear.
	if strings.Contains(script, "HOME=/home/o'brien;") {
		t.Errorf("home dir was not quoted: %q", script)
	}
	if !strings.Contains(script, `'/home/o'\''brien'`) {
		t.Errorf("expected escaped home dir in %q", script)
	}
}

// The credential is injected as environment, never as an argument; anything in argv is visible
// in the process table to every user on the host.
func TestScriptIsTheFinalArgumentAndCarriesNoSecret(t *testing.T) {
	argv := buildArgv("claude -p 'hello'", ExecOpts{User: "agent", PreserveEnv: true})
	if got := argv[len(argv)-1]; !strings.Contains(got, "claude -p") {
		t.Errorf("the script must be the last argument, got %v", argv)
	}
	if len(argv) != 5 {
		t.Errorf("expected su -p <user> -c <script>, got %v", argv)
	}
}

// Reading stdout to completion before touching stderr deadlocks whenever the guest fills its stderr
// pipe buffer: the process blocks on that write, never exits, never closes stdout, and the read
// never returns. Verbose failures are exactly when that happens -- exactly when this tool is being
// used to find out what went wrong. Reported by the Copilot reviewer.
//
// The test reproduces the ordering that hangs: a writer that emits stderr *before* stdout. With
// unbuffered pipes a sequential reader waits on stdout while the writer waits on stderr, so only a
// concurrent drain can finish.
func TestDrainStreamsDoesNotDeadlockWhenStderrComesFirst(t *testing.T) {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()

	go func() {
		// Order matters: stderr first is what a sequential reader cannot survive.
		_, _ = errW.Write([]byte("a verbose failure explanation"))
		_ = errW.Close()
		_, _ = outW.Write([]byte("some stdout"))
		_ = outW.Close()
	}()

	type result struct{ out, errb string }
	done := make(chan result, 1)
	go func() {
		o, e, oErr, eErr := drainStreams(outR, errR)
		if oErr != nil || eErr != nil {
			t.Errorf("unexpected read errors: %v / %v", oErr, eErr)
		}
		done <- result{string(o), string(e)}
	}()

	select {
	case got := <-done:
		if got.out != "some stdout" {
			t.Errorf("stdout = %q", got.out)
		}
		if got.errb != "a verbose failure explanation" {
			t.Errorf("stderr = %q", got.errb)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drainStreams deadlocked: stderr written before stdout requires a concurrent drain")
	}
}

// A read error must be returned, not swallowed: a truncated stream means the captured output the
// verdict is built from is incomplete.
func TestDrainStreamsSurfacesReadErrors(t *testing.T) {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	boom := errors.New("stream broke")

	go func() {
		_ = outW.CloseWithError(boom)
		_, _ = errW.Write([]byte("fine"))
		_ = errW.Close()
	}()

	_, errb, outErr, errErr := drainStreams(outR, errR)
	if outErr == nil {
		t.Error("a broken stdout stream must be reported")
	}
	if errErr != nil {
		t.Errorf("stderr read should have succeeded, got %v", errErr)
	}
	if string(errb) != "fine" {
		t.Errorf("stderr = %q", errb)
	}
}
