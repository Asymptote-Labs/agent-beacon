package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The publishable skills in agent-skills/ tell agents in other harnesses which
// beacon commands to run. They ship through marketplaces on their own schedule,
// so a renamed command or flag would break them silently. These tests keep them
// honest against the real command tree and the Agent Skills spec.

const agentSkillsDir = "../../../agent-skills"

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func agentSkillFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(agentSkillsDir, "skills", "*", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no skills found under %s", agentSkillsDir)
	}
	return files
}

func TestAgentSkillsFrontmatterFollowsSpec(t *testing.T) {
	for _, path := range agentSkillFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := normalizeNewlines(data)
		if !strings.HasPrefix(text, "---\n") {
			t.Fatalf("%s: missing frontmatter", path)
		}
		frontmatter := strings.SplitN(text[4:], "\n---\n", 2)[0]
		fields := map[string]string{}
		for _, line := range strings.Split(frontmatter, "\n") {
			if line == "" || strings.HasPrefix(line, " ") {
				continue
			}
			key, value, _ := strings.Cut(line, ":")
			fields[key] = strings.TrimSpace(value)
		}
		for key := range fields {
			switch key {
			case "name", "description", "license", "compatibility", "metadata", "allowed-tools":
			default:
				t.Errorf("%s: frontmatter key %q is not in the Agent Skills spec", path, key)
			}
		}
		dir := filepath.Base(filepath.Dir(path))
		if name := fields["name"]; name != dir || len(name) > 64 || !skillNamePattern.MatchString(name) {
			t.Errorf("%s: name %q must match its directory %q and the spec pattern", path, name, dir)
		}
		if n := len([]rune(fields["description"])); n == 0 || n > 1024 {
			t.Errorf("%s: description is %d characters, want 1-1024", path, n)
		}
		if n := len([]rune(fields["compatibility"])); n > 500 {
			t.Errorf("%s: compatibility is %d characters, want at most 500", path, n)
		}
		if lines := strings.Count(text, "\n"); lines > 500 {
			t.Errorf("%s: %d lines; keep SKILL.md under 500 and move detail to references/", path, lines)
		}
		for _, link := range regexp.MustCompile(`\]\((references/[^)]+)\)`).FindAllStringSubmatch(text, -1) {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), link[1])); err != nil {
				t.Errorf("%s: linked file %s is missing", path, link[1])
			}
		}
	}
}

func TestAgentSkillsReferenceRealCommandsAndFlags(t *testing.T) {
	checked := 0
	for _, path := range append(agentSkillFiles(t), mustGlob(t, filepath.Join(agentSkillsDir, "skills", "*", "references", "*.md"))...) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range beaconCommandsInMarkdown(normalizeNewlines(data)) {
			checkBeaconCommand(t, path, command)
			checked++
		}
	}
	if checked < 10 {
		t.Fatalf("only %d beacon commands found in the skills; the extractor is probably broken", checked)
	}
}

func TestAgentSkillsManifestsAgree(t *testing.T) {
	type manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	read := func(rel string) manifest {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(agentSkillsDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return m
	}
	want := read("plugin.json")
	for _, rel := range []string{".claude-plugin/plugin.json", ".cursor-plugin/plugin.json", "gemini-extension.json", "kimi.plugin.json"} {
		if got := read(rel); got != want {
			t.Errorf("%s = %+v, want %+v (same as plugin.json)", rel, got, want)
		}
	}
	// Claude Code's marketplace is also read by Codex, Copilot, Droid, Grok, Qwen,
	// Oh My Pi, and OpenClaw. Cursor reads only its own, which names the source
	// without "./" and takes the version from the plugin manifest.
	for _, market := range []struct {
		rel, source    string
		requireVersion bool
	}{
		{".claude-plugin/marketplace.json", "./agent-skills", true},
		{".cursor-plugin/marketplace.json", "agent-skills", false},
	} {
		data, err := os.ReadFile(filepath.Join(agentSkillsDir, "..", market.rel))
		if err != nil {
			t.Fatal(err)
		}
		var marketplace struct {
			Plugins []struct {
				manifest
				Source string `json:"source"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal(data, &marketplace); err != nil {
			t.Fatalf("%s: %v", market.rel, err)
		}
		found := false
		for _, p := range marketplace.Plugins {
			if p.Source != market.source {
				continue
			}
			found = true
			if p.Name != want.Name || (market.requireVersion && p.Version != want.Version) || (!market.requireVersion && p.Version != "" && p.Version != want.Version) {
				t.Errorf("%s entry = %+v, want %+v", market.rel, p.manifest, want)
			}
		}
		if !found {
			t.Errorf("%s has no entry for %s", market.rel, market.source)
		}
	}
	// Pi and Prime Agent install the whole repository, so the root package.json
	// points them at the skills directory.
	data, err := os.ReadFile(filepath.Join(agentSkillsDir, "..", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Pi struct {
			Skills []string `json:"skills"`
		} `json:"pi"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	if len(pkg.Pi.Skills) != 1 || filepath.Clean(pkg.Pi.Skills[0]) != filepath.Join("agent-skills", "skills") {
		t.Errorf("package.json pi.skills = %v, want [./agent-skills/skills]", pkg.Pi.Skills)
	}
	for _, path := range agentSkillFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(normalizeNewlines(data), `version: "`+want.Version+`"`) {
			t.Errorf("%s: metadata version should be %q like plugin.json", path, want.Version)
		}
	}
}

// normalizeNewlines undoes a CRLF checkout, which Windows runners may use.
func normalizeNewlines(data []byte) string {
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// beaconCommandsInMarkdown returns every beacon invocation in fenced code blocks
// and inline code spans, with line continuations joined and heredocs dropped.
func beaconCommandsInMarkdown(text string) []string {
	var out []string
	inFence := false
	var pending string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			pending = ""
			continue
		}
		if inFence {
			if pending != "" {
				trimmed = pending + " " + trimmed
				pending = ""
			}
			if strings.HasSuffix(trimmed, `\`) {
				pending = strings.TrimSuffix(trimmed, `\`)
				continue
			}
			if strings.HasPrefix(trimmed, "beacon ") {
				out = append(out, trimmed)
			}
			continue
		}
		for _, span := range regexp.MustCompile("`(beacon [^`]+)`").FindAllStringSubmatch(line, -1) {
			out = append(out, span[1])
		}
	}
	return out
}

func checkBeaconCommand(t *testing.T, path, command string) {
	t.Helper()
	if i := strings.Index(command, "<<"); i >= 0 {
		command = command[:i]
	}
	tokens := shellFields(command)[1:]
	var words []string
	for _, token := range tokens {
		if strings.HasPrefix(token, "-") {
			break
		}
		words = append(words, token)
	}
	cmd, rest, err := rootCmd.Find(words)
	if err != nil || cmd == rootCmd {
		t.Errorf("%s: %q does not name a beacon command", path, command)
		return
	}
	if cmd.HasSubCommands() && len(rest) > 0 && !strings.HasPrefix(rest[0], "<") && !strings.HasPrefix(rest[0], `"`) {
		t.Errorf("%s: %q: %q is not a subcommand of %q", path, command, rest[0], cmd.CommandPath())
	}
	for _, token := range tokens {
		if !strings.HasPrefix(token, "-") || token == "-" {
			continue
		}
		if !hasFlag(cmd, token) {
			t.Errorf("%s: %q: %s has no flag %s", path, command, cmd.CommandPath(), token)
		}
	}
}

func hasFlag(cmd *cobra.Command, token string) bool {
	name, _, _ := strings.Cut(token, "=")
	flags := cmd.Flags()
	if strings.HasPrefix(name, "--") {
		return flags.Lookup(name[2:]) != nil || cmd.InheritedFlags().Lookup(name[2:]) != nil || name == "--help"
	}
	short := strings.TrimPrefix(name, "-")
	return len(short) == 1 && (flags.ShorthandLookup(short) != nil || cmd.InheritedFlags().ShorthandLookup(short) != nil)
}

// shellFields splits on spaces outside single or double quotes. It is enough for
// the documented examples, which use no escapes or substitutions.
func shellFields(s string) []string {
	var fields []string
	var current strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			current.WriteRune(r)
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields
}
