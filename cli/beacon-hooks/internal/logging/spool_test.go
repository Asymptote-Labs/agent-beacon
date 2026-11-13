package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// stagedSpoolFile is where a hook of harness stages events for sessionID in workspace.
func stagedSpoolFile(workspace, harness, sessionID string) string {
	return localingest.Spool{Dir: filepath.Join(workspace, ".beacon", "spool", harness, sessionID)}.FilePath()
}

// When the runtime log is unreachable but the workspace is writable -- the normal
// workspace-write sandbox DeepSeek Harness runs hooks in (#605) -- a dsh hook stages the
// event in the session workspace instead of losing it, to be drained later; at capture time
// the event is recorded, so nothing is reported as lost and stderr stays silent (one process
// per event: a note here would be a note on every event).

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
	spool := stagedSpoolFile(workspace, "deepseek_harness", "sess-stage-1")
	data, err := os.ReadFile(spool)
	if err != nil {
		t.Fatalf("staged event not in spool: %v", err)
	}
	// The staged line holds the same prompts and output as the log, and a workspace outside the
	// home directory is no reason to share it: the spool is private wherever it lives.
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{spool: 0o600, filepath.Dir(spool): 0o700} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s mode = %v, want %v", path, got, want)
			}
		}
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

// Staging is not DeepSeek Harness's alone: any harness whose hook is confined to the session
// workspace (#1284) stages there under its own canonical name, and the hook carries on. The first
// event a session stages prints one note naming the drain, since nothing tells the hook a sweep
// will find the session; later ones are silent. Once a later hook of the same session reaches the
// log, it drains that spool -- but only what AdmitStagedLine admits for this session and harness,
// since anything that can write the workspace can write the spool.
func TestOtherHarnessStagesThenDrainsOnItsNextWrite(t *testing.T) {
	isolateHookHome(t)
	unwritable := unwritableEndpointLog(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritable)
	workspace := t.TempDir()
	const sessionID = "sess-oc-1"
	emit := func(message string) error {
		logger := NewSessionLogger("tool-use", "opencode", sessionID)
		return logger.EndpointEvent("tool.invoked", "tool", "info", message,
			map[string]interface{}{
				"session": map[string]interface{}{"id": sessionID, "working_directory": workspace},
			})
	}

	var writeErr error
	stderr := captureStderr(t, func() { writeErr = emit("staged while sandboxed") })
	drainCmd := "beacon endpoint spool drain --log-path '" + unwritable + "' --workspace '" + workspace + "' --harness 'opencode' --session '" + sessionID + "'"
	if writeErr != nil || strings.Contains(stderr, "NOT recorded") || !strings.Contains(stderr, drainCmd) {
		t.Fatalf("staging = %v, stderr %q; want nil and one note naming this session's drain", writeErr, stderr)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, ".beacon", "spool", ".gitignore")); err != nil || !strings.Contains(string(data), "\n*\n") {
		t.Fatalf("the spool has no .gitignore hiding it from git: %q, %v", data, err)
	}
	spool := stagedSpoolFile(workspace, "opencode", sessionID)
	data, err := os.ReadFile(spool)
	if err != nil {
		t.Fatalf("opencode event not staged in its workspace spool: %v", err)
	}
	if stderr = captureStderr(t, func() { writeErr = emit("staged again") }); writeErr != nil || stderr != "" {
		t.Fatalf("second staged event = %v, stderr %q; want a silent nil", writeErr, stderr)
	}

	// Lines planted next to the genuine one, each differing from it in one field the drain must
	// check: another session, another harness, and an action only Beacon writes.
	var planted [][]byte
	for _, mutate := range []func(map[string]interface{}){
		func(e map[string]interface{}) { e["session"].(map[string]interface{})["id"] = "sess-other" },
		func(e map[string]interface{}) { e["harness"].(map[string]interface{})["name"] = "claude_code" },
		func(e map[string]interface{}) {
			e["event"].(map[string]interface{})["action"] = "session.share_requested"
		},
	} {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
			t.Fatal(err)
		}
		mutate(event)
		event["message"] = "planted"
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		planted = append(planted, line)
	}
	// And one the drain admits -- this session, this harness -- written by hand with a secret in
	// it: the drain writes it the way the hook writes every line, redacted, never byte for byte.
	const secret = "ghp_wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLE1"
	var admitted map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &admitted); err != nil {
		t.Fatal(err)
	}
	admitted["message"] = "hand-written " + secret
	handWritten, err := json.Marshal(admitted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (localingest.Spool{Dir: filepath.Dir(spool)}).Append(append(planted, handWritten)); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	stderr = captureStderr(t, func() { writeErr = emit("written directly") })
	if writeErr != nil || stderr != "" {
		t.Fatalf("direct write = %v, stderr %q", writeErr, stderr)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	for _, want := range []string{"written directly", "staged while sandboxed", "staged again"} {
		if strings.Count(log, want) != 1 {
			t.Errorf("log holds %q %d times, want once:\n%s", want, strings.Count(log, want), log)
		}
	}
	if strings.Contains(log, "planted") {
		t.Errorf("a planted spool line reached the log:\n%s", log)
	}
	if !strings.Contains(log, "hand-written") || strings.Contains(log, secret) {
		t.Errorf("an admitted spool line was not written through the hook's encoder (redacted):\n%s", log)
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Errorf("spool still holds lines after the drain (refused ones must be discarded): %v", err)
	}
}

// A rootful container sandbox runs the agent as uid 0 over the user's bind-mounted checkout. Staged
// there, the spool would be root's 0700/0600 files the user could neither drain nor delete, so the
// event is reported as not recorded instead. Root that owns the workspace (uid 0 in a rootless
// container owns the mapped one) stages as any account does.
func TestRootStagesOnlyInAWorkspaceItOwns(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	emit := func(session, workspace string) (error, string) {
		var writeErr error
		stderr := captureStderr(t, func() {
			writeErr = NewSessionLogger("tool-use", "opencode", session).EndpointEvent("tool.invoked", "tool", "info", "as root",
				map[string]interface{}{"session": map[string]interface{}{"id": session, "working_directory": workspace}})
		})
		return writeErr, stderr
	}

	users := t.TempDir()
	if err := os.Chown(users, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if writeErr, stderr := emit("sess-root-1", users); writeErr == nil || !strings.Contains(stderr, "NOT recorded") {
		t.Fatalf("root's unwritten event in another account's workspace = %v, stderr %q; want it reported as not recorded", writeErr, stderr)
	}
	if _, err := os.Stat(filepath.Join(users, ".beacon")); !os.IsNotExist(err) {
		t.Fatalf("root staged in another account's workspace: %v", err)
	}

	own := t.TempDir()
	if writeErr, _ := emit("sess-root-2", own); writeErr != nil {
		t.Fatalf("root did not stage in a workspace it owns: %v", writeErr)
	}
	if _, err := os.Stat(stagedSpoolFile(own, "opencode", "sess-root-2")); err != nil {
		t.Fatalf("root's own workspace holds no spool: %v", err)
	}
}

// A cloned repository can commit its own .beacon/spool/.gitignore that does not ignore the spool.
// Staged next to it, the session's prompts would go into the agent's next `git add -A`, so nothing
// is staged and the event is reported as not recorded; the repository's file is left as it is.
func TestNoStagingNextToAGitignoreThatDoesNotHideTheSpool(t *testing.T) {
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	for name, content := range map[string]string{"empty": "", "negated": "*\n!*.jsonl\n"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			gitignore := filepath.Join(workspace, ".beacon", "spool", ".gitignore")
			if err := os.MkdirAll(filepath.Dir(gitignore), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gitignore, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			var writeErr error
			captureStderr(t, func() {
				writeErr = NewSessionLogger("tool-use", "opencode", "sess-gi").EndpointEvent("tool.invoked", "tool", "info", "next to a planted .gitignore",
					map[string]interface{}{"session": map[string]interface{}{"id": "sess-gi", "working_directory": workspace}})
			})
			if writeErr == nil {
				t.Fatal("an event next to a planted .gitignore was reported recorded")
			}
			if _, err := os.Stat(stagedSpoolFile(workspace, "opencode", "sess-gi")); !os.IsNotExist(err) {
				t.Fatalf("an event was staged next to a .gitignore that does not hide it: %v", err)
			}
			if data, _ := os.ReadFile(gitignore); string(data) != content {
				t.Fatalf("the repository's .gitignore was changed: %q", data)
			}
		})
	}
}

// A share request is an action only Beacon writes, which every drain refuses. Staged, it would be
// discarded and the session, already marked as requested, never shared; so it is not staged, the
// write fails, and the caller takes its mark back.
func TestShareRequestIsNeverStaged(t *testing.T) {
	isolateHookHome(t)
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	workspace := t.TempDir()
	var writeErr error
	captureStderr(t, func() {
		writeErr = NewSessionLogger("session-start", "opencode", "sess-share-1").EndpointEventWithFidelity(
			"session.share_requested", "session", "info", "Auto-share requested for this session", "inferred",
			map[string]interface{}{"session": map[string]interface{}{"id": "sess-share-1", "working_directory": workspace}})
	})
	if writeErr == nil {
		t.Fatal("an unwritten share request was reported recorded")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".beacon")); !os.IsNotExist(err) {
		t.Fatalf("a share request was staged: %v", err)
	}
}

// A workspace can plant its session directory as a link to another directory the user owns -- the
// account's own spool, say. Draining through it would discard that spool's lines as refused, and
// staging through it would write into it, so the hook does neither.
func TestSpoolDirPlantedAsALinkIsNeitherDrainedNorWritten(t *testing.T) {
	isolateHookHome(t)
	workspace := t.TempDir()
	const sessionID = "sess-link-1"
	victim := localingest.Spool{Dir: t.TempDir()}
	if _, err := victim.Append([][]byte{[]byte(`{"belongs":"elsewhere"}`)}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(victim.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	harnessDir := filepath.Join(workspace, ".beacon", "spool", "opencode")
	if err := os.MkdirAll(harnessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim.Dir, filepath.Join(harnessDir, sessionID)); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	emit := func() error {
		logger := NewSessionLogger("tool-use", "opencode", sessionID)
		return logger.EndpointEvent("tool.invoked", "tool", "info", "through a link",
			map[string]interface{}{
				"session": map[string]interface{}{"id": sessionID, "working_directory": workspace},
			})
	}

	t.Setenv("BEACON_ENDPOINT_LOG", filepath.Join(t.TempDir(), "runtime.jsonl"))
	if err := emit(); err != nil {
		t.Fatalf("direct write: %v", err)
	}
	t.Setenv("BEACON_ENDPOINT_LOG", unwritableEndpointLog(t))
	var writeErr error
	captureStderr(t, func() { writeErr = emit() })
	if writeErr == nil {
		t.Error("an event was reported recorded although its only spool path runs through a link")
	}
	if after, err := os.ReadFile(victim.FilePath()); err != nil || string(after) != string(before) {
		t.Fatalf("the linked-to spool changed: %q (%v), want %q", after, err, before)
	}
}

// An unsafe session id must not produce a spool file anywhere: writer and drainer agree via
// ValidSpoolName that such ids get no spool, so a path-shaped id can neither escape the spool
// directory nor strand events where no drain will ever look.
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
	want := "pkg/app/.beacon/spool/"
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
	spool := stagedSpoolFile(workspace, "deepseek_harness", "sess-nogit-1")
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("spool file missing outside a repo: %v", err)
	}
	for _, up := range []string{".git", "info"} {
		if _, err := os.Stat(filepath.Join(workspace, up)); !os.IsNotExist(err) {
			t.Errorf("%s appeared in a non-repo workspace: %v", up, err)
		}
	}
}
