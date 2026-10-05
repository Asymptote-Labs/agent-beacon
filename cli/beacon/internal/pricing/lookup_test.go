package pricing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Model strings as Beacon records them, drawn from the runtimes' own fixtures across the repo
// (Claude Code OTLP and session files, Codex, Copilot, OpenCode, Pi, DeepSeek Harness, Cursor,
// the collector exporter) plus the spellings the runtimes are known to emit. The expected keys
// are the embedded catalog's, so a regeneration that moves one is a reviewable test change.
func TestLookupRealModelStrings(t *testing.T) {
	cases := []struct {
		model    string
		key      string // "" = must stay unpriced
		match    Match
		stripped []string
	}{
		// Claude Code reports Anthropic's ids, with a snapshot date or a [1m] marker.
		{"claude-opus-4-6", "claude-opus-4-6", MatchExact, nil},
		{"claude-opus-4-6[1m]", "claude-opus-4-6", MatchExact, []string{StrippedContextWindow}},
		{"claude-opus-5[1m]", "claude-opus-5", MatchExact, []string{StrippedContextWindow}},
		{"claude-sonnet-4-5", "claude-sonnet-4-5", MatchExact, nil},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929", MatchExact, nil},
		{"claude-haiku-4-5", "claude-haiku-4-5", MatchExact, nil},
		{"claude-opus-4-7", "claude-opus-4-7", MatchExact, nil},
		{"claude-sonnet-5-5", "claude-sonnet-5-5", MatchExact, nil},
		{"claude-fable-5-1", "claude-fable-5-1", MatchExact, nil},
		// A snapshot the list does not carry is priced as its undated alias.
		{"claude-sonnet-4-6-20260101", "claude-sonnet-4-6", MatchExact, []string{StrippedDateSnapshot}},
		// Older snapshots Anthropic's own list has retired survive as Bedrock model ids.
		{"claude-sonnet-4-20250514", "anthropic.claude-sonnet-4-20250514-v1:0", MatchProviderPrefix, nil},
		{"claude-opus-4-1-20250805", "anthropic.claude-opus-4-1-20250805-v1:0", MatchProviderPrefix, nil},
		// Claude Code on Bedrock reports the inference profile, which has its own price.
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", MatchExact, nil},
		{"global.anthropic.claude-opus-4-6-v1", "global.anthropic.claude-opus-4-6-v1", MatchExact, nil},
		// Copilot and OpenRouter-backed runtimes dot the version.
		{"claude-sonnet-4.6", "claude-sonnet-4-6", MatchDottedVersion, nil},
		{"claude-opus-4.6", "claude-opus-4-6", MatchDottedVersion, nil},
		{"anthropic/claude-opus-4.6", "claude-opus-4-6", MatchDottedVersion, nil},
		// Provider prefixes and capitalization from gateways.
		{"Anthropic/Claude-Sonnet-4-5", "claude-sonnet-4-5", MatchCanonical, nil},
		{"openrouter/anthropic/claude-sonnet-4-5", "claude-sonnet-4-5", MatchCanonical, nil},
		// Codex and OpenAI-backed runtimes.
		{"gpt-5.4", "gpt-5.4", MatchExact, nil},
		{"gpt-5", "gpt-5", MatchExact, nil},
		{"gpt-5-codex", "gpt-5-codex", MatchExact, nil},
		{"gpt-5.1-codex-max", "gpt-5.1-codex-max", MatchExact, nil},
		{"gpt-5.3-codex", "gpt-5.3-codex", MatchExact, nil},
		{"gpt-5.5", "gpt-5.5", MatchExact, nil},
		{"gpt-5.6-sol", "gpt-5.6-sol", MatchExact, nil},
		{"gpt-4o", "gpt-4o", MatchExact, nil},
		{"gpt-4o-mini", "gpt-4o-mini", MatchExact, nil},
		{"gpt-4o-2024-08-06", "gpt-4o-2024-08-06", MatchExact, nil},
		{"gpt-4.1", "gpt-4.1", MatchExact, nil},
		{"o3", "o3", MatchExact, nil},
		{"o4-mini", "o4-mini", MatchExact, nil},
		{"openai/gpt-5", "gpt-5", MatchCanonical, nil},
		// Effort appended to the model name (OpenCode variants, Cursor's thinking models).
		{"gpt-5.5-medium", "gpt-5.5", MatchExact, []string{StrippedEffortSuffix}},
		{"gpt-5.5-high", "gpt-5.5", MatchExact, []string{StrippedEffortSuffix}},
		{"gpt-5-codex-high", "gpt-5-codex", MatchExact, []string{StrippedEffortSuffix}},
		{"claude-sonnet-4-5-thinking", "claude-sonnet-4-5", MatchExact, []string{StrippedEffortSuffix}},
		// Decorations stack in whatever order a runtime appends them: an effort suffix on a dated
		// snapshot comes off first, and the date it exposes is stripped on the next pass.
		{"claude-sonnet-4-6-20260101-high", "claude-sonnet-4-6", MatchExact, []string{StrippedEffortSuffix, StrippedDateSnapshot}},
		{"claude-sonnet-4-6-20260101-thinking[1m]", "claude-sonnet-4-6", MatchExact, []string{StrippedContextWindow, StrippedEffortSuffix, StrippedDateSnapshot}},
		// Gemini CLI.
		{"gemini-2.5-pro", "gemini-2.5-pro", MatchExact, nil},
		{"gemini-3.1-pro-preview", "gemini-3.1-pro-preview", MatchExact, nil},
		{"gemini-3-pro-preview", "vertex_ai/gemini-3-pro-preview", MatchCanonical, nil},
		// The other vendors' own APIs, whose upstream keys carry a provider prefix.
		{"deepseek-chat", "deepseek-chat", MatchExact, nil},
		{"deepseek-reasoner", "deepseek-reasoner", MatchExact, nil},
		{"glm-4.6", "zai/glm-4.6", MatchCanonical, nil},
		{"grok-code-fast-1", "xai/grok-code-fast-1", MatchCanonical, nil},
		{"kimi-k3", "moonshot/kimi-k3", MatchCanonical, nil},
		{"moonshotai/kimi-k3", "moonshot/kimi-k3", MatchCanonical, nil},
		{"kimi-k2.5", "moonshot/kimi-k2.5", MatchCanonical, nil},
		{"devstral-medium-latest", "mistral/devstral-medium-latest", MatchCanonical, nil},
		// Retired from the vendor's list, priced from OpenRouter's.
		{"kimi-k2", "openrouter/moonshotai/kimi-k2", MatchCanonical, nil},
		{"claude-sonnet-4", "openrouter/anthropic/claude-sonnet-4", MatchCanonical, nil},

		// Unpriced: names that are not models, and names that resemble a model without
		// being one. No substring or fuzzy match may price any of these.
		{"<synthetic>", "", "", nil},
		{"auto", "", "", nil},
		{"unknown", "", "", nil},
		{"", "", "", nil},
		{"grok-build", "", "", nil},
		{"muse-spark-1.3", "", "", nil},
		{"claude-4.5-sonnet", "", "", nil},
		{"claude-sonnet", "", "", nil},
		{"gpt-5.5-turbo", "", "", nil},
		{"fake/model", "", "", nil},
	}
	for _, c := range cases {
		res, ok := Lookup(c.model)
		if c.key == "" {
			if ok {
				t.Errorf("Lookup(%q) priced it as %q (%s); want unpriced", c.model, res.Key, res.Match)
			}
			continue
		}
		if !ok {
			t.Errorf("Lookup(%q) unpriced (candidates %v); want %q", c.model, res.Candidates, c.key)
			continue
		}
		if res.Key != c.key || res.Match != c.match || !reflect.DeepEqual(res.Stripped, c.stripped) {
			t.Errorf("Lookup(%q) = key %q match %s stripped %v; want %q %s %v",
				c.model, res.Key, res.Match, res.Stripped, c.key, c.match, c.stripped)
		}
		if res.Rates.Input <= 0 || res.Rates.Output <= 0 {
			t.Errorf("Lookup(%q) returned rates %+v", c.model, res.Rates)
		}
	}
}

func testCatalog(t *testing.T, models map[string]Rates) *Catalog {
	t.Helper()
	data, err := json.Marshal(CatalogFile{
		SchemaVersion: SchemaVersion,
		Source:        SourceInfo{Name: "test", License: "MIT", URL: "https://example.invalid", FetchedAt: "2026-01-01"},
		GeneratedAt:   "2026-01-01",
		Unit:          RateUnit,
		Models:        models,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func flat(provider string, in, out int64) Rates {
	return Rates{Provider: provider, RateSet: RateSet{Input: in, Output: out}}
}

func TestLookupTieWithDifferentPricesIsUnpriced(t *testing.T) {
	c := testCatalog(t, map[string]Rates{
		"vendor-a/model-x": flat("a", 1_000_000, 2_000_000),
		"vendor-b/model-x": flat("b", 3_000_000, 4_000_000),
	})
	res, ok := c.Lookup("model-x")
	if ok {
		t.Fatalf("priced %q despite a price disagreement", res.Key)
	}
	if want := []string{"vendor-a/model-x", "vendor-b/model-x"}; !reflect.DeepEqual(res.Candidates, want) || res.Match != MatchCanonical {
		t.Fatalf("candidates %v at %s, want %v at canonical", res.Candidates, res.Match, want)
	}
	// A tie is final: stripping a suffix must not go looking for a looser match that
	// happens to be unique.
	c = testCatalog(t, map[string]Rates{
		"vendor-a/model-x-high": flat("a", 1_000_000, 2_000_000),
		"vendor-b/model-x-high": flat("b", 3_000_000, 4_000_000),
		"model-x":               flat("c", 5_000_000, 6_000_000),
	})
	if res, ok := c.Lookup("model-x-high"); ok {
		t.Fatalf("tie fell through to %q", res.Key)
	}
}

func TestLookupTieWithTheSamePriceIsPriced(t *testing.T) {
	c := testCatalog(t, map[string]Rates{
		"claude-opus-9":           flat("anthropic", 5_000_000, 25_000_000),
		"vertex_ai/claude-opus-9": flat("vertex_ai-anthropic_models", 5_000_000, 25_000_000),
	})
	res, ok := c.Lookup("Claude-Opus-9")
	if !ok || res.Key != "claude-opus-9" || res.Match != MatchCanonical {
		t.Fatalf("got %+v ok=%v, want claude-opus-9 at canonical", res, ok)
	}
}

func TestLookupPrefersTheLeastLooseRung(t *testing.T) {
	c := testCatalog(t, map[string]Rates{
		"model-1.5":       flat("x", 1_000_000, 1_000_000),
		"model-1-5":       flat("y", 2_000_000, 2_000_000),
		"model-1.5-pro":   flat("x", 3_000_000, 3_000_000),
		"model-1.5-thing": flat("x", 4_000_000, 4_000_000),
	})
	// Exact beats the dotted-version rung that would tie the first two.
	if res, ok := c.Lookup("model-1.5"); !ok || res.Key != "model-1.5" || res.Match != MatchExact {
		t.Fatalf("model-1.5 = %+v ok=%v", res, ok)
	}
	// So does the canonical rung, which does not fold the dot.
	if res, ok := c.Lookup("MODEL-1.5"); !ok || res.Key != "model-1.5" || res.Match != MatchCanonical {
		t.Fatalf("MODEL-1.5 = %+v ok=%v", res, ok)
	}
	if res, ok := c.Lookup("model-1_5"); ok {
		t.Fatalf("model-1_5 priced as %q; underscores are not a spelling rung", res.Key)
	}
	// No prefix matching: model-1.5-pro is not model-1.5, and model-1 is neither.
	if res, ok := c.Lookup("model-1"); ok {
		t.Fatalf("model-1 priced as %q", res.Key)
	}
	if res, ok := c.Lookup("model-1.5-pro-max"); ok {
		t.Fatalf("model-1.5-pro-max priced as %q", res.Key)
	}
}

func TestLookupBedrockRoutes(t *testing.T) {
	c := testCatalog(t, map[string]Rates{
		"claude-sonnet-9":                        flat("anthropic", 3_000_000, 15_000_000),
		"us.anthropic.claude-sonnet-9-v1:0":      flat("bedrock_converse", 3_300_000, 16_500_000),
		"anthropic.claude-haiku-9-20990101-v1:0": flat("bedrock_converse", 1_000_000, 5_000_000),
	})
	cases := []struct {
		model, key string
		match      Match
	}{
		// The exact inference profile: its own (regional) price.
		{"us.anthropic.claude-sonnet-9-v1:0", "us.anthropic.claude-sonnet-9-v1:0", MatchExact},
		{"bedrock/us.anthropic.claude-sonnet-9-v1:0", "us.anthropic.claude-sonnet-9-v1:0", MatchCanonical},
		// A plain id reaches a Bedrock-only entry through the provider rung.
		{"claude-haiku-9-20990101", "anthropic.claude-haiku-9-20990101-v1:0", MatchProviderPrefix},
		// A profile the catalog lacks meets both the Anthropic price and the US profile's at
		// the provider rung, and they disagree: unpriced rather than either guess.
		{"eu.anthropic.claude-sonnet-9-v1:0", "", MatchProviderPrefix},
		{"anthropic.claude-sonnet-9-v2:0", "", MatchProviderPrefix},
	}
	for _, tc := range cases {
		res, ok := c.Lookup(tc.model)
		if ok != (tc.key != "") || res.Key != tc.key || res.Match != tc.match {
			t.Errorf("Lookup(%q) = key %q match %s ok=%v candidates %v; want %q %s",
				tc.model, res.Key, res.Match, ok, res.Candidates, tc.key, tc.match)
		}
	}
}

func TestLookupDoesNotStripTheRealModelName(t *testing.T) {
	// kimi-k2-thinking and qwen3-32b-thinking are model names that end in an effort word; an
	// exact entry must win over the reduction.
	c := testCatalog(t, map[string]Rates{
		"kimi-k2":          flat("x", 1_000_000, 1_000_000),
		"kimi-k2-thinking": flat("x", 2_000_000, 2_000_000),
	})
	if res, ok := c.Lookup("kimi-k2-thinking"); !ok || res.Key != "kimi-k2-thinking" || len(res.Stripped) != 0 {
		t.Fatalf("kimi-k2-thinking = %+v ok=%v", res, ok)
	}
}
