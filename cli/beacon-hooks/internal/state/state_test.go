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

func TestSetSbdPolicies(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")
	st.SetSbdPolicies("policy content", "gen-1")

	policies, generationID, injected := st.GetSbdState()
	if policies != "policy content" {
		t.Errorf("policies = %q, want %q", policies, "policy content")
	}
	if generationID != "gen-1" {
		t.Errorf("generationID = %q, want %q", generationID, "gen-1")
	}
	if injected {
		t.Error("injected should be false after SetSbdPolicies")
	}
}

func TestSetSbdPolicies_ResetsInjectedFlag(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")

	// Set policies and mark as injected
	st.SetSbdPolicies("policy v1", "gen-1")
	st.MarkSbdInjected()

	_, _, injected := st.GetSbdState()
	if !injected {
		t.Fatal("injected should be true after MarkSbdInjected")
	}

	// New generation resets the injected flag
	st.SetSbdPolicies("policy v2", "gen-2")

	policies, generationID, injected := st.GetSbdState()
	if policies != "policy v2" {
		t.Errorf("policies = %q, want %q", policies, "policy v2")
	}
	if generationID != "gen-2" {
		t.Errorf("generationID = %q, want %q", generationID, "gen-2")
	}
	if injected {
		t.Error("injected should be reset to false on new generation")
	}
}

func TestMarkSbdInjected(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")
	st.SetSbdPolicies("policy content", "gen-1")

	st.MarkSbdInjected()

	_, _, injected := st.GetSbdState()
	if !injected {
		t.Error("injected should be true after MarkSbdInjected")
	}
}

func TestGetSbdState_NoSession(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("nonexistent-session", "cursor")

	policies, generationID, injected := st.GetSbdState()
	if policies != "" {
		t.Errorf("policies = %q, want empty", policies)
	}
	if generationID != "" {
		t.Errorf("generationID = %q, want empty", generationID)
	}
	if injected {
		t.Error("injected should be false for nonexistent session")
	}
}

func TestSbdState_PersistsToDisk(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	// Write with one SessionState instance
	st1 := NewSessionState("test-session", "cursor")
	st1.SetSbdPolicies("persisted policy", "gen-1")

	// Read with a fresh instance
	st2 := NewSessionState("test-session", "cursor")
	policies, generationID, injected := st2.GetSbdState()
	if policies != "persisted policy" {
		t.Errorf("policies = %q, want %q", policies, "persisted policy")
	}
	if generationID != "gen-1" {
		t.Errorf("generationID = %q, want %q", generationID, "gen-1")
	}
	if injected {
		t.Error("injected should be false")
	}
}

func TestSbdState_IsolatedBySessions(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st1 := NewSessionState("session-a", "cursor")
	st2 := NewSessionState("session-b", "cursor")

	st1.SetSbdPolicies("policy-a", "gen-a")
	st2.SetSbdPolicies("policy-b", "gen-b")

	policies1, gen1, _ := st1.GetSbdState()
	policies2, gen2, _ := st2.GetSbdState()

	if policies1 != "policy-a" || gen1 != "gen-a" {
		t.Errorf("session-a: policies=%q gen=%q, want policy-a/gen-a", policies1, gen1)
	}
	if policies2 != "policy-b" || gen2 != "gen-b" {
		t.Errorf("session-b: policies=%q gen=%q, want policy-b/gen-b", policies2, gen2)
	}
}

func TestSbdState_DoesNotAffectOtherFields(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")

	// Set non-SbD fields first
	st.SetModel("gpt-4")
	st.AddEvaluation("eval-1", "/path/to/file.go")

	// Set SbD fields
	st.SetSbdPolicies("policy", "gen-1")

	// Verify non-SbD fields are preserved
	model := st.GetModel()
	if model != "gpt-4" {
		t.Errorf("model = %q, want %q", model, "gpt-4")
	}
	evals := st.GetPendingEvaluations()
	if len(evals) != 1 || evals[0].EvaluationID != "eval-1" {
		t.Errorf("evaluations not preserved after SetSbdPolicies")
	}
}

func TestClearSbdPolicies(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")
	st.SetSbdPolicies("policy content", "gen-1")
	st.MarkSbdInjected()

	// Verify state is set
	policies, _, injected := st.GetSbdState()
	if policies == "" || !injected {
		t.Fatal("precondition failed: state should be set")
	}

	// Clear
	st.ClearSbdPolicies()

	policies, generationID, injected := st.GetSbdState()
	if policies != "" {
		t.Errorf("policies = %q, want empty after clear", policies)
	}
	if generationID != "" {
		t.Errorf("generationID = %q, want empty after clear", generationID)
	}
	if injected {
		t.Error("injected should be false after clear")
	}
}

func TestClearSbdPolicies_NoSession(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	// Should not panic on nonexistent session
	st := NewSessionState("nonexistent", "cursor")
	st.ClearSbdPolicies()

	policies, _, _ := st.GetSbdState()
	if policies != "" {
		t.Error("should remain empty for nonexistent session")
	}
}

func TestClearSbdPolicies_PreservesOtherFields(t *testing.T) {
	_, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")
	st.SetModel("gpt-4")
	st.AddEvaluation("eval-1", "/path/to/file.go")
	st.SetSbdPolicies("policy", "gen-1")

	st.ClearSbdPolicies()

	// SbD fields cleared
	policies, _, _ := st.GetSbdState()
	if policies != "" {
		t.Error("policies should be cleared")
	}

	// Other fields preserved
	if st.GetModel() != "gpt-4" {
		t.Error("model should be preserved after ClearSbdPolicies")
	}
	evals := st.GetPendingEvaluations()
	if len(evals) != 1 {
		t.Error("evaluations should be preserved after ClearSbdPolicies")
	}
}

func TestSbdState_StateFileLocation(t *testing.T) {
	tmpDir, cleanup := setupTestStateDir(t)
	defer cleanup()

	st := NewSessionState("test-session", "cursor")
	st.SetSbdPolicies("policy", "gen-1")

	// Verify state.json was created in the expected location
	stateFile := filepath.Join(tmpDir, "state.json")
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state.json not found at expected path %s: %v", stateFile, err)
	}
}
