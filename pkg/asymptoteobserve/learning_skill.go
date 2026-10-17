package asymptoteobserve

import (
	"strings"
	"unicode"
)

// LearningSkillPath is where a rendered memory skill goes, relative to the
// project root: the cross-harness .agents/skills directory most skill-capable
// runtimes read (Claude Code reads .claude/skills instead).
func LearningSkillPath(slug string) string {
	return ".agents/skills/" + slug + "/SKILL.md"
}

// RenderLearningSkill renders an approved memory as an Agent Skills SKILL.md.
// The CLI's `beacon memory skills` and the hosted Beacon Cloud MCP both call it,
// so a skill promoted from either is byte-for-byte the same file.
func RenderLearningSkill(candidate LearningCandidateV1, memory LearningMemoryV1) string {
	slug := LearningSkillSlug(memory)
	var b strings.Builder
	// Provenance lives under metadata, the Agent Skills spec's string map for
	// client-specific keys, so a generated skill validates and can be published
	// like any other.
	b.WriteString("---\n")
	b.WriteString("name: " + slug + "\n")
	b.WriteString("description: " + skillYAMLQuote(learningSkillDescription(memory)) + "\n")
	b.WriteString("metadata:\n")
	b.WriteString("  beacon_memory_id: " + skillYAMLQuote(memory.ID) + "\n")
	b.WriteString("  beacon_candidate_id: " + skillYAMLQuote(candidate.ID) + "\n")
	if memory.Kind != "" {
		b.WriteString("  beacon_memory_kind: " + skillYAMLQuote(memory.Kind) + "\n")
	}
	if len(memory.Tags) > 0 {
		b.WriteString("  beacon_tags: " + skillYAMLQuote(strings.Join(memory.Tags, ", ")) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString("# " + memory.Title + "\n\n")
	if memory.Applicability != "" {
		b.WriteString("Use this skill " + skillTrailingPeriod(memory.Applicability) + "\n\n")
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

// LearningSkillSlug is the skill's name and directory: the memory title in
// lowercase words joined by dashes, prefixed with beacon-.
func LearningSkillSlug(memory LearningMemoryV1) string {
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

// maxLearningSkillDescription is the Agent Skills limit on the description field.
const maxLearningSkillDescription = 1024

// learningSkillDescription is what a harness reads to decide when to load the
// skill, so it names the situation as well as the lesson: the title's first
// sentence, then the memory's applicability as a "Use ..." clause.
func learningSkillDescription(memory LearningMemoryV1) string {
	description := skillTrailingPeriod(skillFirstSentence(memory.Title))
	if applies := strings.Join(strings.Fields(memory.Applicability), " "); applies != "" {
		description += " Use " + skillTrailingPeriod(applies)
	}
	if runes := []rune(description); len(runes) > maxLearningSkillDescription {
		description = strings.TrimSpace(string(runes[:maxLearningSkillDescription-1])) + "…"
	}
	return description
}

func skillYAMLQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func skillFirstSentence(value string) string {
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

func skillTrailingPeriod(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, ".") {
		return value
	}
	return value + "."
}
