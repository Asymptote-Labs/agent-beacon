package tokens

import "strings"

// contextWindows maps normalized model-name prefixes to context window sizes
// in tokens. Longest prefix wins. The table is a best-effort static snapshot;
// unknown models simply report raw input tokens without a utilization ratio.
//
// Unknown is deliberately preferred to guessed. A wrong window does not read as
// missing data -- it reads as a confident ratio, and a model listed at a fifth
// of its real window reports every ordinary call as running near the limit.
// That is why entries are added only for models whose window is known from the
// vendor rather than inferred from a family name, and why the generic "claude"
// fallback below stays at the conservative 200000 rather than being raised to
// match the current generation.
var contextWindows = []struct {
	prefix string
	tokens int64
}{
	// Fallback for Claude models not named below -- the 3.x family, Sonnet 4.5,
	// Opus 4.5 and anything older, all of which are 200K.
	{"claude", 200000},
	// The current generation is 1M, with Haiku 4.5 the exception at 200K. Before
	// these entries existed every one of these models matched the generic
	// "claude" prefix above and was reported against a 200K window, so a routine
	// 150K-token call showed as 75% utilization and a 200K call tripped the
	// near-limit counter -- on a model with 800K still to spare.
	{"claude-opus-4-6", 1000000},
	{"claude-opus-4-7", 1000000},
	{"claude-opus-4-8", 1000000},
	{"claude-opus-5", 1000000},
	{"claude-sonnet-4-6", 1000000},
	{"claude-sonnet-5", 1000000},
	{"claude-fable-5", 1000000},
	{"claude-mythos-5", 1000000},
	// Explicit rather than left to the "claude" fallback: it lands on the same
	// 200000, but naming it keeps a later edit to the fallback from silently
	// moving Haiku with it.
	{"claude-haiku-4-5", 200000},
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
