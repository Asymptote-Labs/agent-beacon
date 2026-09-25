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
		`beacon_memory_id: "memory-1"`,
		`beacon_candidate_id: "candidate-1"`,
		"# Debugging pattern: Retry package smoke",
		"Trace `trace-1`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered skill missing %q:\n%s", want, content)
		}
	}
}

// A generated skill must pass the Agent Skills spec so it can be published: the
// name fits in 64 characters, and provenance sits under metadata rather than as
// top-level keys the spec does not define.
func TestRenderSkillFollowsAgentSkillsSpec(t *testing.T) {
	candidate, memory := testApprovedSkillMemory()
	memory.Title = strings.Repeat("Always rebuild the embedded hooks binary before running tests ", 3)
	memory.Applicability = "when cli/beacon tests fail\nagainst the placeholder"
	content := RenderSkill(candidate, memory)
	frontmatter := strings.SplitN(strings.TrimPrefix(content, "---\n"), "\n---\n", 2)[0]
	var name, description string
	for _, line := range strings.Split(frontmatter, "\n") {
		if !strings.HasPrefix(line, " ") && strings.Contains(line, ":") {
			key := strings.SplitN(line, ":", 2)[0]
			switch key {
			case "name":
				name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			case "description":
				description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			case "metadata":
			default:
				t.Fatalf("top-level frontmatter key %q is not in the Agent Skills spec:\n%s", key, frontmatter)
			}
		}
	}
	if len(name) == 0 || len(name) > 64 || strings.Contains(name, "--") || strings.HasSuffix(name, "-") {
		t.Fatalf("name %q does not meet the spec", name)
	}
	if name != SkillSlug(memory) {
		t.Fatalf("name %q != slug %q", name, SkillSlug(memory))
	}
	if !strings.Contains(description, "Use when cli/beacon tests fail against the placeholder.") {
		t.Fatalf("description should carry the applicability on one line: %s", description)
	}
	for _, want := range []string{`  beacon_memory_kind: "debugging_pattern"`, `  beacon_tags: "beacon, debugging_pattern"`} {
		if !strings.Contains(frontmatter, want) {
			t.Fatalf("frontmatter missing %q:\n%s", want, frontmatter)
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
