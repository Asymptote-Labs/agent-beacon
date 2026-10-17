package learning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type SkillInstallResult struct {
	Path    string `json:"path"`
	Slug    string `json:"slug"`
	Written bool   `json:"written"`
	Content string `json:"content,omitempty"`
}

func CandidateMemoryForSkill(store *Store, candidateID string) (asymptoteobserve.LearningCandidateV1, asymptoteobserve.LearningMemoryV1, error) {
	candidate, ok, err := store.GetCandidate(candidateID)
	if err != nil {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, err
	}
	if !ok {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, fmt.Errorf("candidate not found: %s", candidateID)
	}
	if candidate.State != asymptoteobserve.LearningCandidateStateApproved || candidate.MemoryID == "" {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, fmt.Errorf("candidate %s is not approved", candidateID)
	}
	memory, ok, err := store.GetMemory(candidate.MemoryID)
	if err != nil {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, err
	}
	if !ok || memory.SupersededBy != "" {
		return asymptoteobserve.LearningCandidateV1{}, asymptoteobserve.LearningMemoryV1{}, fmt.Errorf("approved memory not found: %s", candidate.MemoryID)
	}
	return candidate, memory, nil
}

// RenderSkill renders an approved memory as a SKILL.md. The renderer lives in
// pkg/asymptoteobserve so Beacon Cloud MCP produces the same file.
func RenderSkill(candidate asymptoteobserve.LearningCandidateV1, memory asymptoteobserve.LearningMemoryV1) string {
	return asymptoteobserve.RenderLearningSkill(candidate, memory)
}

func InstallSkill(projectPath string, candidate asymptoteobserve.LearningCandidateV1, memory asymptoteobserve.LearningMemoryV1, force bool) (SkillInstallResult, error) {
	if strings.TrimSpace(projectPath) == "" {
		projectPath = firstNonEmpty(memory.Project.Path, candidate.Project.Path)
	}
	if strings.TrimSpace(projectPath) == "" {
		return SkillInstallResult{}, fmt.Errorf("project path is required to install a skill")
	}
	slug := SkillSlug(memory)
	path := filepath.Join(projectPath, filepath.FromSlash(asymptoteobserve.LearningSkillPath(slug)))
	if !force {
		if _, err := os.Stat(path); err == nil {
			return SkillInstallResult{}, fmt.Errorf("skill already exists: %s (use --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return SkillInstallResult{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return SkillInstallResult{}, err
	}
	content := RenderSkill(candidate, memory)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return SkillInstallResult{}, err
	}
	return SkillInstallResult{Path: path, Slug: slug, Written: true}, nil
}

func SkillSlug(memory asymptoteobserve.LearningMemoryV1) string {
	return asymptoteobserve.LearningSkillSlug(memory)
}
