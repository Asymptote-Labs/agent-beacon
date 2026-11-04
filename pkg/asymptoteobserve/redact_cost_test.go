package asymptoteobserve

import (
	"strings"
	"testing"
)

// BenchmarkRedactStringRepeatedAssignments covers the bare-copy pass, which removes every
// assigned value in one sweep. One strings.ReplaceAll per assignment was quadratic in the text:
// 300 KiB of "token=AAAAAAAA " took about 6 s, of which this pass is now a small part.
func BenchmarkRedactStringRepeatedAssignments(b *testing.B) {
	value := strings.Repeat("token=AAAAAAAA ", 20_000)
	b.SetBytes(int64(len(value)))
	for b.Loop() {
		RedactString(value)
	}
}

func TestRedactStringRemovesBareCopiesLongestFirst(t *testing.T) {
	out := RedactString("token=hunter2hunter2 password=hunter2hunter2xyz later hunter2hunter2xyz and hunter2hunter2")
	if strings.Contains(out, "hunter2") || strings.Contains(out, "xyz") {
		t.Fatalf("copy left in %q", out)
	}
}

func TestIsSecretField(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value interface{}
		want  bool
	}{
		{"password", "hunter2", true}, {"AWS_SECRET_ACCESS_KEY", "x", true}, {"client_secret", "x", true},
		{"X-Api-Key", "x", true}, {"SECRET_KEY_BASE", "x", true}, {"DB_PASS", "x", true}, {"SMTP_PASS", "x", true},
		{"GITHUB_TOKENS", "abc", true}, {"pin_token", 1234, true}, {"password", "12345678", true},
		{"max_tokens", 4096, false}, {"input_tokens", float64(812), false}, {"input_tokens", "1200", false},
		// "token" followed by count or usage names a count (Codex's and Gemini's usage attributes,
		// which Codex sends as strings); anything else after it, or nothing, is still a credential.
		{"input_token_count", "1200", false}, {"total_token_count", int64(9), false}, {"inputTokenCount", "7", false},
		{"cached_content_token_count", "0", false}, {"token_usage", "42", false}, {"input_token_count", "sk-abc", true},
		{"GITHUB_TOKEN", "abc", true}, {"github_token", "12345678", true}, {"token_id", "1234", true}, {"refresh_token", "77", true},
		{"token_type", "Bearer", false}, {"author", "Ada", false}, {"PWD", "/home/ada", false}, {"keyboard", "dvorak", false},
	} {
		if got := IsSecretField(tc.key, tc.value); got != tc.want {
			t.Errorf("IsSecretField(%q, %#v) = %v, want %v", tc.key, tc.value, got, tc.want)
		}
	}
}
