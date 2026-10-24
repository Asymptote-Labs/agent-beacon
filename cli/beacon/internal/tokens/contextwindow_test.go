package tokens

import "testing"

func TestContextWindowMatchesModelVariants(t *testing.T) {
	tests := []struct {
		model string
		want  int64
		known bool
	}{
		// Provider prefix stripped; falls back to the generic "claude" entry.
		{"anthropic/claude-3-5-sonnet-20241022", 200000, true},
		// Longest prefix wins over the generic "claude" fallback, through a provider prefix
		// and a date suffix.
		{"anthropic/claude-opus-5", 1000000, true},
		{"claude-sonnet-4-6-20260101", 1000000, true},
		// "gpt-4.1" beats "gpt-4".
		{"gpt-4.1-mini", 1047576, true},
		{"gpt-4", 8192, true},
		{"GPT-4O", 128000, true},
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

// Copilot reports Claude models with a dotted version. They have the window of the dashed id,
// not the 200K "claude" fallback.
func TestContextWindowMatchesDottedClaudeVersions(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int64
	}{
		{"claude-sonnet-4.6", 1000000},
		{"claude-opus-4.7", 1000000},
		{"claude-haiku-4.5", 200000},
		{"gpt-4.1", 1047576},
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
