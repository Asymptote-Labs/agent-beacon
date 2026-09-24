// Package secretscan finds secret values in free text: a prompt the developer is
// about to send, or a command and session context the policy hook is about to
// forward to the judge.
//
// It is built for precision, because the prompt gate blocks on any hit with no
// second opinion. Every detector matches a structure specific to a real
// credential (a vendor prefix and length, a PEM body, a JWT whose header decodes,
// a secret-named assignment whose value is long and random), and every candidate
// value passes a placeholder filter. There is deliberately no generic "random
// looking string" detector.
//
// Nothing here returns a secret. A Finding carries a masked excerpt that keeps a
// short prefix and the last four characters, and a fingerprint (a SHA-256
// prefix) so repeats can be correlated without storing the value.
package secretscan

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Finding is one secret found in a text.
type Finding struct {
	// Detector is a stable identifier, e.g. "github_token".
	Detector string
	// Label is the human name used in messages, e.g. "a GitHub token".
	Label string
	// Masked is safe to display and store, e.g. "ghp_…3f2a".
	Masked string
	// Fingerprint is the first 12 hex characters of SHA-256(value).
	Fingerprint string
	// Start and End delimit the secret value within the scanned text.
	Start, End int
}

type detector struct {
	id    string
	label string
	re    *regexp.Regexp
	// group is the submatch holding the secret value (0 = whole match).
	group int
	// prefixKeep is how many leading characters of the value the mask keeps.
	prefixKeep int
	// accept is an extra structural check on the value.
	accept func(value, match string) bool
}

var detectors = []detector{
	{
		id: "github_token", label: "a GitHub token", prefixKeep: 4,
		re:     regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36})\b`),
		group:  1,
		accept: func(v, _ string) bool { return entropy(v[4:]) >= 3.0 },
	},
	{
		id: "github_token", label: "a GitHub token", prefixKeep: 11,
		re:     regexp.MustCompile(`\b(github_pat_[A-Za-z0-9]{22}_[A-Za-z0-9]{59})\b`),
		group:  1,
		accept: func(v, _ string) bool { return entropy(v[11:]) >= 3.0 },
	},
	{
		id: "aws_access_key", label: "an AWS access key", prefixKeep: 4,
		re:     regexp.MustCompile(`\b((?:AKIA|ASIA)[A-Z0-9]{16})\b`),
		group:  1,
		accept: func(v, _ string) bool { return !strings.Contains(v, "EXAMPLE") && entropy(v[4:]) >= 2.5 },
	},
	{
		id: "slack_token", label: "a Slack token", prefixKeep: 5,
		re:    regexp.MustCompile(`\b(xox[abposr]-[0-9]{6,}-[0-9A-Za-z-]{10,})\b`),
		group: 1,
	},
	{
		id: "stripe_key", label: "a Stripe live key", prefixKeep: 8,
		re:    regexp.MustCompile(`\b([sr]k_live_[0-9A-Za-z]{20,})\b`),
		group: 1,
	},
	{
		id: "google_api_key", label: "a Google API key", prefixKeep: 4,
		re:     regexp.MustCompile(`\b(AIza[0-9A-Za-z_\-]{35})\b`),
		group:  1,
		accept: func(v, _ string) bool { return entropy(v[4:]) >= 3.0 },
	},
	{
		id: "openai_key", label: "an OpenAI API key", prefixKeep: 8,
		re:    regexp.MustCompile(`\b(sk-(?:proj|svcacct|admin)-[A-Za-z0-9_\-]{40,}|sk-[A-Za-z0-9]{20}T3BlbkFJ[A-Za-z0-9]{20})\b`),
		group: 1,
	},
	{
		id: "anthropic_key", label: "an Anthropic API key", prefixKeep: 10,
		re:    regexp.MustCompile(`\b(sk-ant-(?:api|admin|oat)\d{2}-[A-Za-z0-9_\-]{60,})`),
		group: 1,
	},
	{
		id: "gitlab_token", label: "a GitLab token", prefixKeep: 6,
		re:    regexp.MustCompile(`\b(glpat-[A-Za-z0-9_\-]{20,})\b`),
		group: 1,
	},
	{
		id: "private_key", label: "a private key", prefixKeep: 0,
		// The header alone is not a key: a prompt can mention it. Require a
		// base64 body line after it.
		re:    regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED |PGP )?PRIVATE KEY(?: BLOCK)?-----\s*(?:[A-Za-z0-9-]+:[^\n]*\n\s*)*([A-Za-z0-9+/=]{40,})`),
		group: 1,
	},
	{
		id: "jwt", label: "a JSON Web Token", prefixKeep: 6,
		re:     regexp.MustCompile(`\b(eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{20,})`),
		group:  1,
		accept: acceptJWT,
	},
	{
		id: "bearer_token", label: "a bearer token", prefixKeep: 0,
		re:     regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{20,})`),
		group:  1,
		accept: func(v, _ string) bool { return entropy(v) >= 3.5 && !isPlaceholder(v) },
	},
	{
		id: "url_credentials", label: "a password in a URL", prefixKeep: 0,
		// Local development databases (loopback, docker, .local hosts) are not
		// shared credentials; Holly's prompts carry sandbox URLs routinely.
		re:     regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@'"]+:([^/\s:@'"]{8,})@([^/\s:'"?#]+)`),
		group:  1,
		accept: acceptURLPassword,
	},
	{
		id: "url_credentials", label: "a secret in a URL", prefixKeep: 0,
		re:     regexp.MustCompile(`(?i)[?&#](?:access_token|token|api_key|apikey|key|secret|auth|sig)=([^&\s#'"]{16,})`),
		group:  1,
		accept: func(v, _ string) bool { return !strings.HasPrefix(v, "$") && !isPlaceholder(v) && entropy(v) >= 3.3 },
	},
	{
		// Share links carry human-chosen passwords, which are shorter and less
		// random than tokens (finding P-1 was a Papermark link with #password=).
		id: "url_credentials", label: "a password in a URL", prefixKeep: 0,
		re:     regexp.MustCompile(`(?i)[?&#](?:password|passwd|pwd|pass)=([^&\s#'"]{6,})`),
		group:  1,
		accept: func(v, _ string) bool { return !strings.HasPrefix(v, "$") && !isPlaceholder(v) && entropy(v) >= 2.3 },
	},
	{
		id: "secret_assignment", label: "a secret value", prefixKeep: 0,
		// KEY=value, KEY: value, "key": "value", and npm/pnpm `…:_authToken=value`
		// shapes, where the key names a secret. The value must look random.
		re:     regexp.MustCompile(`(?i)([A-Za-z0-9_.\-]*(?:token|secret|password|passwd|api_?key|access_?key|private_?key|client_?secret|_auth))["']?\s*[:=]\s*["']?([^\s"'` + "`" + `,;]{12,})`),
		group:  2,
		accept: acceptAssignment,
	},
}

// Scan returns every secret found in text, ordered by position, with
// overlapping findings collapsed to the earliest (then longest).
func Scan(text string) []Finding {
	if text == "" {
		return nil
	}
	var found []Finding
	for _, d := range detectors {
		for _, idx := range d.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := idx[2*d.group], idx[2*d.group+1]
			if start < 0 {
				continue
			}
			value := text[start:end]
			match := text[idx[0]:idx[1]]
			if d.accept != nil && !d.accept(value, match) {
				continue
			}
			found = append(found, Finding{
				Detector:    d.id,
				Label:       d.label,
				Masked:      mask(value, d.prefixKeep, d.id),
				Fingerprint: fingerprint(value),
				Start:       start,
				End:         end,
			})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Start != found[j].Start {
			return found[i].Start < found[j].Start
		}
		return found[i].End > found[j].End
	})
	var out []Finding
	lastEnd := -1
	for _, f := range found {
		if f.Start < lastEnd {
			continue
		}
		out = append(out, f)
		lastEnd = f.End
	}
	return out
}

// Mask returns text with every found secret replaced by its masked form.
func Mask(text string) string {
	findings := Scan(text)
	if len(findings) == 0 {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, f := range findings {
		b.WriteString(text[prev:f.Start])
		b.WriteString(f.Masked)
		prev = f.End
	}
	b.WriteString(text[prev:])
	return b.String()
}

func mask(value string, prefixKeep int, id string) string {
	if id == "private_key" {
		return "[private key]"
	}
	tail := ""
	if len(value) > 12 {
		tail = value[len(value)-4:]
	}
	head := ""
	if prefixKeep > 0 && prefixKeep < len(value) {
		head = value[:prefixKeep]
	}
	return head + "…" + tail
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// entropy is the Shannon entropy of s in bits per character.
func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

var placeholderMarkers = []string{
	"xxxx", "example", "placeholder", "changeme", "change_me", "your_", "your-", "yourtoken",
	"redacted", "dummy", "sample", "fake", "<", ">", "****", "...", "…", "todo", "insert",
	"replace", "secret_here", "token_here", "abcdef", "123456", "password", "passwd",
}

func isPlaceholder(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range placeholderMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func acceptAssignment(value, _ string) bool {
	if strings.HasPrefix(value, "$") || strings.HasPrefix(value, "%") || strings.HasPrefix(value, "~") {
		return false
	}
	// Code, not a value: calls, member access, indexing, templates, paths, URLs.
	if strings.ContainsAny(value, ".()[]{}<>/\\") {
		return false
	}
	if isPlaceholder(value) {
		return false
	}
	// Identifiers and words are not secrets. A real token or password almost
	// always mixes letters and digits (a random 40-character base62 string has
	// no digit about once in a thousand), and must look random.
	if !strings.ContainsAny(value, "0123456789") || !strings.ContainsAny(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return false
	}
	return entropy(value) >= 3.2
}

var localHost = regexp.MustCompile(`(?i)^(?:localhost|127\.\d+\.\d+\.\d+|0\.0\.0\.0|\[?::1\]?|host\.docker\.internal|[a-z0-9-]+\.local|[a-z0-9_-]+)$`)

func acceptURLPassword(value, match string) bool {
	if strings.HasPrefix(value, "$") || isPlaceholder(value) || entropy(value) < 2.5 {
		return false
	}
	at := strings.LastIndex(match, "@")
	host := match[at+1:]
	// A bare single-label host (postgres, db, redis) is a compose service name.
	return !localHost.MatchString(host)
}

// acceptJWT requires a header that decodes to JSON naming an algorithm, and
// skips tokens that are public by design (Supabase anon keys) or the well-known
// jwt.io sample.
func acceptJWT(value, _ string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	header, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[0], "="))
	if err != nil {
		return false
	}
	var h map[string]interface{}
	if json.Unmarshal(header, &h) != nil || h["alg"] == nil {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err == nil {
		var p map[string]interface{}
		if json.Unmarshal(payload, &p) == nil {
			if role, _ := p["role"].(string); role == "anon" {
				return false
			}
			if sub, _ := p["sub"].(string); sub == "1234567890" {
				return false
			}
		}
	}
	return true
}
