package asymptoteobserve

import (
	"strings"
	"testing"
)

// A private key block whose lines carry a prefix is redacted: a diff deleting a key file, `cat -n`
// and `grep -n` output, a key quoted in markdown or mail, and the same output JSON-encoded. Only a
// diff's added lines were, and only because `+` is a base64 character (#1374).
func TestRedactStringRedactsAPrivateKeyWhoseLinesCarryAPrefix(t *testing.T) {
	const body = "MIIEowIBAAKCAQEAsecretbodyAbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCD"
	block := func(prefix string) string {
		return prefix + "-----BEGIN RSA PRIVATE KEY-----\n" + prefix + body + "\n" + prefix + body + "\n" + prefix + "-----END RSA PRIVATE KEY-----\n"
	}
	numbered := func(sep string) string {
		return "1" + sep + "-----BEGIN RSA PRIVATE KEY-----\n2" + sep + body + "\n3" + sep + body + "\n4" + sep + "-----END RSA PRIVATE KEY-----\n"
	}
	tests := []struct{ name, input string }{
		{"diff deleting a key file", "--- a/deploy/key.pem\n+++ /dev/null\n@@ -1,4 +0,0 @@\n" + block("-")},
		{"diff adding a key file", "--- /dev/null\n+++ b/deploy/key.pem\n@@ -0,0 +1,4 @@\n" + block("+")},
		{"cat -n", "$ cat -n deploy/key.pem\n" + strings.ReplaceAll(numbered("\t"), "\n", "\n     ")},
		{"grep -n", numbered(":")},
		{"quoted with >", block("> ")},
		{"no prefix", block("")},
		{"cat -n JSON-encoded", `{"output":"     1\t-----BEGIN RSA PRIVATE KEY-----\n     2\t` + body + `\n     3\t-----END RSA PRIVATE KEY-----\n","exit":0}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactString(tc.input)
			if strings.Contains(got, "secretbody") {
				t.Errorf("key material was not redacted:\n%s", got)
			}
			if again := RedactString(got); again != got {
				t.Errorf("redaction is not idempotent:\n%q\n%q", got, again)
			}
		})
	}
	escaped := tests[len(tests)-1].input
	if got := RedactString(escaped); !strings.HasSuffix(got, `","exit":0}`) {
		t.Errorf("JSON-encoded output lost the text after the key: %q", got)
	}

	// The key in a deleted file's diff reaches the log through file.diff.
	event := NewEvent(NewEventOptions{Action: "file.modified", Harness: HarnessInfo{Name: "claude_code"}})
	event.File = &FileInfo{Path: "deploy/key.pem", Operation: "delete", Diff: tests[0].input}
	if got := SanitizeEvent(event, 64*1024).File.Diff; strings.Contains(got, "secretbody") {
		t.Errorf("SanitizeEvent kept the deleted key in file.diff:\n%s", got)
	}
}

// A line prefix does not make text that only names a key header into a key: the lines after a
// header in source code, or in grep output over several files, are kept.
func TestRedactStringKeyLinePrefixNeedsKeyMaterial(t *testing.T) {
	for _, text := range []string{
		"-const pemHeader = \"-----BEGIN RSA PRIVATE KEY-----\"\n+const pemHeader = \"-----BEGIN PRIVATE KEY-----\"\n",
		"src/keys.go:12:\t// -----BEGIN RSA PRIVATE KEY-----\nsrc/keys.go:13:\tfunc parseKeyMaterialFromTheFile() {}",
		"> -----BEGIN RSA PRIVATE KEY-----\n> (key elided)\n> -----END RSA PRIVATE KEY-----\n",
	} {
		if got := RedactString(text); got != text {
			t.Errorf("RedactString(%q) = %q, want it unchanged", text, got)
		}
	}
}

// grep's context lines (`-A`, `-B`, `-C`, and rg's) number a line with `-`, not `:` (#1375 review).
func TestRedactStringRedactsAPrivateKeyInGrepContextLines(t *testing.T) {
	const body = "MIIEowIBAAKCAQEAsecretbodyAbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCD"
	input := "1:-----BEGIN RSA PRIVATE KEY-----\n2-" + body + "\n3-" + body + "\n4------END RSA PRIVATE KEY-----\n"
	if got := RedactString(input); strings.Contains(got, "secretbody") {
		t.Errorf("key material was not redacted:\n%s", got)
	}
}
