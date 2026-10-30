// Package tomledit edits TOML documents Beacon does not own -- Codex's and Kimi Code's
// config.toml -- as lines of text. Those files carry the user's comments and layout, which a
// parse-and-serialize round trip would discard, so Beacon only ever removes its own tables and
// appends new ones; callers parse the result to prove nothing else changed.
//
// It does not track multi-line strings: a line inside a """ value that looks like a header is
// read as one.
package tomledit

import (
	"strconv"
	"strings"
)

// Header parses a table header (`[a.b]`) or array-of-tables header (`[[a.b]]`) line, with
// whitespace around the key parts, bare, "basic" or 'literal' key parts, and an optional trailing
// comment. It returns the key's parts with quoting removed.
func Header(line string) (key []string, array bool, ok bool) {
	rest := strings.TrimSpace(line)
	if !strings.HasPrefix(rest, "[") {
		return nil, false, false
	}
	rest = rest[1:]
	if strings.HasPrefix(rest, "[") {
		array = true
		rest = rest[1:]
	}
	for {
		var part string
		part, rest, ok = keyPart(strings.TrimLeft(rest, " \t"))
		if !ok {
			return nil, false, false
		}
		key = append(key, part)
		rest = strings.TrimLeft(rest, " \t")
		if !strings.HasPrefix(rest, ".") {
			break
		}
		rest = rest[1:]
	}
	closing := "]"
	if array {
		closing = "]]"
	}
	if !strings.HasPrefix(rest, closing) {
		return nil, false, false
	}
	rest = strings.TrimLeft(rest[len(closing):], " \t")
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return nil, false, false
	}
	return key, array, true
}

// keyPart reads one bare, basic-quoted or literal-quoted key part from the start of s.
func keyPart(s string) (part, rest string, ok bool) {
	switch {
	case strings.HasPrefix(s, `"`):
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '"':
				part, err := strconv.Unquote(s[:i+1])
				return part, s[i+1:], err == nil
			}
		}
		return "", "", false
	case strings.HasPrefix(s, "'"):
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", "", false
		}
		return s[1 : end+1], s[end+2:], true
	}
	end := 0
	for end < len(s) && isBareKeyChar(s[end]) {
		end++
	}
	return s[:end], s[end:], end > 0
}

func isBareKeyChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// RemoveTable removes the table at key and every table under it (for key ["otel"]: [otel],
// [otel.exporter."otlp-grpc"], [[otel.x]]) from text. A block runs from its header to the next
// header, so its own trailing blank lines go with it; a block at the end of the document also
// takes the blank lines above it, which is what Append added. It reports whether any block was
// found.
func RemoveTable(text string, key ...string) (string, bool) {
	var out []string
	found, skipping := false, false
	for _, line := range strings.SplitAfter(text, "\n") {
		if header, _, ok := Header(line); ok {
			skipping = hasPrefix(header, key)
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

func hasPrefix(key, prefix []string) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i := range prefix {
		if key[i] != prefix[i] {
			return false
		}
	}
	return true
}

// Append adds block at the end of the document, one blank line after existing content.
func Append(text, block, eol string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return block
	}
	return trimmed + eol + eol + block
}

// EndWithNewline gives a non-empty document exactly one trailing line ending, so a document whose
// last table was Beacon's does not lose its final newline when the table is removed.
func EndWithNewline(text, eol string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	return trimmed + eol
}

// LineEnding is the line ending the document already uses, so an edit does not mix them; a
// document without one gets fallback.
func LineEnding(text, fallback string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\n") {
		return "\n"
	}
	return fallback
}
