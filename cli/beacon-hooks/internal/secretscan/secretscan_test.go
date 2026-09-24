package secretscan

import (
	"strings"
	"testing"
)

// Canaries: structurally valid, never real. Built at runtime so the source
// never contains a literal that a secret scanner (or this one) would flag.
func canary(prefix string, n int, alphabet string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

const (
	alnum = "aZ3kQ9mX2pL7vR4tB8nC1wE6yU5sD0fG"

	upper = "Q7W3E9R2T5Y8U4I6O1P0ASDFGHJKLZXC"
)

var tiptapToken = canary("", 64, "9f3a1c7e5b2d8046")

func TestDetectsEachSecretShape(t *testing.T) {
	cases := map[string]string{
		"github_token":      "here is my token " + canary("ghp_", 36, alnum) + " can you set it up",
		"aws_access_key":    "aws key " + canary("AKIA", 16, upper),
		"slack_token":       "slack " + "xoxb-" + "1234567890-" + canary("", 24, alnum),
		"stripe_key":        "stripe " + canary("sk_live_", 28, alnum),
		"google_api_key":    "gmaps " + canary("AIza", 35, alnum),
		"openai_key":        "openai " + canary("sk-proj-", 48, alnum),
		"anthropic_key":     "claude " + canary("sk-ant-api03-", 90, alnum),
		"gitlab_token":      "gitlab " + canary("glpat-", 20, alnum),
		"private_key":       "-----BEGIN OPENSSH PRIVATE KEY-----\n" + canary("", 70, alnum+"+/") + "\n-----END OPENSSH PRIVATE KEY-----",
		"jwt":               "token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb2xlIjoic2VydmljZV9yb2xlIiwiaXNzIjoic3VwYWJhc2UifQ." + canary("", 43, alnum),
		"bearer_token":      `curl -H "Authorization: Bearer ` + canary("", 40, alnum) + `" https://api.example.com`,
		"url_credentials":   "psql postgres://admin:" + canary("", 18, alnum) + "@db.prod.holly.internal:5432/app",
		"secret_assignment": "TIPTAP_PRO_TOKEN=" + tiptapToken,
	}
	for want, text := range cases {
		found := Scan(text)
		if len(found) != 1 || found[0].Detector != want {
			t.Errorf("%s: got %+v", want, found)
			continue
		}
		f := found[0]
		if strings.Contains(f.Masked, text[f.Start:f.End]) || len(f.Fingerprint) != 12 {
			t.Errorf("%s: mask or fingerprint leaks the value: %+v", want, f)
		}
	}
}

func TestDetectsTheTiptapTokenInItsRealShapes(t *testing.T) {
	for _, text := range []string{
		"//registry.tiptap.dev/:_authToken=" + tiptapToken,
		"'//registry.tiptap.dev/:_authToken': " + tiptapToken,
		"here's the npmrc:\n@tiptap-pro:registry=https://registry.tiptap.dev/\n//registry.tiptap.dev/:_authToken=" + tiptapToken,
		"export TIPTAP_PRO_TOKEN=\"" + tiptapToken + "\"",
	} {
		if found := Scan(text); len(found) != 1 {
			t.Errorf("want one finding in %q, got %+v", text[:40], found)
		}
	}
}

func TestDetectsAShareLinkPassword(t *testing.T) {
	// The shape of finding P-1: a Papermark auto-login link.
	text := "log in with https://www.papermark.com/view/cm1abc2def#password=Hv7qLm2x"
	found := Scan(text)
	if len(found) != 1 || found[0].Detector != "url_credentials" {
		t.Fatalf("got %+v", found)
	}
}

func TestIgnoresPlaceholdersAndCode(t *testing.T) {
	for _, text := range []string{
		"set GITHUB_TOKEN=<your-token> in .env",
		"GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"TIPTAP_PRO_TOKEN=$(secret get TIPTAP_PRO_TOKEN) pnpm install",
		"TIPTAP_PRO_TOKEN=${TIPTAP_PRO_TOKEN}",
		"DATABASE_PASSWORD=[REDACTED] docker compose config",
		"DATABASE_USER=holly DATABASE_PASSWORD=holly docker compose up",
		"const token = getTokenFromHeader(request)",
		"token = response.data.access_token",
		"accessToken: process.env.GITHUB_ACCESS_TOKEN",
		"api_key: YOUR_API_KEY_HERE",
		"password: changeme123",
		"AKIAIOSFODNN7EXAMPLE",
		"Authorization: Bearer ${TOKEN}",
		"postgres://holly:holly@localhost:5432/holly",
		"env DATABASE_URL='postgresql://postgres:Pg5595sandbox@localhost:5595/holly_sandbox_city'",
		"redis://default:Rd7x9sandbox@redis:6379/0",
		"mysql://root:My8secretpw@host.docker.internal:3306/app",
		"https://x-access-token:${GITHUB_TOKEN}@github.com/org/repo",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		"the -----BEGIN RSA PRIVATE KEY----- header marks a PEM file",
		"sk_test_" + "4eC39HqLyjWDarjtT1zdp7dc",
		"refresh_token_expires_in: 7776000",
		"session_token_ttl=3600000000000",
		"authTokenStorageKey: tiptapCollabAuthTokenV2",
		"password_reset_url=https://app.holly.gov/reset",
	} {
		if found := Scan(text); len(found) != 0 {
			t.Errorf("false positive in %q: %+v", text, found)
		}
	}
}

func TestSimonsP45PromptsPass(t *testing.T) {
	for _, text := range []string{
		"the new mac needs a .env and the tiptap token to install properly. any idea sthere?",
		"am i using the mac cross copy feature here? iddi hte first command bu idk about hte second",
		"pnpm config get '//registry.tiptap.dev/:_authToken' | tr -d '\\n' | echo\n\nnpm error The //registry.tiptap.dev/:_authToken option is protected, and cannot be retrieved in this way\nnpm error A complete log of this run can be found in: /Users/simonbukin/.npm/_logs/2026-09-22T21_58_08_935Z-debug-0.log",
		"omfg just print it i do not care",
	} {
		if found := Scan(text); len(found) != 0 {
			t.Errorf("false positive in %q: %+v", text, found)
		}
	}
}

func TestMaskReplacesEverySecret(t *testing.T) {
	a := canary("ghp_", 36, alnum)
	b := "TIPTAP_PRO_TOKEN=" + tiptapToken
	out := Mask("first " + a + " then " + b + " done")
	if strings.Contains(out, a) || strings.Contains(out, tiptapToken) {
		t.Fatalf("secret survived masking: %s", out)
	}
	if !strings.HasPrefix(out, "first ghp_…") || !strings.Contains(out, "TIPTAP_PRO_TOKEN=…") || !strings.HasSuffix(out, " done") {
		t.Fatalf("unexpected mask output: %s", out)
	}
}
