package asymptoteobserve

import "testing"

func TestNormalizeModelName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"already canonical", "claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"trims and lowercases", "  Claude-Sonnet-4-5 ", "claude-sonnet-4-5"},
		{"drops provider prefix", "anthropic/claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"drops mixed-case provider prefix", "Anthropic/Claude-Sonnet-4-5", "claude-sonnet-4-5"},
		{"drops multi-segment prefix", "openrouter/anthropic/claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"unknown model keeps its own name", "some-new-model-v9", "some-new-model-v9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeModelName(tc.input); got != tc.want {
				t.Fatalf("NormalizeModelName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// The whole point of the field: these spellings all describe one model and one bill, and before
// normalization each was its own row in the BY MODEL rollup.
func TestNormalizeModelNameCollapsesSpellingsOfOneModel(t *testing.T) {
	spellings := []string{
		"claude-sonnet-4-5",
		"anthropic/claude-sonnet-4-5",
		"Anthropic/Claude-Sonnet-4-5",
		"  claude-sonnet-4-5  ",
		"openrouter/anthropic/claude-sonnet-4-5",
	}
	for _, spelling := range spellings {
		if got := NormalizeModelName(spelling); got != "claude-sonnet-4-5" {
			t.Fatalf("NormalizeModelName(%q) = %q, want all spellings to collapse to claude-sonnet-4-5", spelling, got)
		}
	}
}

// Model ids are not rewritten. Folding the dot in Copilot's claude-sonnet-4.6 would merge it with
// Anthropic's claude-sonnet-4-6, but the same rule turns gpt-4.1 into gpt-4-1, which is not a
// model under any name. A split row is visible; an invented id is not. See the doc comment.
func TestNormalizeModelNameDoesNotInventIDs(t *testing.T) {
	for _, model := range []string{"gpt-4.1", "claude-sonnet-4.6", "gemini-2.5-pro", "gpt-4o-2024-08-06"} {
		if got := NormalizeModelName(model); got != model {
			t.Fatalf("NormalizeModelName(%q) = %q, want the id left intact", model, got)
		}
	}
}

func TestSplitModelProvider(t *testing.T) {
	for _, tc := range []struct {
		name         string
		input        string
		wantModel    string
		wantProvider string
	}{
		{"no prefix", "claude-sonnet-4-5", "claude-sonnet-4-5", ""},
		{"single prefix", "anthropic/claude-sonnet-4-5", "claude-sonnet-4-5", "anthropic"},
		{"multi prefix keeps the whole path", "openrouter/anthropic/claude-sonnet-4-5", "claude-sonnet-4-5", "openrouter/anthropic"},
		{"lowercases the provider too", "OpenRouter/Anthropic/Claude-Sonnet-4-5", "claude-sonnet-4-5", "openrouter/anthropic"},
		{"empty", "", "", ""},
		// Neither half of these is a usable pair, so nothing is split off rather than
		// reporting an empty model or an empty provider that was never read.
		{"trailing slash is not a provider", "anthropic/", "anthropic/", ""},
		{"leading slash is not a provider", "/claude-sonnet-4-5", "/claude-sonnet-4-5", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, provider := SplitModelProvider(tc.input)
			if model != tc.wantModel || provider != tc.wantProvider {
				t.Fatalf("SplitModelProvider(%q) = (%q, %q), want (%q, %q)", tc.input, model, provider, tc.wantModel, tc.wantProvider)
			}
		})
	}
}

// Normalizing must be idempotent: the canonical value has to survive being normalized again, or
// re-normalizing a stored event would keep changing it. This is the property vscode_copilot
// failed on the harness side before it was pinned.
func TestNormalizeModelNameIsIdempotent(t *testing.T) {
	for _, model := range []string{
		"anthropic/claude-sonnet-4-5", "gpt-5", "openrouter/anthropic/claude-sonnet-4-5",
		"anthropic/", "/gpt-5", "gemini-2.5-pro",
	} {
		once := NormalizeModelName(model)
		if twice := NormalizeModelName(once); twice != once {
			t.Fatalf("NormalizeModelName(%q) = %q, but normalizing again gave %q", model, once, twice)
		}
	}
}
