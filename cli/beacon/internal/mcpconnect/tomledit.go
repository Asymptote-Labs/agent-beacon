package mcpconnect

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/jsonc"
	"github.com/pelletier/go-toml/v2"
)

// Codex CLI keeps MCP servers in ~/.codex/config.toml as [mcp_servers.<name>] tables. The file
// also holds the user's model, profile and sandbox settings, so Beacon edits it through tomledit:
// it appends or removes its own table as lines of text, never re-serializes the document, and
// parses the result to prove nothing else changed.

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
	return jsonc.Quote(value)
}

// tomlBlock renders [mcp_servers.<name>] with fields in the order given. Values are strings, or
// the person's own `enabled` boolean that connect carries over.
func tomlBlock(table string, name string, fields []jsonc.Field, eol string) string {
	var b strings.Builder
	b.WriteString("[" + table + "." + tomlKey(name) + "]" + eol)
	for _, f := range fields {
		value := tomlString(fmt.Sprint(f.Value))
		if v, ok := f.Value.(bool); ok {
			value = strconv.FormatBool(v)
		}
		b.WriteString(f.Key + " = " + value + eol)
	}
	return b.String()
}

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(name string) string {
	if bareTOMLKey.MatchString(name) {
		return name
	}
	return tomlString(name)
}

func tomlTableEntry(parsed map[string]any, table, name string) (map[string]any, bool) {
	servers, ok := parsed[table].(map[string]any)
	if !ok {
		return nil, false
	}
	entry, ok := servers[name].(map[string]any)
	return entry, ok
}
