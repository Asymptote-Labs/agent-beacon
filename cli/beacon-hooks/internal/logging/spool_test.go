package logging

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
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

// stageDshEvent stages one session.started event for sessionID with the given workspace and
// returns what EndpointEvent printed on stderr and the error it reported.
func stageDshEvent(t *testing.T, sessionID, workspace string) (string, error) {
	t.Helper()
	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewSessionLogger("session-start", "dsh", sessionID)
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": sessionID, "working_directory": workspace},
			})
	})
	return stderr, writeErr
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// The spool holds retained content inside what is usually a repository, and agents commit
// with `git add -A`. The ignore file the hook writes inside the spool directory must keep it
// out of git in a plain checkout and in a linked worktree (where .git is a file and an
// .git/info/exclude entry would not apply), without touching any file Beacon does not own.
func TestFirstSpoolUseKeepsTheSpoolOutOfGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", worktree)
	excludeBefore, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))

	for _, workspace := range []string{filepath.Join(root, "pkg", "app"), worktree} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ { // the second stage proves an existing ignore file is left alone
			if stderr, err := stageDshEvent(t, "sess-git-1", workspace); err != nil || stderr != "" {
				t.Fatalf("stage in %s: err=%v stderr=%q", workspace, err, stderr)
			}
		}
		if status := runGit(t, workspace, "status", "--porcelain", "--untracked-files=all", "--ignored=no"); strings.TrimSpace(status) != "" {
			t.Errorf("spool visible to git in %s:\n%s", workspace, status)
		}
		runGit(t, workspace, "add", "-A")
		if staged := runGit(t, workspace, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
			t.Errorf("git add -A staged spool files in %s:\n%s", workspace, staged)
		}
		if _, err := os.Stat(filepath.Join(workspace, ".gitignore")); !os.IsNotExist(err) {
			t.Errorf("the workspace's own .gitignore was touched in %s: %v", workspace, err)
		}
	}
	excludeAfter, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if string(excludeBefore) != string(excludeAfter) {
		t.Errorf(".git/info/exclude changed:\nbefore:\n%s\nafter:\n%s", excludeBefore, excludeAfter)
	}
}

// A workspace that is not a git worktree is the common non-repo case: staging works and the
// ignore file is written all the same, so a directory that becomes a repository later is
// already covered.
func TestSpoolOutsideAGitWorktreeStillWritesTheIgnoreFile(t *testing.T) {
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	workspace := t.TempDir()
	if _, err := stageDshEvent(t, "sess-nogit-1", workspace); err != nil {
		t.Fatalf("staged write outside a repo: %v", err)
	}
	spoolDir := filepath.Join(workspace, ".beacon", "dsh-spool")
	if _, err := os.Stat(filepath.Join(spoolDir, "sess-nogit-1.jsonl")); err != nil {
		t.Fatalf("spool file missing outside a repo: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(spoolDir, ".gitignore"))
	if err != nil || !strings.Contains(string(data), "\n*\n") {
		t.Fatalf("spool ignore file = %q, %v; want a `*` line", data, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git appeared in a non-repo workspace: %v", err)
	}
}

// Staged events carry the same retained content as the runtime log, in a directory that is
// usually readable by more than its owner, so the spool is private to the session's user.
func TestSpoolFilesArePrivateToTheUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	workspace := t.TempDir()
	if _, err := stageDshEvent(t, "sess-mode-1", workspace); err != nil {
		t.Fatal(err)
	}
	spoolDir := filepath.Join(workspace, ".beacon", "dsh-spool")
	for path, want := range map[string]os.FileMode{
		spoolDir: 0o700,
		filepath.Join(spoolDir, "sess-mode-1.jsonl"):      0o600,
		filepath.Join(spoolDir, "sess-mode-1.jsonl.lock"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

// The drain runs outside the sandbox and refuses a spool reached through a symlink, so the
// hook must not stage into one either: it would lose the event without saying so. The loss
// is reported, and nothing lands where the link points.
func TestSymlinkedSpoolDirectoryIsNotStagedInto(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	workspace := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(workspace, ".beacon")); err != nil {
		t.Fatal(err)
	}
	stderr, err := stageDshEvent(t, "sess-link-1", workspace)
	if err == nil {
		t.Fatal("EndpointEvent reported success for an event staged through a symlink")
	}
	if !strings.Contains(stderr, "this event was NOT recorded") {
		t.Fatalf("loss not reported:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(target, "dsh-spool", "sess-link-1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("event written through the symlink: %v", err)
	}
}

// The drain accepts a staged event only when it names the spool's own session; an event the
// hook would stage under a different session id is reported as lost instead.
func TestEventNamingAnotherSessionIsNotStaged(t *testing.T) {
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	workspace := t.TempDir()
	var writeErr error
	stderr := captureStderr(t, func() {
		logger := NewSessionLogger("session-start", "dsh", "sess-own-1")
		writeErr = logger.EndpointEvent("session.started", "session", "info", "Agent started",
			map[string]interface{}{
				"session": map[string]interface{}{"id": "sess-other-1", "working_directory": workspace},
			})
	})
	if writeErr == nil || !strings.Contains(stderr, "this event was NOT recorded") {
		t.Fatalf("err=%v stderr=%q; want the loss reported", writeErr, stderr)
	}
	if files := asymptoteobserve.DSHSpoolFiles(workspace, "sess-own-1"); len(files) != 0 {
		t.Fatalf("staged %v", files)
	}
}

// The drain trusts a staged event.id only when it re-derives from the line itself. Every
// line the hook writer stages must pass that check, or genuine events would lose their
// redelivery protection.
func TestStagedEventIDVerifiesAgainstTheLine(t *testing.T) {
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	workspace := t.TempDir()
	logger := NewSessionLogger("pre-tool", "dsh", "sess-id-1")
	for _, fields := range []map[string]interface{}{
		{"session": map[string]interface{}{"id": "sess-id-1", "working_directory": workspace}},
		{
			"session": map[string]interface{}{"id": "sess-id-1", "working_directory": workspace},
			"tool":    map[string]interface{}{"name": "run_shell", "input": map[string]interface{}{"command": "echo <a&b> 1e21 \u2028", "timeout": 1.5, "n": 9007199254740993}},
			"command": map[string]interface{}{"line": "echo hi", "exit_code": 0},
		},
	} {
		if err := logger.EndpointEvent("tool.invoked", "tool", "info", "Tool <invoked> & done", fields); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".beacon", "dsh-spool", "sess-id-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 staged lines, got %d", len(lines))
	}
	for _, line := range lines {
		if id, ok := asymptoteobserve.VerifiedEventID([]byte(line)); !ok {
			t.Errorf("staged event.id %q does not verify:\n%s", id, line)
		}
	}
}
