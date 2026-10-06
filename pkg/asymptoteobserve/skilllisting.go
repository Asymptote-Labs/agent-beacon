package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SkillListing is a skill index a runtime showed the model, split into one entry per skill.
//
// Every capture path builds its session.context event from one, so a skill index is recorded the
// same way whichever runtime or collection method saw it: one gen_ai.system_instructions text part
// per entry, and a system_context block naming each skill. Supporting another runtime means
// finding where it exposes its index and handing the text to ParseSkillListing; nothing about the
// event is runtime-specific.
//
// Each entry gets its own part because a writer limits every string it stores, not the event. One
// part holding a 6 KB index of fifteen skills keeps the first four; split, every skill keeps its
// description up to the limit.
type SkillListing struct {
	// Source is where the runtime exposed the index: a SystemContextSource value.
	Source  string
	Entries []SkillListingEntry
}

// SkillListingEntry is one skill as the model was shown it. Name is empty for text the listing
// carries outside any skill's entry, which is kept so nothing the model saw is dropped.
type SkillListingEntry struct {
	Name string
	Text string
}

// ParseSkillListing splits a rendered skill index into entries. Both runtimes Beacon reads one from,
// Claude Code's skill_listing attachment and Oh My Pi's <skills> block, render a skill as a line
// "- <name>: <description>", and a description can run onto the lines after it.
//
// names, when the runtime reports them, are the skills the listing holds, and only a line naming one
// of them starts an entry, so a description cannot forge another skill's entry with a line that
// looks like one. A name can contain a colon (Claude Code's plugin skills are plugin:skill), so a
// line is matched against the longest name it begins with. Without names, an entry starts at any
// "- " line whose name, the text before ": ", has no spaces.
func ParseSkillListing(source, text string, names []string) SkillListing {
	listing := SkillListing{Source: source}
	remaining := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			remaining[name] = true
		}
	}
	var name string
	var lines []string
	flush := func() {
		if entry := strings.TrimSpace(strings.Join(lines, "\n")); entry != "" {
			listing.Entries = append(listing.Entries, SkillListingEntry{Name: name, Text: entry})
		}
		lines = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if next, ok := skillEntryName(line, len(names) > 0, remaining); ok {
			flush()
			name = next
			delete(remaining, next)
		}
		lines = append(lines, line)
	}
	flush()
	return listing
}

func skillEntryName(line string, named bool, remaining map[string]bool) (string, bool) {
	rest, ok := strings.CutPrefix(line, "- ")
	if !ok {
		return "", false
	}
	if named {
		var longest string
		for name := range remaining {
			if strings.HasPrefix(rest, name+":") && len(name) > len(longest) {
				longest = name
			}
		}
		return longest, longest != ""
	}
	name, _, found := strings.Cut(rest, ": ")
	if !found {
		name, found = strings.CutSuffix(rest, ":")
	}
	if !found || name == "" || strings.ContainsAny(name, " \t") {
		return "", false
	}
	return name, true
}

// SkillNameHash is the hash a skill's name is recorded under, here and in the configuration
// inventory, so a skill the model was shown and a SKILL.md found on disk join on it.
func SkillNameHash(name string) string {
	if name == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(name))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Text is the listing as the model was shown it: the entries, one after another.
func (l SkillListing) Text() string {
	texts := make([]string, len(l.Entries))
	for i, entry := range l.Entries {
		texts[i] = entry.Text
	}
	return strings.Join(texts, "\n")
}

// SystemInstructions is the listing in the gen_ai.system_instructions shape: one text part per entry.
func (l SkillListing) SystemInstructions() []interface{} {
	parts := make([]interface{}, len(l.Entries))
	for i, entry := range l.Entries {
		parts[i] = map[string]interface{}{"type": GenAIPartTypeText, "content": entry.Text}
	}
	return parts
}

// SystemContext names the listing's skills.
func (l SkillListing) SystemContext() *SystemContextInfo {
	info := &SystemContextInfo{Kind: SystemContextSkillListing, Source: l.Source}
	for _, entry := range l.Entries {
		if entry.Name != "" {
			info.Skills = append(info.Skills, SkillRefInfo{SkillName: entry.Name, SkillNameHash: SkillNameHash(entry.Name)})
		}
	}
	return info
}

// Content is the listing's content marker. Hash and Bytes cover the whole listing. storeLimit is
// the limit the writer applies to each string it stores, and Truncated says whether any one entry
// is longer than that, because each entry is stored as its own string.
func (l SkillListing) Content(storeLimit int) *ContentInfo {
	info := RetainedContent(l.Text(), 0)
	if info == nil {
		return nil
	}
	for _, entry := range l.Entries {
		if storeLimit > 0 && len(entry.Text) > storeLimit {
			info.Truncated = true
		}
	}
	return info
}

// Apply records the listing on ev, keeping what ev.GenAI already holds, such as a subagent's
// agent block.
func (l SkillListing) Apply(ev *Event, storeLimit int) {
	if len(l.Entries) == 0 {
		return
	}
	if ev.GenAI == nil {
		ev.GenAI = &GenAIInfo{}
	}
	ev.GenAI.SystemInstructions = l.SystemInstructions()
	ev.SystemContext = l.SystemContext()
	ev.Content = l.Content(storeLimit)
}

// Fields is Apply in the map form the hook adapter assembles events in: the gen_ai,
// system_context and content values to merge into an event's fields. The two forms serialize
// identically, which TestSkillListingFieldsMatchTheTypedEvent holds them to.
func (l SkillListing) Fields(storeLimit int) map[string]interface{} {
	if len(l.Entries) == 0 {
		return nil
	}
	context := l.SystemContext()
	contextFields := map[string]interface{}{"kind": context.Kind}
	if context.Source != "" {
		contextFields["source"] = context.Source
	}
	if len(context.Skills) > 0 {
		skills := make([]interface{}, len(context.Skills))
		for i, skill := range context.Skills {
			skills[i] = map[string]interface{}{"skill_name": skill.SkillName, "skill_name_hash": skill.SkillNameHash}
		}
		contextFields["skills"] = skills
	}
	return map[string]interface{}{
		"gen_ai":         map[string]interface{}{"system_instructions": l.SystemInstructions()},
		"system_context": contextFields,
		"content":        contentFields(l.Content(storeLimit)),
	}
}
