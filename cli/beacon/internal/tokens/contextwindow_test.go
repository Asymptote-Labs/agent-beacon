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
