package asymptoteobserve

import "strings"

// NormalizeModelName maps whatever a runtime calls a model onto one canonical spelling, and
// SplitModelProvider recovers the provider prefix that normalizing removes.
//
// This exists for the same reason NormalizeHarnessName does, one field over. `model` is what
// every token report groups by -- `beacon token-usage` builds its BY MODEL rollup with
// addGroup(byModel, event.Model, ...) on the raw string -- so the same model reported under two
// spellings is two rows, and a reader comparing spend across runtimes sees a model's cost split
// in half with no indication that it happened. The spellings genuinely differ in practice:
// Claude Code's OTLP reports `claude-sonnet-4-5`, an OpenRouter-backed runtime reports
// `anthropic/claude-sonnet-4-5`, and a gateway reports `Anthropic/Claude-Sonnet-4-5`. All three
// are one model and one bill.
//
// Normalizing at the point of writing rather than at every point of reading is the same choice
// the harness name made, for the same reason: the alternative pushes the inconsistency onto
// every consumer, including the SIEM queries and dashboards Beacon does not own.
//
// What it does, and deliberately no more:
//
//   - Trims surrounding whitespace and lowercases. Model ids are conventionally lowercase, and
//     the runtimes that capitalize them are the same ones that vary the prefix.
//   - Drops a provider prefix, keeping the last "/"-separated segment. The prefix is not
//     discarded by callers: SplitModelProvider returns it so it can be recorded as
//     gen_ai.provider.name, which is where a provider belongs and where it stays queryable.
//
// What it deliberately does NOT do is rewrite the model id itself -- no dot-to-dash folding, no
// date-suffix stripping, no vendor alias table. It is tempting, because Copilot reports
// `claude-sonnet-4.6` where Anthropic's own id is `claude-sonnet-4-6`, and folding the dot would
// merge them. But the same rule applied to `gpt-4.1` produces `gpt-4-1`, which is not a model
// that exists under any name, and a canonical field whose values do not exist anywhere is worse
// than one that occasionally splits: a split row is visible, an invented id is not. Alias
// mapping needs a catalog to validate against, so it belongs with the pricing work, not here.
//
// The empty string in, empty string out. An unrecognized model keeps its own name, as an
// unrecognized harness does.
func NormalizeModelName(model string) string {
	normalized, _ := SplitModelProvider(model)
	return normalized
}

// SplitModelProvider returns the canonical model name and the provider prefix that was removed
// from it, both normalized. The provider is empty when the name carried no prefix.
//
// Only the last segment is treated as the model, so a doubly-prefixed name such as
// `openrouter/anthropic/claude-sonnet-4-5` yields the model `claude-sonnet-4-5` and the provider
// `openrouter/anthropic` -- the whole prefix rather than just its last component, because
// discarding "openrouter" would claim the call went somewhere it did not.
//
// A trailing slash is not a provider: "anthropic/" has no model part, so the input is returned
// unchanged rather than reduced to the empty string. Same for a leading slash, which would
// otherwise report an empty provider as if one had been read.
func SplitModelProvider(model string) (name string, provider string) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", ""
	}
	idx := strings.LastIndex(normalized, "/")
	if idx < 0 {
		return normalized, ""
	}
	prefix, suffix := normalized[:idx], normalized[idx+1:]
	if prefix == "" || suffix == "" {
		// "/model" or "provider/" -- neither half is a usable pair, so nothing is split off.
		return normalized, ""
	}
	return suffix, prefix
}
