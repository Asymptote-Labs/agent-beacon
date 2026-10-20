package asymptoteobserve

import (
	"math/rand"
	"strings"
	"testing"
)

// redactGateFragments are the pieces the randomized gate test builds text from: every rule's
// needles and the text around them, near misses, and the case-folding edge cases.
var redactGateFragments = []string{
	"token", "Token", "TOKEN", "tokens", "input_tokens", "api_key", "API-KEY", "apikey", "access_key",
	"secret", "client_secret", "secret_key", "private-key", "password", "PassWord", "passwd",
	"passphrase", "authorization", "Authorization", "credential", "credentials", "keyboard", "key",
	"GITHUB_TOKEN", "export ", "--token", "--db-password", "-x-token", "a-token", "9", "_", "-password", "--api-key", "--access-key", "-x", "--", "-",
	"=", ":", " ", "  ", "\t", "\n", "\r", ",", `"`, "'", "`", `\`, `\"`, `\\\"`, "{", "}", "[", "]",
	"Bearer ", "bearer", "Basic ", "Digest ", "eyJ", "eyJhbGciOiJIUzI1NiJ9", ".", "abcdefgh12345678",
	"hunter2hunter2", "null", "true", "42", "https://", "postgres://user:", "pw", "@host", "://",
	"sk-", "sk-ant-api03-", "AKIA", "ASIA", "ABCDEFGHIJKLMNOP", "ghp_", "gho_", "ghs_", "github_pat_",
	"glpat-", "xoxb-", "sk_live_", "rk_test_", "AIza", "npm_", "hf_", "bcn_api_", "bcn_device_",
	"bcn_cli_", "bcn_mcprt_", "bcn_code_", "bcn_client_", "bcn_exchange_", "bcn_mcp_", "bcn_enfdev_", "bcn_other_",
	"AKIAIOSFODNN7EXAMPLE", "ASIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "|", "/", "+",
	"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ", "-----BEGIN RSA PRIVATE KEY-----",
	"-----BEGIN PRIVATE KEY-----", "-----END RSA PRIVATE KEY-----", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
	"Proc-Type: 4,ENCRYPTED", "\\n", "[REDACTED]", "ſecret", "toKen", "Key", "İ", "ß", "é", "日本",
}

func randomRedactInput(r *rand.Rand) string {
	var b strings.Builder
	for n := r.Intn(12); n >= 0; n-- {
		b.WriteString(redactGateFragments[r.Intn(len(redactGateFragments))])
	}
	return b.String()
}

// TestRedactGateNeverChangesAResult proves the gate is only a shortcut: every text redacts exactly
// as it does when every rule runs.
func TestRedactGateNeverChangesAResult(t *testing.T) {
	inputs := []string{
		"", "info", "token=abcdefgh12345678", `{"api_key": "x y z"}`, `"password": 42`,
		"export GITHUB_TOKEN=ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk'",
		"mysql --password hunter2 -u root", "postgres://user:pw@host/db", "input_tokens: 812",
		"ſecret=abcdefgh12345678", "KEY=abcdefgh12345678", "toKen\\\"=\"abc\"",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END RSA PRIVATE KEY-----",
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		inputs = append(inputs, randomRedactInput(r))
	}
	redacted := 0
	for _, input := range inputs {
		gated, all := redactString(input, true), redactString(input, false)
		if gated != all {
			t.Fatalf("gated redaction of %q = %q, every rule gives %q", input, gated, all)
		}
		if gated != input {
			redacted++
		}
	}
	// The corpus has to reach the rules for the comparison to mean anything.
	if redacted < len(inputs)/10 {
		t.Fatalf("only %d of %d inputs were redacted at all", redacted, len(inputs))
	}
}

func FuzzRedactGate(f *testing.F) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 64; i++ {
		f.Add(randomRedactInput(r))
	}
	f.Fuzz(func(t *testing.T, input string) {
		if gated, all := redactString(input, true), redactString(input, false); gated != all {
			t.Fatalf("gated redaction of %q = %q, every rule gives %q", input, gated, all)
		}
	})
}
