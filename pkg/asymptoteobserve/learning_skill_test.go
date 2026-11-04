package asymptoteobserve

import (
	"strings"
	"testing"
)

// The CLI and Beacon Cloud MCP both render promoted memory with
// RenderLearningSkill, and a skill a team committed from one must not churn when
// re-rendered by the other, so the exact output is pinned here.
func TestRenderLearningSkillGolden(t *testing.T) {
	evidence := []LearningEvidenceV1{{TraceID: "trace-1", Summary: "Retry fixed a transient package smoke failure"}, {TraceID: "trace-2"}}
	candidate := LearningCandidateV1{ID: "candidate-1", Evidence: evidence}
	memory := LearningMemoryV1{
		ID:            "memory-1",
		CandidateID:   candidate.ID,
		Kind:          LearningMemoryKindDebuggingPattern,
		Title:         `Retry "package smoke" once. Then debug`,
		Body:          "\nInspect the error and rerun once before changing code.\n\n",
		Applicability: "when package smoke fails\nwith a transient network error",
		Tags:          []string{"beacon", "debugging_pattern"},
		Evidence:      evidence,
	}
	want := `---
name: beacon-retry-package-smoke-once-then-debug
description: "Retry \"package smoke\" once. Use when package smoke fails with a transient network error."
metadata:
  beacon_memory_id: "memory-1"
  beacon_candidate_id: "candidate-1"
  beacon_memory_kind: "debugging_pattern"
  beacon_tags: "beacon, debugging_pattern"
---

# Retry "package smoke" once. Then debug

Use this skill when package smoke fails
with a transient network error.

## Guidance

Inspect the error and rerun once before changing code.

## Beacon Evidence

- Trace ` + "`trace-1`" + `: Retry fixed a transient package smoke failure
- Trace ` + "`trace-2`" + `
`
	if got := RenderLearningSkill(candidate, memory); got != want {
		t.Fatalf("rendered skill changed:\n%s\nwant:\n%s", got, want)
	}
	if got := LearningSkillPath(LearningSkillSlug(memory)); got != ".agents/skills/beacon-retry-package-smoke-once-then-debug/SKILL.md" {
		t.Fatalf("path = %s", got)
	}
}

func TestLearningSkillSlugBounds(t *testing.T) {
	for title, want := range map[string]string{
		"":                        "beacon-memory-7",
		"!!!":                     "beacon-memory",
		"Beacon: hooks first":     "beacon-hooks-first",
		"Ünïcode títle":           "beacon-ünïcode-títle",
		strings.Repeat("ab ", 40): "beacon-" + strings.TrimSuffix(strings.Repeat("ab-", 16), "-"),
	} {
		got := LearningSkillSlug(LearningMemoryV1{ID: "memory-7", Title: title})
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", title, got, want)
		}
		if len(got) > 64 {
			t.Errorf("slug(%q) is %d bytes, over the Agent Skills limit", title, len(got))
		}
	}
	long := LearningMemoryV1{Title: "t", Applicability: strings.Repeat("x", 2000)}
	if got := learningSkillDescription(long); len([]rune(got)) != maxLearningSkillDescription {
		t.Fatalf("description is %d runes, want the %d cap", len([]rune(got)), maxLearningSkillDescription)
	}
}
