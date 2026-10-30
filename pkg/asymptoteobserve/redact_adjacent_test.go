package asymptoteobserve

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

const (
	adjacentGitHub = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	adjacentGoogle = "AIzaSyA-abcdefghijklmnopqrstuvwxyz01234"
	adjacentNPM    = "npm_abcdefghijklmnopqrstuvwxyz0123456789"
	adjacentJWT    = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk"
)

// adjacentCases are secrets written right after another secret, or right after text a pass
// replaces, each in the shape it is written in. A pass of the rules used to leave the second
// secret, or part of it.
var adjacentCases = []struct{ name, input, want string }{
	{"two Google keys", adjacentGoogle + adjacentGoogle, "AIza[REDACTED]AIza[REDACTED]"},
	{"two npm tokens", adjacentNPM + adjacentNPM, "npm_[REDACTED]npm_[REDACTED]"},
	// The first token's pattern takes the second's `ghp` and stops at its `_`.
	{"two GitHub tokens", adjacentGitHub + adjacentGitHub, "ghp_[REDACTED]ghp_[REDACTED]"},
	{"two short GitHub tokens", "ghp_abcdefghijklmnopqrstuvwxyz0123ghp_abcdefghijklmnopqrstuvwxyz0123", "ghp_[REDACTED]ghp_[REDACTED]"},
	{"two Hugging Face tokens", "hf_abcdefghijklmnopqrstuvwxyz01234567hf_abcdefghijklmnopqrstuvwxyz01234567", "hf_[REDACTED]hf_[REDACTED]"},
	{"two Stripe keys", "sk_live_abcdefghijklmnopsk_live_abcdefghijklmnop", "sk_live_[REDACTED]sk_live_[REDACTED]"},
	{"a Slack token into a GitHub token", "xoxb-1234567890-abc" + adjacentGitHub, "xoxb-[REDACTED]ghp_[REDACTED]"},
	{"a GitHub token into a Google key", adjacentGitHub + adjacentGoogle, "ghp_[REDACTED]AIza[REDACTED]"},
	{"a GitHub token into a JWT", adjacentGitHub + adjacentJWT, "ghp_[REDACTED]eyJ[REDACTED]"},
	{"a JWT into a JWT", adjacentJWT + adjacentJWT, "eyJ[REDACTED]eyJ[REDACTED]"},
	{"an API key into an OAuth token", "sk-abcdefghijklmnopqrst-ya29.abcdefghijklmnopqrstuvwxyz", "sk-[REDACTED]ya29.[REDACTED]"},
	// The key's pattern takes the JWT's first segment and stops at its dot, too short a key
	// without it: the two are redacted as one.
	{"an API key into a JWT it cannot be cut before", "sk-" + adjacentJWT, "sk-[REDACTED]"},
	{"curl cookies after an npm token", adjacentNPM + "curl -b 'a=1' -b 'b=2'", "npm_[REDACTED]curl -b '[REDACTED]' -b '[REDACTED]'"},
	// A token's pattern runs on over a URL's scheme, `Bearer` or `curl`: the token is cut before
	// it, and the follower is redacted.
	{"a URL password after a GitHub token", adjacentGitHub + "postgres://user:pw@host/db", "ghp_[REDACTED]s://user:[REDACTED]@host/db"},
	{"a URL password after a JWT", adjacentJWT + "postgres://user:pw@host/db", "eyJ[REDACTED]s://user:[REDACTED]@host/db"},
	{"a Bearer token after a Hugging Face token", "hf_abcdefghijklmnopqrstuvwxyz01234567Bearer abcdefghijk", "hf_[REDACTED]Bearer [REDACTED]"},
	{"a Bearer token after a Slack token", "xoxb-1234567890-abcBearer abcdefghijk", "xoxb-[REDACTED]Bearer [REDACTED]"},
	{"a curl password after an API key", "sk-abcdefghijklmnopqrstuvwxyz0123curl -u admin:hunter2 https://x", "sk-[REDACTED]curl -u admin:[REDACTED] https://x"},
	{"a curl password after a GitLab token", "glpat-abcdefghijklmnopqrstuvcurl -u admin:hunter2", "glpat-[REDACTED]curl -u admin:[REDACTED]"},
	{"a URL password after an AWS key id", "AKIAIOSFODNN7EXAMPLEpostgres://user:pw@host/db", "AKIA[REDACTED]postgres://user:[REDACTED]@host/db"},
	{"a Bearer token after an AWS key id", "AKIAIOSFODNN7EXAMPLEBearer abcdefghijk", "AKIA[REDACTED]Bearer [REDACTED]"},
	{"a curl password after an AWS key id", "AKIAIOSFODNN7EXAMPLEcurl -u admin:hunter2", "AKIA[REDACTED]curl -u admin:[REDACTED]"},
	// When the key id rule asks whether a curl command follows the key id, the rule for a key id
	// and its secret has already asked about the line from its later `curl`, which has none.
	{"a curl password after a Google key and an AWS key id", adjacentGoogle + "AKIAIOSFODNN7EXAMPLEcurl -u a:curl -u a:", "AIza[REDACTED]AKIA[REDACTED]curl -u a:[REDACTED] -u a:"},
	// A `curl` a word byte follows (`curl_x`) is no command, and says nothing of a later one.
	{"a curl password after a token, past a glued curl_", adjacentGitHub + "curl_x " + adjacentGitHub + "curl -u admin:hunter2", "ghp_[REDACTED]_x ghp_[REDACTED]curl -u admin:[REDACTED]"},
	// The flag starts the line after the command, and its value is on the line after that.
	{"a curl flag alone on the line after a key", adjacentNPM + "curl x\n-u\na:hunter2", "npm_[REDACTED]curl x\n-u\na:[REDACTED]"},
	{"a Google key, then a GitHub token", adjacentGoogle + adjacentGitHub, "AIza[REDACTED]ghp_[REDACTED]"},
	{"npm tokens and Google keys in turn", adjacentNPM + adjacentGoogle + adjacentNPM + adjacentGoogle, "npm_[REDACTED]AIza[REDACTED]npm_[REDACTED]AIza[REDACTED]"},
	// The key id's pattern ends in \b, which a token right after it takes away.
	{"an AWS key id, then a GitHub token", "AKIAIOSFODNN7EXAMPLE" + adjacentGitHub, "AKIA[REDACTED]ghp_[REDACTED]"},
	{"an AWS key id, then a Google key", "AKIAIOSFODNN7EXAMPLE" + adjacentGoogle, "AKIA[REDACTED]AIza[REDACTED]"},
	{"an AWS secret, then a GitHub token", "AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" + adjacentGitHub, "AKIA[REDACTED],[REDACTED]ghp_[REDACTED]"},
	{"a GitHub token, then an AWS key id and secret", adjacentGitHub + "AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n", "ghp_[REDACTED]AKIA[REDACTED],[REDACTED]\n"},
	// The rules for a URL's password, a Bearer token and a curl password run before the shape
	// rules, while the key before them is still there.
	{"a URL password after an npm token", adjacentNPM + "postgres://user:pw@host/db", "npm_[REDACTED]postgres://user:[REDACTED]@host/db"},
	{"a Bearer token after an npm token", adjacentNPM + "Bearer abcdefghijk", "npm_[REDACTED]Bearer [REDACTED]"},
	{"a curl password after an npm token", adjacentNPM + "curl -u admin:hunter2 https://x", "npm_[REDACTED]curl -u admin:[REDACTED] https://x"},
	{"curl passwords after npm tokens", adjacentNPM + "curl -u a:b1 " + adjacentNPM + "curl -u c:d2", "npm_[REDACTED]curl -u a:[REDACTED] npm_[REDACTED]curl -u c:[REDACTED]"},
	{"a URL password after a Google key, then a token", adjacentGoogle + "https://x:pw@h/" + adjacentNPM, "AIza[REDACTED]https://x:[REDACTED]@h/npm_[REDACTED]"},
	// A bare copy of an assigned value is replaced after every rule has run.
	{"a GitHub token after a copy of an assigned value", "token=abcdefgh12345678 abcdefgh12345678" + adjacentGitHub, "token=[REDACTED] [REDACTED]ghp_[REDACTED]"},
	{"a URL password after a copy of an assigned value", "token=abcdefgh12345678 abcdefgh12345678redis://:pw@h", "token=[REDACTED] [REDACTED]redis://:[REDACTED]@h"},
	// The quoted-label rule keeps `null`, the capture of the value runs on past it, and the rule
	// for `x-access-token:` then replaces the token inside what was captured.
	{"a value changed after it was captured", `"secret": null}bcn_code_--https://x-access-token:ghs_abcdefghijklmnopqrstuvwxyz0123@github.com/o/r`, `"secret": [REDACTED]`},
	// A token's pattern takes the first bytes of a copy of an assigned value written right after
	// it, and the copy, removed only whole, was left from there on: the item runs on to its end.
	{"a copy of an assigned value after a token", "hf_abcdefghijklmnopqrstuvwxyz01234567opaque-flag-value tool login --token=opaque-flag-value", "hf_[REDACTED] tool login --token=[REDACTED]"},
}

func TestRedactStringRedactsSecretsWrittenRightAfterAnother(t *testing.T) {
	for _, tc := range adjacentCases {
		if got := RedactString(tc.input); got != tc.want {
			t.Errorf("%s: RedactString(%q) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
		if got := redactString(tc.input, false); got != tc.want {
			t.Errorf("%s: with every rule's whole pattern, %q, want %q", tc.name, got, tc.want)
		}
		withBudget(t, 0, -1, func() {
			if got := RedactString(tc.input); got != tc.want {
				t.Errorf("%s: from safe starts, %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// Text where a secret only ends next to other text, or is followed by something shaped like one
// that is not, redacts as before.
func TestRedactStringKeepsWhatFollowsASecretWhenItIsNone(t *testing.T) {
	for input, want := range map[string]string{
		adjacentGoogle + "xyz":                       "AIza[REDACTED]xyz",
		adjacentNPM + "postgres":                     "npm_[REDACTED]postgres",
		adjacentNPM + "ghp_short":                    "npm_[REDACTED]ghp_short",
		"AKIAIOSFODNN7EXAMPLExoxz":                   "AKIAIOSFODNN7EXAMPLExoxz",
		"AKIAIOSFODNN7EXAMPLEghp_short":              "AKIAIOSFODNN7EXAMPLEghp_short",
		adjacentGitHub + "_suffix":                   "ghp_[REDACTED]_suffix",
		"x" + adjacentGitHub:                         "x" + adjacentGitHub,
		"token=abcdefgh12345678 abcdefgh12345678xyz": "token=[REDACTED] [REDACTED]xyz",
		"[REDACTED]Bearer":                           "[REDACTED]Bearer",
		// A URL whose user name only ends in a word a label holds keeps its host and path; one
		// that is a label (api_key) is redacted as far as the label rules take it.
		"https://deploykey:pw@host/repo": "https://deploykey:[REDACTED]@host/repo",
		"https://key:pw@host/repo":       "https://key:[REDACTED]@host/repo",
		"https://keys:pw@host/repo":      "https://keys:[REDACTED]@host/repo",
	} {
		if got := RedactString(input); got != want {
			t.Errorf("RedactString(%q) = %q, want %q", input, got, want)
		}
	}
}

// A URL's password that runs on as the label rules read it (urlRunsOn) reads an AWS key id and
// its secret as the shape rule for the pair leaves them: the run that takes the key id takes the
// secret too, and the pair does not stop the password where the secret's `/` would.
func TestRedactStringRunsAURLPasswordOnOverAnAWSKeyAndSecret(t *testing.T) {
	const pair = "AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	for input, want := range map[string]string{
		"A://token:,@0,@0,@" + pair + "*S-1":                                            "A://token:[REDACTED]",
		"A://token:,@AKIAIOSFODNN7EXAMPLE:,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY x":  "A://token:[REDACTED] x",
		"A://token:,@0," + pair + "@h":                                                  "A://token:[REDACTED]",
		"A://token:,@0,AKIAIOSFODNN7EXAMPLE wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY@h": "A://token:[REDACTED],AKIA[REDACTED] [REDACTED]@h",
	} {
		got := RedactString(input)
		if got != want {
			t.Errorf("RedactString(%q) = %q, want %q", input, got, want)
		}
		if again := RedactString(got); again != got {
			t.Errorf("RedactString(%q) = %q, and again %q", input, got, again)
		}
	}
}

// One pass leaves nothing for a second one: events are sanitized again on every read path, and a
// derived field that redacts text a writer already redacted must find nothing new.
func FuzzRedactIsIdempotent(f *testing.F) {
	for _, tc := range adjacentCases {
		f.Add(tc.input)
	}
	for _, input := range redactIdempotenceInputs {
		f.Add(input)
	}
	r := rand.New(rand.NewPCG(5, 5))
	for range 64 {
		f.Add(randomAdjacentInput(r))
	}
	f.Fuzz(func(t *testing.T, input string) {
		once := RedactString(input)
		if twice := RedactString(once); twice != once {
			t.Fatalf("RedactString is not idempotent on %q:\nonce  %q\ntwice %q", input, once, twice)
		}
		if all := redactString(input, false); all != once {
			t.Fatalf("redaction of %q = %q, every rule's whole pattern gives %q", input, once, all)
		}
	})
}

// randomAdjacentInput is randomRedactInput's fragments with whole secrets among them, so they
// often meet.
func randomAdjacentInput(r *rand.Rand) string {
	var b strings.Builder
	for n := r.IntN(8); n >= 0; n-- {
		if r.IntN(3) == 0 {
			b.WriteString(adjacentSecrets[r.IntN(len(adjacentSecrets))])
		} else {
			b.WriteString(redactGateFragments[r.IntN(len(redactGateFragments))])
		}
	}
	return b.String()
}

var adjacentSecrets = []string{
	adjacentGitHub, adjacentGoogle, adjacentNPM, adjacentJWT,
	"ghs_abcdefghijklmnopqrstuvwxyz0123", "hf_abcdefghijklmnopqrstuvwxyz01234567",
	"sk-abcdefghijklmnopqrstuvwxyz0123", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz", "AKIAIOSFODNN7EXAMPLE",
	"AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "github_pat_abcdefghijklmnopqrstuvwxyz",
	"glpat-abcdefghijklmnopqrstuv", "xoxb-1234567890-abc", "sk_live_abcdefghijklmnop", "ya29.abcdefghijklmnopqrstuvwxyz",
	"bcn_api_abcdefgh", "postgres://user:pw@host/db", "https://x-access-token:abc@github.com", "token=abcdefgh12345678",
	"Bearer abcdefghijk", "curl -u admin:hunter2 ", "abcdefgh12345678",
}

// Idempotence and the gate's equivalence over many random texts where secrets meet, beyond what
// the fuzz corpus holds, every fourth also from safe starts.
func TestRedactIsIdempotentWhereSecretsMeet(t *testing.T) {
	backToBack := regexp.MustCompile(`\[REDACTED\][A-Za-z0-9_.-]{1,16}\[REDACTED\]`)
	r := rand.New(rand.NewPCG(6, 6))
	met := 0
	for i := range 10000 {
		input := randomAdjacentInput(r)
		once := RedactString(input)
		if twice := RedactString(once); twice != once {
			t.Fatalf("RedactString is not idempotent on %q:\nonce  %q\ntwice %q", input, once, twice)
		}
		if all := redactString(input, false); all != once {
			t.Fatalf("redaction of %q = %q, every rule's whole pattern gives %q", input, once, all)
		}
		if i%4 == 0 {
			withBudget(t, 0, -1, func() {
				if got := RedactString(input); got != once {
					t.Fatalf("redaction of %q from safe starts = %q, want %q", input, got, once)
				}
			})
		}
		if backToBack.MatchString(once) {
			met++
		}
	}
	// The texts have to put secrets next to each other for the check to mean anything.
	if met < 600 {
		t.Fatalf("only %d texts redacted a secret right after another", met)
	}
}
