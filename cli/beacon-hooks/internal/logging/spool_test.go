package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// When the runtime log is unreachable but the workspace is writable -- the normal
// workspace-write sandbox DeepSeek Harness runs hooks in (#605) -- a dsh hook stages the
// event in the session workspace instead of losing it. `beacon endpoint dsh sync` drains
// that spool later; at capture time the event is recorded, so nothing is reported as lost
// and stderr stays silent (one process per event: a note here would be a note on every
// event).

func TestDshSandboxedWriteStagesEventInWorkspaceSpool(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	workspace := t.TempDir()

	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewSessionLogger("session-start", "dsh", "sess-stage-1")
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": "sess-stage-1", "working_directory": workspace},
			})
	})
	if writeErr != nil {
		t.Fatalf("EndpointEvent returned an error although the event was staged: %v", writeErr)
	}
	if stderr != "" {
		t.Fatalf("a staged event printed to stderr:\n%s", stderr)
	}
	spool := filepath.Join(workspace, ".beacon", "dsh-spool", "sess-stage-1.jsonl")
	data, err := os.ReadFile(spool)
	if err != nil {
		t.Fatalf("staged event not in spool: %v", err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("spool line is not JSON: %v", err)
	}
	harness := event["harness"].(map[string]interface{})
	if harness["collection_method"] != "hook" {
		t.Errorf("collection_method = %v, want hook (the spool holds deferred hook events)", harness["collection_method"])
	}
	info := event["event"].(map[string]interface{})
	if info["id"] == nil || info["id"] == "" {
		t.Error("staged event has no event.id; a drain redelivery could not be deduplicated")
	}
	session := event["session"].(map[string]interface{})
	if session["working_directory"] != workspace {
		t.Errorf("working_directory = %v, want %v", session["working_directory"], workspace)
	}
}

// Only DeepSeek Harness gets a spool. Staging another runtime's events into a project
// checkout would be new repository pollution with nothing to drain it, so a claude or cursor
// hook that cannot write the log reports the loss exactly as before.
func TestNonDshWriteFailureDoesNotStageASpool(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	workspace := t.TempDir()
	t.Chdir(workspace)

	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewSessionLogger("session-start", "cursor", "sess-notdsh")
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": "sess-notdsh", "working_directory": workspace},
			})
	})
	if writeErr == nil {
		t.Fatal("EndpointEvent returned nil; the caller could not tell the event was lost")
	}
	if !strings.Contains(stderr, "this event was NOT recorded") {
		t.Fatalf("stderr lacks the NOT-recorded explanation:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".beacon")); !os.IsNotExist(err) {
		t.Errorf("a non-dsp hook created a spool directory in the workspace: %v", err)
	}
}

// An unsafe session id must not produce a spool file anywhere: writer and drainer agree via
// ValidDSHSessionIDForSpool that such ids get no spool, so a path-shaped id can neither
// escape the spool directory nor strand events where no drain will ever look.
func TestUnsafeSessionIDFallsBackToReportedLoss(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewSessionLogger("session-start", "dsh", "../escaped")
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": "../escaped", "working_directory": workspace},
			})
	})
	if writeErr == nil {
		t.Fatal("EndpointEvent returned nil for an event that reached no log")
	}
	if !strings.Contains(stderr, "this event was NOT recorded") {
		t.Fatalf("stderr lacks the NOT-recorded explanation:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".beacon")); !os.IsNotExist(err) {
		t.Errorf("unsafe session id created a spool directory: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(workspace) + string(filepath.Separator) + "escaped"); !os.IsNotExist(err) {
		t.Error("unsafe session id escaped the workspace")
	}
}

// When the workspace is unwritable too -- the read-only sandbox mode -- there is no spool
// and the loss is reported exactly as it was before the spool existed: NOT recorded, with
// the dsh sandbox explanation and the sync/doctor remedies.
func TestUnwritableLogAndWorkspaceReportsLossAsBefore(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	// The workspace's parent is a file, so MkdirAll of any path under it fails the way a
	// read-only sandbox makes every write fail.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(blocker, "workspace")

	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewSessionLogger("session-start", "dsh", "sess-ro-1")
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": "sess-ro-1", "working_directory": workspace},
			})
	})
	if writeErr == nil {
		t.Fatal("EndpointEvent returned nil; the caller could not tell the event was lost")
	}
	for _, want := range []string{"this event was NOT recorded", logPath, "DeepSeek Harness", "beacon endpoint dsh sync"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr does not mention %q:\n%s", want, stderr)
		}
	}
}

// First spool use hides the directory from git status via .git/info/exclude (the local,
// unshared mechanism -- never .gitignore), exactly once, relative to the worktree root even
// when the session cwd sits below it.
func TestFirstSpoolUseExcludesTheDirectoryFromGitStatus(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	stage := func() {
		logger := NewSessionLogger("session-start", "dsh", "sess-git-1")
		_ = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": "sess-git-1", "working_directory": sub},
			})
	}
	stage() // second call proves the entry is not appended twice
	stage()

	exclude := filepath.Join(root, ".git", "info", "exclude")
	data, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatalf("exclude file not written: %v", err)
	}
	want := "pkg/app/.beacon/dsh-spool/"
	if got := strings.Count(string(data), want); got != 1 {
		t.Fatalf("exclude lists %q %d times, want once:\n%s", want, got, data)
	}
	if strings.Contains(string(data), ".gitignore") {
		t.Errorf("spool containment must use .git/info/exclude, not .gitignore:\n%s", data)
	}
}

// A workspace that is not a git worktree is the common non-repo case: staging must work and
// simply skip containment without erroring or walking anywhere odd.
func TestSpoolOutsideAGitWorktreeSkipsContainment(t *testing.T) {
	isolateHookHome(t)
	logPath := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	workspace := t.TempDir()

	logger := NewSessionLogger("session-start", "dsh", "sess-nogit-1")
	if err := logger.EndpointEvent("session.started", "session", "info", "Agent started",
		map[string]interface{}{
			"session": map[string]interface{}{"id": "sess-nogit-1", "working_directory": workspace},
		}); err != nil {
		t.Fatalf("staged write outside a repo: %v", err)
	}
	spool := filepath.Join(workspace, ".beacon", "dsh-spool", "sess-nogit-1.jsonl")
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("spool file missing outside a repo: %v", err)
	}
	for _, up := range []string{".git", "info"} {
		if _, err := os.Stat(filepath.Join(workspace, up)); !os.IsNotExist(err) {
			t.Errorf("%s appeared in a non-repo workspace: %v", up, err)
		}
	}
}
