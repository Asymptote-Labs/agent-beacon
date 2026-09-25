package learning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

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

func RenderSkill(candidate asymptoteobserve.LearningCandidateV1, memory asymptoteobserve.LearningMemoryV1) string {
	slug := SkillSlug(memory)
	var b strings.Builder
	// Provenance lives under metadata, the Agent Skills spec's string map for
	// client-specific keys, so a generated skill validates and can be published
	// like any other.
	b.WriteString("---\n")
	b.WriteString("name: " + slug + "\n")
	b.WriteString("description: " + yamlQuote(skillDescription(memory)) + "\n")
	b.WriteString("metadata:\n")
	b.WriteString("  beacon_memory_id: " + yamlQuote(memory.ID) + "\n")
	b.WriteString("  beacon_candidate_id: " + yamlQuote(candidate.ID) + "\n")
	if memory.Kind != "" {
		b.WriteString("  beacon_memory_kind: " + yamlQuote(memory.Kind) + "\n")
	}
	if len(memory.Tags) > 0 {
		b.WriteString("  beacon_tags: " + yamlQuote(strings.Join(memory.Tags, ", ")) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString("# " + memory.Title + "\n\n")
	if memory.Applicability != "" {
		b.WriteString("Use this skill " + ensureTrailingPeriod(memory.Applicability) + "\n\n")
	}
	b.WriteString("## Guidance\n\n")
	b.WriteString(strings.TrimSpace(memory.Body))
	b.WriteString("\n\n")
	if len(memory.Evidence) > 0 {
		b.WriteString("## Beacon Evidence\n\n")
		for _, evidence := range memory.Evidence {
			b.WriteString("- Trace `" + evidence.TraceID + "`")
			if evidence.Summary != "" {
				b.WriteString(": " + evidence.Summary)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func InstallSkill(projectPath string, candidate asymptoteobserve.LearningCandidateV1, memory asymptoteobserve.LearningMemoryV1, force bool) (SkillInstallResult, error) {
	if strings.TrimSpace(projectPath) == "" {
		projectPath = firstNonEmpty(memory.Project.Path, candidate.Project.Path)
	}
	if strings.TrimSpace(projectPath) == "" {
		return SkillInstallResult{}, fmt.Errorf("project path is required to install a skill")
	}
	slug := SkillSlug(memory)
	path := filepath.Join(projectPath, ".agents", "skills", slug, "SKILL.md")
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
	base := strings.TrimSpace(memory.Title)
	if base == "" {
		base = memory.ID
	}
	var out strings.Builder
	lastDash := false
runes:
	for _, r := range strings.ToLower(base) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(r)
			lastDash = false
		case !lastDash:
			out.WriteByte('-')
			lastDash = true
		}
		// Stop at 48 bytes so the name, with the beacon- prefix, stays within the
		// Agent Skills limit of 64 characters.
		if out.Len() >= 48 {
			break runes
		}
	}
	slug := strings.Trim(out.String(), "-")
	if slug == "" {
		slug = "beacon-memory"
	}
	if !strings.HasPrefix(slug, "beacon-") {
		slug = "beacon-" + slug
	}
	return slug
}

// maxSkillDescription is the Agent Skills limit on the description field.
const maxSkillDescription = 1024

// skillDescription is what a harness reads to decide when to load the skill, so
// it names the situation as well as the lesson: the title's first sentence, then
// the memory's applicability as a "Use ..." clause.
func skillDescription(memory asymptoteobserve.LearningMemoryV1) string {
	description := ensureTrailingPeriod(firstSentence(memory.Title))
	if applies := strings.Join(strings.Fields(memory.Applicability), " "); applies != "" {
		description += " Use " + ensureTrailingPeriod(applies)
	}
	if runes := []rune(description); len(runes) > maxSkillDescription {
		description = strings.TrimSpace(string(runes[:maxSkillDescription-1])) + "…"
	}
	return description
}

func yamlQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func firstSentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Beacon approved project memory."
	}
	for _, sep := range []string{".", "\n"} {
		if idx := strings.Index(value, sep); idx > 0 {
			return strings.TrimSpace(value[:idx])
		}
	}
	return value
}

func ensureTrailingPeriod(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, ".") {
		return value
	}
	return value + "."
}
