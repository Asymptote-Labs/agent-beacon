package state

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

func TestSessionModelPersistsToDisk(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st1 := NewSessionState("test-session", "cursor")
	st1.SetModel("gpt-5.5")

	st2 := NewSessionState("test-session", "cursor")
	if got := st2.GetModel(); got != "gpt-5.5" {
		t.Errorf("model = %q, want %q", got, "gpt-5.5")
	}
}

func TestSessionStateIsolatedBySession(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st1 := NewSessionState("session-a", "cursor")
	st2 := NewSessionState("session-b", "cursor")

	st1.SetModel("model-a")
	st2.SetModel("model-b")

	if got := st1.GetModel(); got != "model-a" {
		t.Errorf("session-a model = %q, want model-a", got)
	}
	if got := st2.GetModel(); got != "model-b" {
		t.Errorf("session-b model = %q, want model-b", got)
	}
}

func TestEvaluationsPersistToDisk(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st1 := NewSessionState("test-session", "cursor")
	st1.AddEvaluation("eval-1", "/path/to/file.go")

	st2 := NewSessionState("test-session", "cursor")
	evals := st2.GetPendingEvaluations()
	if len(evals) != 1 || evals[0].EvaluationID != "eval-1" {
		t.Fatalf("evaluations = %#v, want eval-1", evals)
	}
	if evals[0].FilePath != "/path/to/file.go" {
		t.Errorf("file path = %q, want /path/to/file.go", evals[0].FilePath)
	}
}

func TestSessionStateFileLocation(t *testing.T) {
	tmpDir, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")
	st.SetModel("gpt-5.5")

	// Verify state.json was created in the expected location
	stateFile := filepath.Join(tmpDir, "state.json")
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state.json not found at expected path %s: %v", stateFile, err)
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
	if err := st.SetModel("gpt-5.5"); err == nil {
		t.Fatal("SetModel returned nil, want save failure")
	}
}

// TestHelperProcessIncrement is not a test: it is the body of the child processes started by
// TestStateIsSafeAcrossProcesses, which stand in for concurrent hook invocations.
func TestHelperProcessIncrement(t *testing.T) {
	dir := os.Getenv("BEACON_STATE_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	config.CursorDir = dir
	st := NewSessionState("shared-session", "cursor")
	for i := 0; i < 40; i++ {
		if _, err := st.IncrementRepromptCount(); err != nil {
			t.Fatalf("IncrementRepromptCount: %v", err)
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
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessIncrement$")
		cmd.Env = append(os.Environ(), "BEACON_STATE_HELPER_DIR="+dir)
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

	if got, want := NewSessionState("shared-session", "cursor").GetRepromptCount(), procs*perProc; got != want {
		t.Fatalf("reprompt count = %d, want %d: concurrent processes lost updates", got, want)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "state.json.*")); len(leftovers) > 0 {
		for _, f := range leftovers {
			if filepath.Base(f) != "state.json.lock" {
				t.Fatalf("temporary state file left behind: %s", f)
			}
		}
	}
}
