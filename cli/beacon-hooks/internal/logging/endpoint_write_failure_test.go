package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
)

// A hook that cannot write the runtime log records nothing, and on DeepSeek Harness that is the
// normal state of a sandboxed session rather than a fault (#605): the harness confines hook
// commands to the session workspace, so ~/.beacon is unreachable. The hook still exits 0 -- a
// telemetry failure must never become a block -- which makes stderr the only channel that reaches
// anyone at the time. These tests pin what goes there.

// unwritableEndpointLog returns a runtime log path that cannot be created on any platform: its
// parent is a regular file. That stands in for the read-only file system a sandbox presents
// without needing root, a real sandbox, or a permission model that differs between Unix and
// Windows.
func unwritableEndpointLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	return filepath.Join(blocker, "runtime.jsonl")
}

// captureStderr runs fn with os.Stderr redirected to a file and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	orig := os.Stderr
	os.Stderr = f
	func() {
		defer func() { os.Stderr = orig }()
		fn()
	}()
	if err := f.Close(); err != nil {
		t.Fatalf("close stderr capture: %v", err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	return string(data)
}

// isolateHookHome keeps the per-runtime state directories NewLoggerForPlatform creates out of the
// real home. They are resolved once at package init, so setting HOME alone would not move them.
func isolateHookHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	origDsh, origCursor := config.DshDir, config.CursorDir
	config.DshDir = filepath.Join(home, ".beacon", "dsh")
	config.CursorDir = filepath.Join(home, ".beacon", "cursor")
	t.Cleanup(func() { config.DshDir, config.CursorDir = origDsh, origCursor })
}

func TestEndpointWriteFailureExplainsTheDshSandbox(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewLoggerForPlatform("session-start", "dsh")
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent session started", nil)
	})
	if writeErr == nil {
		t.Fatalf("EndpointEvent returned nil for an unwritable log; the caller could not tell the event was lost")
	}
	for _, want := range []string{
		"this event was NOT recorded",
		logPath,
		"DeepSeek Harness",
		"danger-full-access",
		"beacon endpoint dsh sync",
		"beacon endpoint doctor",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr does not mention %q:\n%s", want, stderr)
		}
	}
}

// The explanation is a paragraph, and one hook invocation can emit several events. It is printed
// once per path per process so a runtime that shows hook stderr does not repeat it per event.
func TestEndpointWriteFailureIsExplainedOncePerPath(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	stderr := captureStderr(t, func() {
		logger := NewLoggerForPlatform("post-tool", "dsh")
		for i := 0; i < 3; i++ {
			_ = logger.EndpointEvent("tool.completed", "tool", "info", "Tool completed", nil)
		}
	})
	if got := strings.Count(stderr, "this event was NOT recorded"); got != 1 {
		t.Fatalf("explanation printed %d times, want once:\n%s", got, stderr)
	}
	// The terse per-event line is kept: it is what a log reader greps for, and it is one line.
	if got := strings.Count(stderr, "failed to write endpoint event"); got != 3 {
		t.Fatalf("per-event error line printed %d times, want 3:\n%s", got, stderr)
	}
}

// Other runtimes get the generic explanation. Naming DeepSeek Harness's sandbox on a Cursor hook
// would send an operator looking for a setting that does not exist there.
func TestEndpointWriteFailureOnOtherRuntimesDoesNotBlameDsh(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	stderr := captureStderr(t, func() {
		logger := NewLoggerForPlatform("session-start", "cursor")
		_ = logger.EndpointEvent("session.started", "session", "info", "Agent session started", nil)
	})
	if !strings.Contains(stderr, "this event was NOT recorded") || !strings.Contains(stderr, "beacon endpoint doctor") {
		t.Fatalf("stderr lacks the generic explanation:\n%s", stderr)
	}
	if strings.Contains(stderr, "DeepSeek") || strings.Contains(stderr, "dsh sync") {
		t.Fatalf("a non-dsh hook blamed the DeepSeek Harness sandbox:\n%s", stderr)
	}
}

// A write that succeeds says nothing. Several runtimes surface any hook stderr to the person at the
// keyboard, so noise on the happy path would be a regression of its own.
func TestEndpointWriteSuccessIsSilentOnStderr(t *testing.T) {
	isolateHookHome(t)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	stderr := captureStderr(t, func() {
		logger := NewLoggerForPlatform("session-start", "dsh")
		if err := logger.EndpointEvent("session.started", "session", "info", "Agent session started", nil); err != nil {
			t.Errorf("EndpointEvent: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("a successful write printed to stderr:\n%s", stderr)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("runtime log was not written: %v", err)
	}
}
