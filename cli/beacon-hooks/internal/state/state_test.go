package state

import (
	"os"
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
