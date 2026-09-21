package mcpserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestApprovedMemoryFromOneHarnessIsRetrievableForAnother(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "endpoint", "logs", "runtime.jsonl")
	store := learning.Open(learning.PathForRuntimeLog(logPath))
	project := asymptoteobserve.LearningProjectV1{ID: "project-1"}
	memory := asymptoteobserve.LearningMemoryV1{
		ID:          "memory-cross-harness",
		CandidateID: "candidate-claude",
		Kind:        asymptoteobserve.LearningMemoryKindDebuggingPattern,
		Title:       "Claude Code trace: retry package smoke after transient network failure",
		Body:        "When package smoke fails with ECONNRESET, inspect the error and rerun once before changing code.",
		Project:     project,
		Evidence: []asymptoteobserve.LearningEvidenceV1{
			{TraceID: "session:claude_code:s1", Summary: "Claude Code solved the package smoke retry workflow"},
		},
	}
	if err := store.PutMemory(memory); err != nil {
		t.Fatal(err)
	}
	server := New(Options{LogPath: logPath})
	result, err := server.callTool(t.Context(), callToolParams{
		Name: "get_memory_context",
		Arguments: map[string]interface{}{
			"project_id": project.ID,
			"task":       "package smoke ECONNRESET retry",
		},
	})
	if err != nil {
		t.Fatalf("get_memory_context returned error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "memory-cross-harness") || !strings.Contains(text, "Claude Code") {
		t.Fatalf("cross-harness memory not returned: %s", text)
	}
}
