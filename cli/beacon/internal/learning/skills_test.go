package learning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestRenderSkillIncludesProvenance(t *testing.T) {
	candidate, memory := testApprovedSkillMemory()
	content := RenderSkill(candidate, memory)
	for _, want := range []string{
		`name: beacon-debugging-pattern-retry-package-smoke`,
		"metadata:\n  beacon_memory_id: \"memory-1\"\n  beacon_candidate_id: \"candidate-1\"",
		`beacon_tags: "beacon,debugging_pattern"`,
		"# Debugging pattern: Retry package smoke",
		"Trace `trace-1`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered skill missing %q:\n%s", want, content)
		}
	}
}

func TestInstallSkillWritesAndRefusesOverwrite(t *testing.T) {
	project := t.TempDir()
	candidate, memory := testApprovedSkillMemory()
	result, err := InstallSkill(project, candidate, memory, false)
	if err != nil {
		t.Fatalf("InstallSkill returned error: %v", err)
	}
	if !result.Written || result.Path == "" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Retry package smoke") {
		t.Fatalf("skill content = %s", string(data))
	}
	if _, err := InstallSkill(project, candidate, memory, false); err == nil {
		t.Fatal("InstallSkill should refuse overwrite without force")
	}
	if _, err := InstallSkill(project, candidate, memory, true); err != nil {
		t.Fatalf("InstallSkill force returned error: %v", err)
	}
	if got, want := result.Path, filepath.Join(project, ".agents", "skills", "beacon-debugging-pattern-retry-package-smoke", "SKILL.md"); got != want {
		t.Fatalf("path = %s, want %s", got, want)
	}
}

func TestCandidateMemoryForSkillRequiresApprovedCandidate(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate, memory := testApprovedSkillMemory()
	candidate.State = asymptoteobserve.LearningCandidateStateCandidate
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CandidateMemoryForSkill(store, candidate.ID); err == nil {
		t.Fatal("candidate memory lookup should require approval")
	}
	candidate.State = asymptoteobserve.LearningCandidateStateApproved
	candidate.MemoryID = memory.ID
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemory(memory); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CandidateMemoryForSkill(store, candidate.ID); err != nil {
		t.Fatalf("CandidateMemoryForSkill returned error: %v", err)
	}
}

func testApprovedSkillMemory() (asymptoteobserve.LearningCandidateV1, asymptoteobserve.LearningMemoryV1) {
	project := asymptoteobserve.LearningProjectV1{ID: "project-1", Path: "/repo"}
	evidence := []asymptoteobserve.LearningEvidenceV1{{TraceID: "trace-1", Summary: "Retry fixed a transient package smoke failure"}}
	candidate := asymptoteobserve.LearningCandidateV1{
		ID:       "candidate-1",
		MemoryID: "memory-1",
		State:    asymptoteobserve.LearningCandidateStateApproved,
		Kind:     asymptoteobserve.LearningMemoryKindDebuggingPattern,
		Title:    "Debugging pattern: Retry package smoke",
		Body:     "When package smoke fails with a transient network error, inspect the error and rerun once before changing code.",
		Project:  project,
		Evidence: evidence,
	}
	memory := asymptoteobserve.LearningMemoryV1{
		ID:            "memory-1",
		CandidateID:   candidate.ID,
		Kind:          candidate.Kind,
		Title:         candidate.Title,
		Body:          candidate.Body,
		Applicability: "when package smoke fails with a transient network error",
		Tags:          []string{"beacon", "debugging_pattern"},
		Project:       project,
		Evidence:      evidence,
	}
	return candidate, memory
}
