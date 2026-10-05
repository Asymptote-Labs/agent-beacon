package mcpconnect

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Codex CLI keeps MCP servers in ~/.codex/config.toml as [mcp_servers.<name>] tables. The file
// also holds the user's model, profile and sandbox settings, so Beacon edits it the way the Kimi
// Code installer edits config.toml: it appends or removes its own table as lines of text, never
// re-serializes the document, and parses the result to prove nothing else changed.

func parseTOML(text string) (map[string]any, error) {
	parsed := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return parsed, nil
	}
	if err := toml.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// tomlString renders a TOML basic string, double-quoted as Codex's own `codex mcp add` writes it.
// JSON's string escapes (\" \\ \n \t \uXXXX, and nothing else) are all valid in a TOML basic
// string, and the parse-back check in verifyConnect confirms the result.
func tomlString(value string) string {
	return jsonString(value)
}

// tomlLineEnding is the line ending the document already uses. A new document gets "\n" on every
// platform, as `codex mcp add` writes it.
func tomlLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// tomlBlock renders [mcp_servers.<name>] with fields in the order given.
func tomlBlock(table string, name string, fields []field, eol string) string {
	var b strings.Builder
	b.WriteString("[" + table + "." + tomlKey(name) + "]" + eol)
	for _, f := range fields {
		b.WriteString(f.Key + " = " + tomlValue(f.Value) + eol)
	}
	return b.String()
}

// tomlValue renders a string, or an array of strings, the way `codex mcp add` writes them.
func tomlValue(value any) string {
	if list, ok := value.([]any); ok {
		parts := make([]string, len(list))
		for i, v := range list {
			parts[i] = tomlString(fmt.Sprint(v))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return tomlString(fmt.Sprint(value))
}

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(name string) string {
	if bareTOMLKey.MatchString(name) {
		return name
	}
	return tomlString(name)
}

// tomlHeaderFor matches the header of table.name and of its subtables (for example
// [mcp_servers.beacon-managed.env]), in any of TOML's quoting styles.
func tomlHeaderFor(table, name string) *regexp.Regexp {
	key := `(?:` + regexp.QuoteMeta(name) + `|"` + regexp.QuoteMeta(name) + `"|'` + regexp.QuoteMeta(name) + `')`
	return regexp.MustCompile(`^\s*\[\s*` + regexp.QuoteMeta(table) + `\s*\.\s*` + key + `\s*(?:\.[^\]]*)?\]\s*(?:#.*)?$`)
}

var tomlAnyHeader = regexp.MustCompile(`^\s*\[`)

// stripTOMLTable removes every [table.name] block (and its subtables) from text. A block runs
// from its header to the next header, so its own trailing blank lines go with it; a block at the
// end of the document also takes the blank lines above it, which is what appendTOMLBlock added.
// It reports whether any block was found.
func stripTOMLTable(text, table, name string) (string, bool) {
	header := tomlHeaderFor(table, name)
	lines := strings.SplitAfter(text, "\n")
	var out []string
	found, skipping := false, false
	for _, line := range lines {
		bare := strings.TrimRight(line, "\r\n")
		if tomlAnyHeader.MatchString(bare) {
			skipping = header.MatchString(bare)
			found = found || skipping
		}
		if !skipping {
			out = append(out, line)
		}
	}
	if skipping {
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
	}
	return strings.Join(out, ""), found
}

// appendTOMLBlock adds block at the end of the document, one blank line after existing content.
func appendTOMLBlock(text, block, eol string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return block
	}
	return trimmed + eol + eol + block
}

// endTOMLWithNewline gives a non-empty document exactly one trailing line ending, so a document
// whose last table was Beacon's does not lose its final newline when the table is removed.
func endTOMLWithNewline(text, eol string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	return trimmed + eol
}

func tomlTableEntry(parsed map[string]any, table, name string) (map[string]any, bool) {
	servers, ok := parsed[table].(map[string]any)
	if !ok {
		return nil, false
	}
	entry, ok := servers[name].(map[string]any)
	return entry, ok
}
