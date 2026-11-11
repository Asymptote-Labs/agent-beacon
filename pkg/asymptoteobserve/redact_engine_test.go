package asymptoteobserve

import (
	"fmt"
	mrand "math/rand"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The needle scanner must report every occurrence of every needle, overlapping ones included,
// folded needles whatever the case of the text's ASCII letters, on text short enough to be read in
// one lane and long enough to be read in several, with occurrences across the lanes' edges.
func TestNeedleScannerFindsEveryOccurrence(t *testing.T) {
	sc := &redactEngineFor().scanner
	want := func(text string) []redactHit {
		var hits []redactHit
		for start := range len(text) {
			for id, nd := range sc.needles {
				end := start + len(nd.text)
				if end > len(text) {
					continue
				}
				if nd.folded && strings.EqualFold(text[start:end], nd.text) && isASCII(text[start:end]) || !nd.folded && text[start:end] == nd.text {
					hits = append(hits, redactHit{start: start, end: end, id: id})
				}
			}
		}
		return hits
	}
	pieces := []string{" ", "\n", "x", "_", "-", "=", "://", "\u017f", "\u212a", "é", "\xff"}
	for _, nd := range sc.needles {
		pieces = append(pieces, nd.text, strings.ToUpper(nd.text), nd.text[:len(nd.text)/2])
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 3000 {
		var b strings.Builder
		n := 1 + r.IntN(8)
		if i%3 == 0 {
			n = 60 + r.IntN(200)
		}
		for range n {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		text := b.String()
		var found idSet
		got := sc.scan(text, nil, &found)
		expected := want(text)
		key := func(a, b redactHit) int {
			if a.start != b.start {
				return a.start - b.start
			}
			return a.id - b.id
		}
		slices.SortFunc(got, key)
		slices.SortFunc(expected, key)
		if !slices.Equal(got, expected) {
			t.Fatalf("scan(%q)\n = %v\nwant %v", text, got, expected)
		}
		var mask idSet
		for _, h := range expected {
			mask.add(h.id)
		}
		if found != mask {
			t.Fatalf("scan(%q) found %x, want %x", text, found, mask)
		}
	}
}

// The rules use fewer needles and sets than one word of an idSet holds, so nothing else reaches
// the ids past 63. A scanner over idSetCap needles must report each one, in either word, and the
// set of those found must hold exactly them.
func TestNeedleScannerReportsIDsInEveryWord(t *testing.T) {
	var sc needleScanner
	needles := make([]redactNeedle, idSetCap)
	for id := range needles {
		needles[id] = redactNeedle{text: fmt.Sprintf("<n%d>", id)}
	}
	sc.build(needles)
	for _, ids := range [][]int{{0}, {63}, {64}, {idSetCap - 1}, {1, 63, 64, 65, idSetCap - 1}} {
		var text strings.Builder
		var want idSet
		for _, id := range ids {
			text.WriteString("x " + needles[id].text)
			want.add(id)
		}
		var found idSet
		hits := sc.scan(text.String(), nil, &found)
		var got []int
		for _, h := range hits {
			got = append(got, h.id)
		}
		if !slices.Equal(got, ids) || found != want {
			t.Errorf("scan(%q): ids %v, found %x; want ids %v, found %x", text.String(), got, found, ids, want)
		}
		for id := range idSetCap {
			if found.has(id) != slices.Contains(ids, id) {
				t.Errorf("found.has(%d) = %t for ids %v", id, found.has(id), ids)
			}
		}
	}
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Every rule opens where its start says it does: running a rule's whole pattern over text built
// around its needles, running it anchored where its start names, and running it whole from the safe
// start before the first of those places (what it does past its budget), find the same matches.
// TestRedactGateNeverChangesAResult checks the same through RedactString; this names the rule.
func TestEveryRuleMatchesAsItsWholePattern(t *testing.T) {
	engine := redactEngineFor()
	fragments := []string{" ", "\n", "\t", "=", ": ", `"`, "'", "`", `\"`, ",", "x", "-", "--", "_", ".", "@", "/",
		"abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", "[REDACTED]", "eyJ", "set-", "-H ", "curl ", "-u ", "-b ",
		"token=abcdefgh1234", `"api_key": "x y"`, "'secret': 42", "--password hunter2", `["--api-key", "k"]`,
		"curl -u admin:pw https://x", "curl -b 'sid=1' x", "Cookie: sid=1", "postgres://u:p@h", "redis://:p@h",
		"Bearer abc.def", "authorization: basic x", "ghp_abcdefghijklmnopqrstuvwxyz0123", "AKIAIOSFODNN7EXAMPLE",
		"sk-abcdefghijklmnopqrstuvwxyz", "sk_live_abcdefghijklmnop", "ya29.abcdefghijklmnopqrstuvwxyz",
		"eyJabcdefghij.eyJabcdefghij.abcdefghij", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAabcdefghijklmnop\n"}
	for _, set := range []*redactNeedles{redactLabelNeedles, redactFlagNeedles} {
		fragments = append(fragments, set.needles...)
	}
	for _, rule := range redactionRules {
		fragments = append(fragments, rule.needles.needles...)
	}
	r := rand.New(rand.NewPCG(3, 4))
	matched := 0
	for i, rule := range redactionRules {
		// The fragments the rule's pattern matches on their own, and its needles, are drawn often.
		near := slices.Clone(rule.needles.needles)
		for _, f := range fragments {
			if rule.pattern().MatchString(f) {
				near = append(near, f)
			}
		}
		for range 400 {
			var b strings.Builder
			for range 1 + r.IntN(10) {
				f := fragments[r.IntN(len(fragments))]
				if r.IntN(2) == 0 {
					f = near[r.IntN(len(near))]
				}
				b.WriteString(f)
			}
			text := b.String()
			collect := func(text *redactGate) (out [][]int) {
				for m := range rule.matches(text, &engine.rules[i]) {
					out = append(out, m)
				}
				return out
			}
			want := collect(&redactGate{value: text, enabled: false, engine: engine})
			got := collect(&redactGate{value: text, enabled: true, engine: engine})
			if !slices.EqualFunc(got, want, slices.Equal[[]int]) {
				t.Fatalf("rule %d (%s) on %q: anchored matches %v, whole pattern %v", i, rule.expr, text, got, want)
			}
			withBudget(t, 0, -1, func() {
				got = collect(&redactGate{value: text, enabled: true, engine: engine})
			})
			if !slices.EqualFunc(got, want, slices.Equal[[]int]) {
				t.Fatalf("rule %d (%s) on %q: from a safe start %v, whole pattern %v", i, rule.expr, text, got, want)
			}
			matched += len(want)
		}
	}
	// The texts have to reach the patterns for the comparison to mean anything.
	if matched < 3000 {
		t.Fatalf("only %d matches across every rule", matched)
	}
}

// withBudget runs f with the anchored-matching budget set to perByte times the text plus base: 0
// and -1 make every rule run its whole pattern from a safe start.
func withBudget(t *testing.T, perByte, base int, f func()) {
	t.Helper()
	savedPerByte, savedBase := redactAnchoredPerByte, redactAnchoredBase
	redactAnchoredPerByte, redactAnchoredBase = perByte, base
	defer func() { redactAnchoredPerByte, redactAnchoredBase = savedPerByte, savedBase }()
	f()
}

// Redaction from safe starts gives what every rule's whole pattern gives, on the gate test's own
// random inputs: the safe start is exact for every rule, whatever comes before its first place.
// (randomRedactInput takes the legacy math/rand generator the gate test uses.)
func TestRedactFromSafeStartsNeverChangesAResult(t *testing.T) {
	r := mrand.New(mrand.NewSource(7))
	withBudget(t, 0, -1, func() {
		for range 5000 {
			input := randomRedactInput(r)
			if gated, all := redactString(input, true), redactString(input, false); gated != all {
				t.Fatalf("redaction from safe starts of %q = %q, every rule gives %q", input, gated, all)
			}
		}
	})
}

// Redaction's work is linear in the text whatever the text holds. Each text below makes some rule
// try many places, each of which a pattern could read on from to the end of a long line or run:
// many curl commands on one line, JWT-shaped and private-key-shaped fragments that never complete,
// assignments and flags without values, long runs of label words, URL schemes. The work counted
// is every byte redaction hands a pattern, and every byte it scans back over to find where a match
// starts: an upper bound on what the regexp engine and the needle checks read. Doubling the text
// must not more than double it, give or take, at every size.
func TestRedactionWorkIsLinear(t *testing.T) {
	families := map[string]string{
		"curl words on one line":       "curl x ",
		"curl flags on one line":       "curl -H 'a: b' -b ",
		"JWT prefixes":                 "-eyJabcdefghij",
		"private key headers":          "-----BEGIN PRIVATE KEY-----Proc-Type: x ",
		"labels without values":        "key=,",
		"quoted labels without values": `"token": ,`,
		"flags without values":         " --token ''",
		"listed flags":                 `"--token", `,
		"one long flag":                "token",
		"one long label":               "key",
		"URL schemes":                  "a://",
		"authorization headers":        "authorization: bearer ,",
		"cookie headers":               "\ncookie: ",
		"assignments":                  "token=abcdefgh12345678 ",
		// Secrets written right after another one (redact_adjacent.go): chains of keys of fixed
		// length, of tokens that run on into the next, of key ids that end only where the next
		// key starts, of JWT first segments that never complete, and URLs, Bearer tokens, curl
		// commands and replaced copies right after a key.
		"Google keys back to back":     "AIzaSyA-abcdefghijklmnopqrstuvwxyz01234",
		"GitHub tokens back to back":   "ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"AWS key ids back to back":     "AKIAIOSFODNN7EXAMPLE",
		"npm tokens and Google keys":   "npm_abcdefghijklmnopqrstuvwxyz0123456789AIzaSyA-abcdefghijklmnopqrstuvwxyz01234",
		"tokens into JWT prefixes":     "ghp_0123456789abcdefghijklmnopqrstuvwxyzeyJabc-",
		"API keys into JWT prefixes":   "sk-eyJabcdefghij",
		"URL schemes after keys":       "npm_abcdefghijklmnopqrstuvwxyz0123456789a://x:",
		"Bearer tokens after keys":     "npm_abcdefghijklmnopqrstuvwxyz0123456789Bearer ",
		"curl commands after keys":     "npm_abcdefghijklmnopqrstuvwxyz0123456789curl -b x ",
		"keys after replaced copies":   "token=abcdefgh12345678 abcdefgh12345678ghp_0123456789abcdefghijklmnopqrstuvwxyz ",
		"key ids into unfinished keys": "AKIAIOSFODNN7EXAMPLEAIza",
		"tokens into URL schemes":      "ghp_0123456789abcdefghijklmnopqrstuvwxyzpostgres://u:p@h/",
		"tokens into Bearer tokens":    "hf_abcdefghijklmnopqrstuvwxyz01234567Bearer x ",
		"keys into curl commands":      "sk-abcdefghijklmnopqrstuvwxyz0123curl -u a:b ",
		"keys into curl lines":         "sk-abcdefghijklmnopqrstuvwxyz0123curl x ",
		"key ids into URL schemes":     "AKIAIOSFODNN7EXAMPLEa://x:y@",
		"key ids into unfinished URLs": "AKIAIOSFODNN7EXAMPLEa:/",
		// Values that are themselves labels, copies that start or end a match, and AWS secrets in
		// URL passwords, which make redaction pass over the text again; tokens into copies.
		"labels kept values hid":     `token:seCret":token":0 `,
		"labels hid three passes":    `0secret=token :ghp_0123456789abcdefghijklmnopqrstsecret=token :seCret':token':@h' `,
		"labels captured values hid": "token:abctoken\":`secret`:`token`:}token:--token \"token\" ",
		"copies after flags":         "-pAsswd --token -token:\"--token\" ",
		"key ids before copies":      "AKIAIOSFODNN7EXAMPLEAuthorization: Authorization: ",
		"tokens into copies":         "token=opaque-flag-value hf_abcdefghijklmnopqrstuvwxyz01234567opaque-flag-value ",
		"AWS secrets in passwords":   "https://u:a,AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY@h ",
		// A URL password's run on (urlRunsOn) over AWS key ids and their secrets.
		"URL runs over AWS keys":      "A://token:,@AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY@",
		"URL passwords over AWS keys": "A://token:,@0,AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY ",
		// Tokens glued to `Bearer` with no blank after it, which run on to the end of the text.
		"tokens glued to Bearer": "hf_abcdefghijklmnopqrstuvwxyz01234567Bearer.",
		// Every other scan the chains make: open tails run on to the end, curl lines over blank
		// lines, curl lines chained after keys, and URL schemes after tokens.
		"API keys back to back":               "sk-abcdefghijklmnopqrstuvwxyz0123",
		"keys glued to curl over blank lines": "sk-abcdefghijklmnopqrstuvwxyz0123curl x\n   \n\n",
		"curl lines chained after keys":       "npm_abcdefghijklmnopqrstuvwxyz0123456789curl x\n",
		"tokens glued to URL scheme runs":     "ghp_0123456789abcdefghijklmnopqrstuvwxyza.b",
	}
	texts := map[string]func(n int) string{}
	for name, unit := range families {
		texts[name] = func(n int) string { return repeatTo(unit, n) }
	}
	texts["one long flag"] = func(n int) string { return "-" + repeatTo("token", n) + " x" }
	// Texts that are not one unit repeated: many places on one long line or run that all reach the
	// same far end (a curl flag, a URL's `://`), and copies of assigned values that a token runs
	// into.
	texts["key ids glued to curl, one flag at the end"] = func(n int) string {
		return repeatTo("AKIAIOSFODNN7EXAMPLEcurl ", n) + " -u a:b"
	}
	texts["keys glued to curl, one flag at the end"] = func(n int) string {
		return repeatTo("sk-abcdefghijklmnopqrstuvwxyz0123curl ", n) + " -u a:b"
	}
	texts["key ids in a URL scheme run"] = func(n int) string { return repeatTo("AKIA.", n) + "b://" }
	texts["key ids glued into a URL scheme"] = func(n int) string { return repeatTo("AKIAIOSFODNN7EXAMPLE", n) + "b://x:y@h" }
	texts["key ids in URL scheme runs broken by non-ASCII"] = func(n int) string {
		return repeatTo("AKIAIOSFODNN7EXAMPLEa\u00e9", n) + "b://x:y@h"
	}
	texts["key ids in a URL scheme run over Kelvin signs"] = func(n int) string {
		return repeatTo("AKIAIOSFODNN7EXAMPLEa\u212a", n) + "b://x:y@h"
	}
	texts["a long value repeating a token's tail"] = func(n int) string {
		a := strings.Repeat("a", n/3)
		return "token=" + a + " hf_" + a + " " + strings.Repeat("x ", n/6)
	}
	texts["many values sharing their first bytes"] = func(n int) string {
		var b strings.Builder
		for i := 0; b.Len() < n/2; i++ {
			fmt.Fprintf(&b, "token=aaaaaaaa%06d\n", i)
		}
		return b.String() + repeatTo("ghp_"+strings.Repeat("a", 40)+" ", n/2)
	}
	// Long assigned values, whose copies are searched for in the whole text: one as long as half
	// the text, many of fixed length, and many ending the same way, which share a run of states
	// with many edges out of it.
	texts["one value as long as half the text"] = func(n int) string {
		a := strings.Repeat("a", n/2)
		return "token=" + a + " hf_" + a
	}
	texts["many long values"] = func(n int) string {
		var b strings.Builder
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, "token=%070d\n", i)
		}
		return b.String()
	}
	texts["many long values ending the same way"] = func(n int) string {
		var b strings.Builder
		tail := strings.Repeat("a", 70)
		for i := 0; b.Len() < n/2; i++ {
			fmt.Fprintf(&b, "token=%d%s\n", i, tail)
		}
		return b.String() + repeatTo("a", n/2)
	}
	// Every flag on a curl line is redacted in one pass: many on one line, after values that never
	// close, and many lines.
	texts["curl passwords on one line"] = func(n int) string { return "curl" + repeatTo(" -u a:b", n) }
	texts["curl cookies on one line"] = func(n int) string { return "curl" + repeatTo(" -b a=1", n) }
	texts["curl cookies that never close"] = func(n int) string { return "curl -b a=1" + repeatTo(" -b 'x", n) }
	texts["curl lines with flags"] = func(n int) string { return repeatTo("curl -b a=1 -u a:b -b 'x\n", n) }
	// URLs whose scheme a Bearer token took, and replaced schemes with no password after them.
	texts["URLs after Bearer tokens"] = func(n int) string { return repeatTo("Bearer a://u:p@h ", n) }
	texts["replaced schemes without passwords"] = func(n int) string { return "[REDACTED]://u:" + repeatTo("[REDACTED]://u:", n) }
	// Labeled values ending in AWS key ids, with and without their secrets after them.
	texts["labeled key ids and secrets"] = func(n int) string {
		return repeatTo("token=AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY ", n)
	}
	texts["labeled key ids without secrets"] = func(n int) string { return repeatTo("token=xAKIAIOSFODNN7EXAMPLE::|,", n) }
	// Key pairs after labeled key ids, each followed by a run a label could be spelled in.
	texts["labeled key pairs before label runs"] = func(n int) string {
		return repeatTo("token=AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY-"+strings.Repeat("_", 200)+": ", n/2) +
			"token=AKIAIOSFODNN7EXAMPLE,wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" + repeatTo("-", n/2)
	}
	// Copies that end alike after one byte each, X+a^9 and X+a^10 for every printable X, so two
	// states of the copy finder have as many children; a token of every shape, each a chain that
	// asks for the copies; and a long run of `a`, over which the finder would go between those two
	// states at every byte, with the tokens before it and after it. Their work per byte, that of
	// the whole redaction, is held to a ceiling too, not only its growth: about 90 now, where it
	// was over 3,000 when every chain read the whole text through child lists.
	wideCopies := func(tokensFirst bool) func(n int) string {
		return func(n int) string {
			var b, tokens strings.Builder
			for c := byte('!'); c <= '~'; c++ {
				if c == 'a' || strings.IndexByte("\"'`,\\", c) >= 0 {
					continue
				}
				fmt.Fprintf(&b, "token=%c%s\ntoken=%c%s\n", c, strings.Repeat("a", 9), c, strings.Repeat("a", 10))
			}
			for _, token := range adjacentSecrets {
				tokens.WriteString(token + " ")
			}
			run := repeatTo("a", n-b.Len()-tokens.Len())
			if tokensFirst {
				return b.String() + tokens.String() + run
			}
			return b.String() + run + tokens.String()
		}
	}
	texts["copies that end alike, tokens of every shape, a long run"] = wideCopies(true)
	texts["copies that end alike, a long run, tokens of every shape"] = wideCopies(false)
	perByte := map[string]int{
		"copies that end alike, tokens of every shape, a long run": 128,
		"copies that end alike, a long run, tokens of every shape": 128,
	}
	for name, text := range texts {
		work := func(n int) int {
			total := 0
			redactWork = func(bytes int) { total += bytes }
			defer func() { redactWork = nil }()
			redactString(text(n), true)
			return total
		}
		previous := work(16 << 10)
		for n := 32 << 10; n <= 256<<10; n *= 2 {
			w := work(n)
			if w > 5*previous/2+4096 {
				t.Errorf("%s: work %d on %d bytes, %d on half as many: not linear", name, w, n, previous)
				break
			}
			if ceiling := perByte[name]; ceiling > 0 && w > ceiling*n {
				t.Errorf("%s: work %d on %d bytes, %d a byte, more than %d", name, w, n, w/n, ceiling)
				break
			}
			previous = w
		}
	}
}

// repeatTo is unit repeated to n bytes.
func repeatTo(unit string, n int) string {
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}
