package asymptoteobserve

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	DefaultStringLimit    = 4096
	DefaultRawStringLimit = 2048
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*bearer\s+[^"',\s]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)\s*[:=]\s*["']?[^"',\s]+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),
}

var assignedSecretPattern = regexp.MustCompile("(?i)(?:api[_-]?key|token|secret|password|authorization)\\s*[:=]\\s*[\"'`]?([^\"'`,\\s]+)")

type PrivacyOptions struct {
	RedactSecrets bool
	StringLimit   int
}

func RedactString(value string) string {
	// Remember assignment values before replacing their labeled occurrence so
	// repeated bare copies in the same retained prompt/output are also removed.
	// This is common in model reasoning that quotes `token=value` and later
	// refers to `value` by itself.
	var assigned []string
	for _, match := range assignedSecretPattern.FindAllStringSubmatch(value, -1) {
		if len(match) > 1 && len(match[1]) >= 8 {
			assigned = append(assigned, match[1])
		}
	}
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllStringFunc(value, func(match string) string {
			if strings.Contains(match, "=") {
				return match[:strings.Index(match, "=")+1] + "[REDACTED]"
			}
			if strings.Contains(match, ":") {
				return match[:strings.Index(match, ":")+1] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	for _, secret := range assigned {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func TruncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit < 32 {
		return value[:limit]
	}
	return value[:limit-15] + "...[truncated]"
}

func CleanString(value string, limit int, redactSecrets bool) string {
	value = TruncateString(value, limit)
	if redactSecrets {
		value = RedactString(value)
	}
	return value
}

func SanitizeMap(input map[string]interface{}, opts PrivacyOptions) map[string]interface{} {
	limit := opts.StringLimit
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case string:
			out[key] = CleanString(typed, limit, opts.RedactSecrets)
		case map[string]interface{}:
			out[key] = SanitizeMap(typed, opts)
		case []interface{}:
			out[key] = SanitizeSlice(typed, opts)
		default:
			out[key] = typed
		}
	}
	return out
}

func SanitizeSlice(input []interface{}, opts PrivacyOptions) []interface{} {
	limit := opts.StringLimit
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	out := make([]interface{}, len(input))
	for index, value := range input {
		switch typed := value.(type) {
		case string:
			out[index] = CleanString(typed, limit, opts.RedactSecrets)
		case map[string]interface{}:
			out[index] = SanitizeMap(typed, opts)
		case []interface{}:
			out[index] = SanitizeSlice(typed, opts)
		default:
			out[index] = typed
		}
	}
	return out
}

func SanitizeEvent(event Event, maxBytes int) Event {
	event.Message = CleanString(event.Message, DefaultStringLimit, true)
	if event.Tool != nil {
		event.Tool.Command = CleanString(event.Tool.Command, DefaultStringLimit, true)
		event.Tool.Path = TruncateString(event.Tool.Path, DefaultRawStringLimit)
	}
	if event.Command != nil {
		event.Command.Command = CleanString(event.Command.Command, DefaultStringLimit, true)
	}
	if event.Approval != nil {
		event.Approval.Reason = CleanString(event.Approval.Reason, DefaultStringLimit, true)
	}
	if event.Policy != nil {
		event.Policy.Reason = CleanString(event.Policy.Reason, DefaultStringLimit, true)
	}
	if event.Prompt != nil {
		event.Prompt.Text = CleanString(event.Prompt.Text, DefaultStringLimit, true)
	}
	if event.MCP != nil {
		event.MCP = sanitizeTyped(event.MCP, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if event.GenAI != nil {
		event.GenAI = sanitizeTyped(event.GenAI, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if event.Raw != nil {
		event.Raw = SanitizeMap(event.Raw, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if data, err := json.Marshal(event); err == nil && len(data) > maxBytes {
		event.Truncated = true
	}
	return event
}

func sanitizeTyped[T any](input *T, opts PrivacyOptions) *T {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		out := *input
		return &out
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		out := *input
		return &out
	}
	raw = SanitizeMap(raw, opts)
	data, err = json.Marshal(raw)
	if err != nil {
		out := *input
		return &out
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		fallback := *input
		return &fallback
	}
	return &out
}
