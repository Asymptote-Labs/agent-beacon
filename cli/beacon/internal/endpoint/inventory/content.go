package inventory

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// DefaultMaxContentBytes caps how much redacted text is retained per captured
// file when content capture is requested. Files larger than the cap are
// truncated to a UTF-8 rune boundary and flagged as truncated.
const DefaultMaxContentBytes = 64 * 1024

const redactedPlaceholder = "***REDACTED***"

// secretMarkers are the substring markers that identify sensitive config keys.
// They are shared by the env-key filter and the content/definition redactors so
// captured contents and summarized metadata stay consistent.
var secretMarkers = []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "AUTH", "CREDENTIAL"}

// secretAssignmentRE matches a `key: value` or `key = value` assignment where
// the key contains a secret marker, across JSON, TOML, YAML, and shell-style
// content. The value is captured separately so it can be replaced while
// preserving the surrounding key, separator, and quote style.
var secretAssignmentRE = regexp.MustCompile(
	`(?i)("?[A-Za-z0-9_.\-]*(?:` + strings.ToLower(strings.Join(secretMarkers, "|")) + `)[A-Za-z0-9_.\-]*"?)(\s*[:=]\s*)` +
		"(\"(?:[^\"\\\\]|\\\\.)*\"|'[^']*'|`[^`]*`|[^\\s,}\\]\\n#]+)",
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

// captureContent redacts secrets in the raw file body and truncates the result
// to maxBytes on a UTF-8 rune boundary. Redaction runs before truncation so a
// secret can never be partially exposed by the cut.
func captureContent(data []byte, maxBytes int) *CapturedContent {
	redacted, count := redactSecrets(string(data))
	truncated := false
	if maxBytes > 0 && len(redacted) > maxBytes {
		redacted = truncateUTF8(redacted, maxBytes)
		truncated = true
	}
	return &CapturedContent{
		Bytes:         len(data),
		Truncated:     truncated,
		RedactedCount: count,
		Text:          redacted,
	}
}

// redactSecrets replaces the values of secret-looking assignments with a
// placeholder and returns the redacted text plus the number of redactions.
func redactSecrets(text string) (string, int) {
	count := 0
	out := secretAssignmentRE.ReplaceAllStringFunc(text, func(match string) string {
		sub := secretAssignmentRE.FindStringSubmatch(match)
		if len(sub) != 4 {
			return match
		}
		count++
		return sub[1] + sub[2] + redactValueLiteral(sub[3])
	})
	return out, count
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

// redactStructured walks a parsed config value and replaces string values held
// under secret-looking keys with the placeholder, leaving structure intact.
func redactStructured(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, val := range typed {
			if containsSecretMarker(key) {
				out[key] = redactedPlaceholder
				continue
			}
			out[key] = redactStructured(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = redactStructured(item)
		}
		return out
	default:
		return value
	}
}

// redactStructuredMap redacts a definition block and returns it as a map.
func redactStructuredMap(def map[string]interface{}) map[string]interface{} {
	if m, ok := redactStructured(def).(map[string]interface{}); ok {
		return m
	}
	return nil
}

func containsSecretMarker(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range secretMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func truncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
