package inventory

import (
	"regexp"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// DefaultMaxContentBytes caps how much redacted text is retained per captured
// file when content capture is requested. Files larger than the cap are
// truncated to a UTF-8 rune boundary and flagged as truncated.
const DefaultMaxContentBytes = 64 * 1024

// redactedPlaceholder is what asymptoteobserve.RedactString puts in place of a secret, so a value
// redacted for its key and one redacted for its shape read the same.
const redactedPlaceholder = "[REDACTED]"

// secretKeyCandidate is a key that may be named for a secret: every word
// asymptoteobserve.IsSecretKeyName takes for a credential's holds one of these, and that predicate
// decides.
const secretKeyCandidate = `"?[A-Za-z0-9_.\-]*(?:token|secret|pass|key|auth|credential|cookie)[A-Za-z0-9_.\-]*"?`

// secretAssignmentRE matches a `key: value` or `key = value` assignment to a
// secretKeyCandidate, across JSON, TOML, YAML, and shell-style content. Config
// files name secrets more loosely than the labels asymptoteobserve.RedactString
// knows (`OPENAI_KEY`, `X_AUTH`, `SECRET_KEY_BASE`), so these values are
// redacted for their key alone. The value is captured separately so it can be
// replaced while preserving the surrounding key, separator, and quote style; an
// unquoted one runs through a placeholder RedactString left in it.
var secretAssignmentRE = regexp.MustCompile(
	`(?i)(` + secretKeyCandidate + `)(\s*[:=]\s*)` +
		"(\"(?:[^\"\\\\]|\\\\.)*\"|'[^']*'|`[^`]*`|(?:" + regexp.QuoteMeta(redactedPlaceholder) + "|[^\\s,}\\]\\n#])+)",
)

var yamlSecretBlockScalarHeaderRE = regexp.MustCompile(
	`(?i)^([ \t]*(?:-\s*)?` + secretKeyCandidate + `\s*:\s*)` +
		`([|>](?:[+-]?[0-9]|[0-9][+-]?)?[ \t]*(?:#.*)?)(\r?\n?)$`,
)

// CapturedContent is the opt-in raw body of a config, hook, or skill file with
// sensitive values redacted and the body truncated to a size cap. It is only
// populated when content capture is explicitly requested.
type CapturedContent struct {
	Bytes         int    `json:"bytes"`
	Truncated     bool   `json:"truncated,omitempty"`
	RedactedCount int    `json:"redacted_count,omitempty"`
	Text          string `json:"text"`
}

// contentOptions controls opt-in raw content capture during a scan.
type contentOptions struct {
	include  bool
	maxBytes int
}

func (c contentOptions) limit() int {
	if c.maxBytes > 0 {
		return c.maxBytes
	}
	return DefaultMaxContentBytes
}

// ContentLimit is how many bytes of a captured file a scan given MaxContentBytes maxBytes keeps.
func ContentLimit(maxBytes int) int {
	return contentOptions{maxBytes: maxBytes}.limit()
}

// captureContent redacts secrets in the raw file body and truncates the result
// to maxBytes on a UTF-8 rune boundary. Redaction runs before truncation so a
// secret can never be partially exposed by the cut.
func captureContent(data []byte, maxBytes int) *CapturedContent {
	redacted, count := redactSecrets(string(data))
	truncated := false
	if maxBytes > 0 && len(redacted) > maxBytes {
		redacted = asymptoteobserve.TruncateUTF8(redacted, maxBytes)
		truncated = true
	}
	return &CapturedContent{
		Bytes:         len(data),
		Truncated:     truncated,
		RedactedCount: count,
		Text:          redacted,
	}
}

// redactSecrets redacts captured text and returns it with the number of values replaced. Block
// scalars under a secret-looking key go first, whole, since RedactString would take the `|` for
// the value and keep the lines under it. RedactString then replaces what it recognizes anywhere
// (credential formats, Authorization headers, secret flags, URL passwords, labeled assignments),
// and what is left under a secret-looking key goes last.
func redactSecrets(text string) (string, int) {
	out := asymptoteobserve.RedactString(redactYAMLBlockScalarSecrets(text))
	out = secretAssignmentRE.ReplaceAllStringFunc(out, func(match string) string {
		sub := secretAssignmentRE.FindStringSubmatch(match)
		// A value RedactString already handled (`"Bearer [REDACTED]"`) stays as it is.
		if len(sub) != 4 || strings.Contains(sub[3], redactedPlaceholder) || !asymptoteobserve.IsSecretField(sub[1], strings.Trim(sub[3], "\"'`")) {
			return match
		}
		return sub[1] + sub[2] + redactValueLiteral(sub[3])
	})
	return out, max(0, strings.Count(out, redactedPlaceholder)-strings.Count(text, redactedPlaceholder))
}

func redactYAMLBlockScalarSecrets(text string) string {
	lines := splitLinesKeepEnd(text)
	if len(lines) == 0 {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		sub := yamlSecretBlockScalarHeaderRE.FindStringSubmatch(lines[i])
		if len(sub) != 4 || !asymptoteobserve.IsSecretKeyName(sub[1]) {
			out.WriteString(lines[i])
			continue
		}

		keyIndent := leadingIndentWidth(lines[i])
		bodyStart := i + 1
		bodyEnd := bodyStart
		blockIndent := yamlBlockScalarIndent(sub[2], keyIndent)
		if blockIndent == 0 {
			for bodyEnd < len(lines) && isBlankLine(lines[bodyEnd]) {
				bodyEnd++
			}
			if bodyEnd < len(lines) {
				indent := leadingIndentWidth(lines[bodyEnd])
				if indent > keyIndent {
					blockIndent = indent
				}
			}
		}
		if blockIndent == 0 {
			blockIndent = keyIndent + 2
		}
		for bodyEnd < len(lines) {
			if isBlankLine(lines[bodyEnd]) {
				bodyEnd++
				continue
			}
			if leadingIndentWidth(lines[bodyEnd]) < blockIndent {
				break
			}
			bodyEnd++
		}

		out.WriteString(sub[1])
		out.WriteString(redactedPlaceholder)
		out.WriteString(sub[3])
		i = bodyEnd - 1
	}
	return out.String()
}

// redactValueLiteral replaces a captured value with the placeholder while
// preserving the original quote style so the surrounding structure stays valid.
func redactValueLiteral(value string) string {
	switch {
	case strings.HasPrefix(value, `"`):
		return `"` + redactedPlaceholder + `"`
	case strings.HasPrefix(value, "'"):
		return "'" + redactedPlaceholder + "'"
	case strings.HasPrefix(value, "`"):
		return "`" + redactedPlaceholder + "`"
	default:
		return redactedPlaceholder
	}
}

// redactStructured walks a parsed config value and replaces values held under
// secret-looking keys with the placeholder, leaving structure intact. Strings
// get the same redaction as captured file text, and an argument after a flag
// named for a secret is one: `"args": ["--api-key", "x"]`.
func redactStructured(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, val := range typed {
			if asymptoteobserve.IsSecretField(key, val) {
				out[key] = redactedPlaceholder
				continue
			}
			out[key] = redactStructured(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			if arg, ok := item.(string); ok && arg != "" && i > 0 && followsSecretFlag(typed[i-1]) {
				out[i] = redactedPlaceholder
				continue
			}
			out[i] = redactStructured(item)
		}
		return out
	case string:
		redacted, _ := redactSecrets(typed)
		return redacted
	default:
		return value
	}
}

func followsSecretFlag(previous interface{}) bool {
	flag, ok := previous.(string)
	return ok && asymptoteobserve.IsSecretFlag(flag)
}

// redactStructuredMap redacts a definition block and returns it as a map.
func redactStructuredMap(def map[string]interface{}) map[string]interface{} {
	if m, ok := redactStructured(def).(map[string]interface{}); ok {
		return m
	}
	return nil
}

func splitLinesKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:idx+1])
		s = s[idx+1:]
	}
	return lines
}

func isBlankLine(line string) bool {
	return strings.TrimSpace(line) == ""
}

func leadingIndentWidth(line string) int {
	width := 0
	for _, r := range line {
		switch r {
		case ' ', '\t':
			width++
		default:
			return width
		}
	}
	return width
}

func yamlBlockScalarIndent(header string, keyIndent int) int {
	for _, r := range header[1:] {
		if r >= '1' && r <= '9' {
			return keyIndent + int(r-'0')
		}
	}
	return 0
}
