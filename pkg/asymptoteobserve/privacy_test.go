package asymptoteobserve

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactStringRedactsKnownSecretForms(t *testing.T) {
	tests := []string{
		"authorization: Bearer secret-token",
		"token=super-secret",
		"Bearer abcdef0123456789",
		"sk-abcdefghijklmnopqrstuvwxyz",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if got := RedactString(input); strings.Contains(got, "secret") || strings.Contains(got, "abcdefghijklmnopqrstuvwxyz") {
				t.Fatalf("RedactString(%q) = %q", input, got)
			}
		})
	}
}

func TestSanitizeMapDoesNotMutateInput(t *testing.T) {
	input := map[string]interface{}{
		"token": "token=super-secret",
		"nested": map[string]interface{}{
			"authorization": "authorization: Bearer nested-secret",
		},
	}

	got := SanitizeMap(input, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	if input["token"] != "token=super-secret" {
		t.Fatalf("SanitizeMap mutated input: %#v", input)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal sanitized map: %v", err)
	}
	if strings.Contains(string(data), "super-secret") || strings.Contains(string(data), "nested-secret") {
		t.Fatalf("SanitizeMap leaked secret: %s", string(data))
	}
}

func TestSanitizeEventRedactsAndTruncates(t *testing.T) {
	event := NewEvent(NewEventOptions{
		Action:  "tool.invoked",
		Harness: HarnessInfo{Name: "test"},
		Message: "token=message-secret",
	})
	event.Tool = &ToolInfo{
		Command: "curl -H 'Authorization: Bearer command-secret'",
		Path:    strings.Repeat("p", 3000),
	}
	event.Policy = &PolicyInfo{Reason: "api_key=policy-secret"}
	event.Prompt = &PromptInfo{Text: "token=prompt-secret"}
	event.Raw = map[string]interface{}{"nested": map[string]interface{}{"token": "token=raw-secret"}}

	sanitized := SanitizeEvent(event, 64*1024)
	data, err := json.Marshal(sanitized)
	if err != nil {
		t.Fatalf("marshal sanitized event: %v", err)
	}
	text := string(data)
	for _, secret := range []string{"message-secret", "command-secret", "policy-secret", "prompt-secret", "raw-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted: %s", secret, text)
		}
	}
	if len(sanitized.Tool.Path) > DefaultRawStringLimit {
		t.Fatalf("tool path was not truncated: %d", len(sanitized.Tool.Path))
	}
}
