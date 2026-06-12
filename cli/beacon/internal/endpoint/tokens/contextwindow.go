package tokens

import "strings"

// contextWindows maps normalized model-name prefixes to context window sizes
// in tokens. Longest prefix wins. The table is a best-effort static snapshot;
// unknown models simply report raw input tokens without a utilization ratio.
var contextWindows = []struct {
	prefix string
	tokens int64
}{
	{"claude", 200000},
	{"gpt-5", 400000},
	{"gpt-4.1", 1047576},
	{"gpt-4o", 128000},
	{"gpt-4-turbo", 128000},
	{"gpt-4", 8192},
	{"gpt-3.5", 16385},
	{"o1", 200000},
	{"o3", 200000},
	{"o4", 200000},
	{"gemini-1.5-pro", 2097152},
	{"gemini-1.5-flash", 1048576},
	{"gemini-2", 1048576},
	{"gemini-3", 1048576},
}

// ContextWindow returns the context window size for a model name, matching
// provider-prefixed and date-suffixed variants such as
// "anthropic/claude-sonnet-4-5" or "gpt-4o-2024-08-06".
func ContextWindow(model string) (int64, bool) {
	normalized := normalizeModel(model)
	if normalized == "" {
		return 0, false
	}
	best := int64(0)
	bestLen := -1
	for _, entry := range contextWindows {
		if strings.HasPrefix(normalized, entry.prefix) && len(entry.prefix) > bestLen {
			best = entry.tokens
			bestLen = len(entry.prefix)
		}
	}
	return best, bestLen >= 0
}

func normalizeModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	return normalized
}
