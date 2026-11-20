package asymptoteobserve

import (
	"strings"
	"testing"
)

// TestOrdinaryTextReachesNoRedactionPattern holds the redaction gate: text with no credential
// shape in it, which is nearly all of what a hook redacts, must not run a single pattern.
func TestOrdinaryTextReachesNoRedactionPattern(t *testing.T) {
	ordinary := []string{
		"info", "tool.invoked", "claude_code", "abc-123", "2026-10-12T03:47:36.515970306Z",
		"/home/user/project/internal/auth/config.go",
		"cd /home/user/project && go test ./... -count=1 2>&1 | tail -50",
		strings.Repeat("ok  \tgithub.com/example/project/internal/pkg\t0.123s  // refreshes the api token cache\n", 100),
		"input_tokens: 812, output_tokens: 90, cache_read_input_tokens: 4000",
		"Please refactor the token refresh logic so the api key is read from the environment.",
		"func load(cfg Config) string { return cfg.Endpoint }",
		"keyboard shortcuts, passport photos, secretary notes",
	}
	for _, text := range ordinary {
		gate := redactGate{value: text, enabled: true}
		if gate.holds(redactLabelNeedles) {
			t.Errorf("%.60q reaches the assignment pattern", text)
		}
		for i, rule := range redactionRules {
			if gate.holds(rule.needles) {
				t.Errorf("%.60q reaches redaction rule %d (%s)", text, i, rule.pattern())
			}
		}
	}
}
