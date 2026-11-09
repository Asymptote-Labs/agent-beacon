package state

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
)

func setupTestStateDir(t *testing.T) (tmpDir string, cleanup func()) {
	t.Helper()
	tmpDir = t.TempDir()

	origCursorDir := config.CursorDir
	config.CursorDir = tmpDir
	os.MkdirAll(tmpDir, 0755)

	return tmpDir, func() {
		config.CursorDir = origCursorDir
	}
}

func TestSessionStateIsolatedBySession(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	if err := NewSessionState("session-a", "cursor").SetPromptEmitted(); err != nil {
		t.Fatalf("SetPromptEmitted: %v", err)
	}

	if !NewSessionState("session-a", "cursor").HasPromptEmitted() {
		t.Error("session-a prompt_emitted = false, want true")
	}
	if NewSessionState("session-b", "cursor").HasPromptEmitted() {
		t.Error("session-b prompt_emitted = true, want false")
	}
}

// State files written by older hooks carry evaluations, reprompt_count and model keys. They must
// still load, and the sweep must remove their stale sessions.
func TestLegacyStateFileLoads(t *testing.T) {
	tmpDir, cleanup := setupTestStateDir(t)
	defer cleanup()

	fresh := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	legacy := `{
  "fresh": {"evaluations": [{"evaluation_id": "e1", "file_path": "/a.go", "created_at": "` + fresh + `"}], "reprompt_count": 2, "model": "gpt-5.5", "prompt_emitted": true, "created_at": "` + fresh + `"},
  "stale": {"evaluations": [], "reprompt_count": 3, "model": "gpt-5.5", "created_at": "` + stale + `"}
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "state.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	if !NewSessionState("fresh", "cursor").HasPromptEmitted() {
		t.Fatal("legacy session prompt_emitted = false, want true")
	}
	if got := CleanupStaleForPlatform("cursor"); got != 1 {
		t.Fatalf("CleanupStaleForPlatform = %d, want 1 stale session removed", got)
	}
	if !NewSessionState("fresh", "cursor").HasPromptEmitted() {
		t.Fatal("fresh legacy session lost by the sweep")
	}
}

func TestSessionStateSurfacesSaveFailure(t *testing.T) {
	tmpDir := t.TempDir()
	stateDirPath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(stateDirPath, []byte("file"), 0644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	origCursorDir := config.CursorDir
	config.CursorDir = stateDirPath
	t.Cleanup(func() {
		config.CursorDir = origCursorDir
	})

	st := NewSessionState("test-session", "cursor")
	if err := st.SetPromptEmitted(); err == nil {
		t.Fatal("SetPromptEmitted returned nil, want save failure")
	}
}

// TestHelperProcessRecord is not a test: it is the body of the child processes started by
// TestStateIsSafeAcrossProcesses, which stand in for concurrent hook invocations.
func TestHelperProcessRecord(t *testing.T) {
	dir := os.Getenv("BEACON_STATE_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	config.CursorDir = dir
	proc := os.Getenv("BEACON_STATE_HELPER_PROC")
	for i := 0; i < 40; i++ {
		if err := NewSessionState(fmt.Sprintf("session-%s-%d", proc, i), "cursor").SetPromptEmitted(); err != nil {
			t.Fatalf("SetPromptEmitted: %v", err)
		}
	}
}

// Hooks run as separate processes, so the state file needs cross-process exclusion: the
// in-process mutex cannot stop two hook invocations from losing each other's update.
func TestStateIsSafeAcrossProcesses(t *testing.T) {
	dir, cleanup := setupTestStateDir(t)
	defer cleanup()

	const procs, perProc = 4, 40
	var cmds []*exec.Cmd
	var outputs []*bytes.Buffer
	for i := 0; i < procs; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessRecord$")
		cmd.Env = append(os.Environ(), "BEACON_STATE_HELPER_DIR="+dir, fmt.Sprintf("BEACON_STATE_HELPER_PROC=%d", i))
		out := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = out, out
		outputs = append(outputs, out)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		cmds = append(cmds, cmd)
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v\n%s", err, outputs[i])
		}
	}

	for i := 0; i < procs; i++ {
		for j := 0; j < perProc; j++ {
			if !NewSessionState(fmt.Sprintf("session-%d-%d", i, j), "cursor").HasPromptEmitted() {
				t.Fatalf("session-%d-%d missing: concurrent processes lost updates", i, j)
			}
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "state.json.*")); len(leftovers) > 0 {
		for _, f := range leftovers {
			if filepath.Base(f) != "state.json.lock" {
				t.Fatalf("temporary state file left behind: %s", f)
			}
		}
	}
}

func TestMarkReplyRecordedOncePerReplyAndSession(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	a := NewSessionState("session-a", "cursor")
	for i, want := range []bool{true, false} {
		fresh, err := a.MarkReplyRecorded("msg-1")
		if err != nil || fresh != want {
			t.Fatalf("mark %d = %v, %v; want %v", i+1, fresh, err, want)
		}
	}
	// A later reply is new, and a reply id is per session.
	if fresh, _ := NewSessionState("session-a", "cursor").MarkReplyRecorded("msg-2"); !fresh {
		t.Fatal("a new reply id was treated as already recorded")
	}
	if fresh, _ := NewSessionState("session-b", "cursor").MarkReplyRecorded("msg-2"); !fresh {
		t.Fatal("another session's reply id suppressed this session's")
	}
	// The marker sits beside the other session data without disturbing it.
	if err := a.SetPromptEmitted(); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := a.MarkReplyRecorded("msg-2"); fresh {
		t.Fatal("setting prompt_emitted reset the recorded reply")
	}
}

// Stale-session cleanup runs on every SessionStart and SessionEnd of the platform and removes
// sessions older than two hours, which is every Factory session. A session's
// reply marker has to survive it, or a Stop over an unchanged transcript records the reply again.
func TestCleanupKeepsARecentReplyMarker(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	expired := time.Now().Add(-replyMarkerTTL - time.Hour).UTC().Format(time.RFC3339)
	st := NewSessionState("seed", "cursor")
	state := map[string]*SessionData{
		"recent-reply":  {CreatedAt: old, LastReplyID: "m1", LastReplyAt: time.Now().UTC().Format(time.RFC3339)},
		"expired-reply": {CreatedAt: old, LastReplyID: "m2", LastReplyAt: expired},
		"no-reply":      {CreatedAt: old},
	}
	if err := st.saveStateFile(state); err != nil {
		t.Fatal(err)
	}

	CleanupStaleForPlatform("cursor")

	after := st.loadStateFile()
	if after["recent-reply"] == nil {
		t.Fatal("cleanup removed a session with a recent reply marker")
	}
	if after["expired-reply"] != nil || after["no-reply"] != nil {
		t.Fatalf("cleanup kept stale sessions: %v", after)
	}
	if fresh, _ := NewSessionState("recent-reply", "cursor").MarkReplyRecorded("m1"); fresh {
		t.Fatal("the reply was treated as new after cleanup")
	}
}

// Antigravity recovers a session's first prompt from its transcript, and a conversation resumed
// after the two-hour sweep still starts with that prompt. Its marker has to survive the sweep for
// promptMarkerTTL, measured from when it was set (or, for a marker written before that was
// recorded, from the session's creation), or the resumed conversation records the prompt again.
func TestCleanupKeepsARecentPromptMarker(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	expired := time.Now().Add(-promptMarkerTTL - time.Hour).UTC().Format(time.RFC3339)
	st := NewSessionState("seed", "cursor")
	state := map[string]*SessionData{
		"recent-prompt":  {CreatedAt: old, PromptEmitted: true, PromptEmittedAt: old},
		"legacy-prompt":  {CreatedAt: old, PromptEmitted: true},
		"expired-prompt": {CreatedAt: expired, PromptEmitted: true, PromptEmittedAt: expired},
		"expired-legacy": {CreatedAt: expired, PromptEmitted: true},
		"no-prompt":      {CreatedAt: old},
	}
	if err := st.saveStateFile(state); err != nil {
		t.Fatal(err)
	}

	if got := CleanupStaleForPlatform("cursor"); got != 3 {
		t.Fatalf("CleanupStaleForPlatform = %d, want 3", got)
	}

	after := st.loadStateFile()
	if after["recent-prompt"] == nil || after["legacy-prompt"] == nil {
		t.Fatalf("cleanup removed a session with a recent prompt marker: %v", after)
	}
	if claimed, _ := NewSessionState("recent-prompt", "cursor").MarkPromptEmittedIfNeeded(); claimed {
		t.Fatal("the first prompt was claimed again after cleanup")
	}
}

func TestMarkPromptEmittedRecordsWhen(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("s1", "cursor")
	if claimed, err := st.MarkPromptEmittedIfNeeded(); err != nil || !claimed {
		t.Fatalf("MarkPromptEmittedIfNeeded = %v, %v", claimed, err)
	}
	data := st.loadStateFile()["s1"]
	at, err := time.Parse(time.RFC3339, data.PromptEmittedAt)
	if err != nil || time.Since(at) > time.Minute {
		t.Fatalf("prompt_emitted_at = %q, want the time the marker was set", data.PromptEmittedAt)
	}
}
