package tokens

import "testing"

func TestContextWindowMatchesModelVariants(t *testing.T) {
	tests := []struct {
		model string
		want  int64
		known bool
	}{
		{"claude-sonnet-4-5", 200000, true},
		{"anthropic/claude-3-5-sonnet-20241022", 200000, true},
		// The current generation is 1M. Longest prefix wins, so these must beat
		// the generic "claude" fallback rather than inheriting its 200K.
		{"claude-opus-5", 1000000, true},
		{"claude-opus-4-8", 1000000, true},
		{"claude-opus-4-6", 1000000, true},
		{"claude-sonnet-5", 1000000, true},
		{"claude-sonnet-4-6-20260101", 1000000, true},
		{"claude-fable-5-1", 1000000, true},
		{"anthropic/claude-opus-5", 1000000, true},
		// Haiku 4.5 is the current-generation exception at 200K.
		{"claude-haiku-4-5", 200000, true},
		{"gpt-4o-2024-08-06", 128000, true},
		{"gpt-4o-mini", 128000, true},
		{"gpt-4.1-mini", 1047576, true},
		{"gpt-4-turbo", 128000, true},
		{"gpt-4", 8192, true},
		{"gpt-5-mini", 400000, true},
		{"o1-preview", 200000, true},
		{"gemini-1.5-pro-latest", 2097152, true},
		{"gemini-2.0-flash", 1048576, true},
		{"GPT-4O", 128000, true},
		{"experimental-model", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, known := ContextWindow(tt.model)
			if got != tt.want || known != tt.known {
				t.Fatalf("ContextWindow(%q) = %d/%v, want %d/%v", tt.model, got, known, tt.want, tt.known)
			}
		})
	}
}

// Sonnet 4.5 (200K) and Sonnet 5 (1M) differ by one character in a position that a
// careless prefix rule gets wrong, and getting it wrong is invisible: the ratio still
// renders, just against the wrong denominator.
func TestContextWindowDoesNotConfuseAdjacentClaudeVersions(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int64
	}{
		{"claude-sonnet-4-5", 200000},
		{"claude-sonnet-5", 1000000},
		{"claude-opus-4-5", 200000},
		{"claude-opus-4-6", 1000000},
		{"claude-haiku-4-5", 200000},
	} {
		got, known := ContextWindow(tc.model)
		if !known || got != tc.want {
			t.Errorf("ContextWindow(%q) = %d/%v, want %d/true", tc.model, got, known, tc.want)
		}
	}
}

// An unknown model must stay unknown rather than being given a plausible-looking window.
// A wrong window does not read as missing data; it reads as a confident ratio, and the
// near-limit counter fires on it.
func TestContextWindowLeavesUnknownModelsUnknown(t *testing.T) {
	for _, model := range []string{"llama-4-405b", "some-vendor/frontier-x", "qwen3-coder-plus"} {
		if got, known := ContextWindow(model); known {
			t.Errorf("ContextWindow(%q) = %d/true, want unknown", model, got)
		}
	}
}
