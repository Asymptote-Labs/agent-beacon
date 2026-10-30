package asymptoteobserve

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	DefaultStringLimit    = 4096
	DefaultRawStringLimit = 2048
)

// redactedValue replaces a secret. Rules that match a labeled value keep the label (`token=`,
// `Authorization: Bearer `, a URL's `scheme://user:`) and replace only the value, so the text still
// says what was there.
const redactedValue = "[REDACTED]"

// The redaction tables below are named redact*, not secret*: static analysis treats a variable
// named for a secret as sensitive data, and these hold patterns, not credentials.

// redactLabel is the part of a label that marks the value after it as a secret: `token`,
// `GITHUB_TOKEN`, `api_key`, `AWS_SECRET_ACCESS_KEY`, `"password"`. The label must end in the
// marker, so `input_tokens: 812` and `max_tokens=4096` stay readable.
const redactLabel = `[A-Za-z0-9_.-]*?(?:api[_-]?key|access[_-]?key|secret[_-]?key|private[_-]?key|client[_-]?secret|` +
	`token|secret|password|passwd|passphrase|authorization|credentials?)`

// redactSeparator runs from the end of a label to its value: an optional closing quote (escaped
// too, for JSON quoted inside a string) and the separator.
const redactSeparator = `(?:\\?["'` + "`" + `])?\s*[:=]\s*`

// redactOpenQuote opens a quoted value, escaped or not.
const redactOpenQuote = `\\?["'` + "`" + `]`

// redactInQuotes is a value quoted with q, up to its own closing quote: spaces, commas, brackets,
// the other quote characters and escaped characters, an escaped q among them, are part of it
// (`"it's-mine"`, `"ab\"cd"`).
func redactInQuotes(q string) string {
	return `(?:[^` + q + `\\\n]|\\[^\n])+`
}

// redactInEscapedQuotes is a value whose opening quote was itself escaped (`\"`), which means it
// sits inside a JSON string: there the escaped q is its closing quote, and any other escaped
// character is part of it, a quote escaped twice (`\\\"`, the value's own quote) among them.
func redactInEscapedQuotes(q string) string {
	return `(?:[^` + q + `\\\n]|\\\\\\` + q + `|\\[^` + q + `\n])+`
}

// quotedValues pairs each way a value can be opened with the body it runs to: an escaped and a
// plain opening for each quote character. Go has no backreferences, so each pair is its own rule.
var quotedValues = [][2]string{
	{`\\"`, redactInEscapedQuotes(`"`)}, {`"`, redactInQuotes(`"`)},
	{`\\'`, redactInEscapedQuotes(`'`)}, {`'`, redactInQuotes(`'`)},
	{"\\\\`", redactInEscapedQuotes("`")}, {"`", redactInQuotes("`")},
}

// quotedKind says what comes before a quoted value: a secret-named label or a secret-named flag.
type quotedKind int

const (
	quotedAssignment quotedKind = iota
	quotedFlag
)

// quotedRules builds one rule per quotedValues entry for the given kind of prefix.
//
// It takes the kind rather than the prefix pattern itself: static analysis reads a call whose
// argument is a constant naming a credential (the pattern spells out "password") as returning that
// credential, and these rules hold patterns, not secrets.
func quotedRules(kind quotedKind, keep func(string) bool) []redactionRule {
	prefix, words, separator, within := `\b`+redactLabel+redactSeparator, redactLabelWords, skipSeparator, redactLabelNeedles
	if kind == quotedFlag {
		prefix, words, separator, within = redactFlag, redactFlagWords, skipFlagSeparator, redactFlagNeedles
	}
	rules := make([]redactionRule, 0, len(quotedValues))
	for _, quoted := range quotedValues {
		// The pattern opens the value with this one quote right after the separator, so text
		// where no label is followed by it cannot match.
		open := strings.ReplaceAll(quoted[0], `\\`, `\`)
		needles := newRedactNeedles(true, words...)
		needles.within = within
		needles.followedBy = func(rest string) bool {
			rest, ok := separator(rest)
			return ok && strings.HasPrefix(rest, open)
		}
		rules = append(rules, lazyRule(`(?i)(`+prefix+quoted[0]+`)(?P<value>`+quoted[1]+`)`, "${1}"+redactedValue, keep, needles))
	}
	return rules
}

// redactUnquoted is an unquoted value, up to a quote, a comma or whitespace. Brackets, braces and
// backslashes are part of it: a generated password is full of them. It never starts with a quote,
// escaped or not, so it leaves a value the quoted rule already replaced alone.
const redactUnquoted = `(?:[^"'` + "`" + `,\s\\]|\\[^"'` + "`" + `])[^"'` + "`" + `,\s]*`

// redactBare is a bare value after a quoted label: a JSON number or literal, which ends at the
// structure around it.
const redactBare = `[^"'` + "`" + `\\\s,{}\[\]][^"'` + "`" + `\\\s,{}\[\]]*`

// redactFlagName is a command-line flag named for a secret: `--password`, `--token`, `-api-key`.
const redactFlagName = `--?[a-z0-9-]*(?:password|passwd|passphrase|token|secret|api-?key|access-?key)`

// redactFlag is a redactFlagName and its separator: `--password `, `--token=`, `-api-key `.
const redactFlag = `(?:^|\s)` + redactFlagName + `(?:=|\s+)`

// redactionRule is one redaction. ${1} and ${3}, when the pattern has them, are kept around the
// value.
type redactionRule struct {
	pattern     func() *regexp.Regexp
	replacement string
	// keep, when set, leaves a match alone if its value (the group named value) passes.
	keep func(value string) bool
	// needles are literals the pattern cannot match without: when the text holds none of them,
	// the rule is skipped without running the pattern.
	needles *redactNeedles
	// repeat applies the rule until the text stops changing. A rule scoped to a command line
	// matches from the command to the value, so a second such value on the line lies inside the
	// first match and is reached only by another pass.
	repeat bool
}

// redactNeedles is a set of literals a pattern cannot match without. folded needles are lower
// case and compared against the lower-cased text, for a case-insensitive pattern. followedBy, when
// set, must also hold for the text right after the needle. within, when set, is a broader set the
// text must also hold, searched first: it is what keeps a family of narrow sets to one search on
// text that holds none of them.
type redactNeedles struct {
	id         uint
	folded     bool
	needles    []string
	followedBy func(rest string) bool
	precededBy func(before string) bool
	within     *redactNeedles
}

var redactNeedleSets uint

func newRedactNeedles(folded bool, needles ...string) *redactNeedles {
	if len(needles) == 0 {
		panic("asymptoteobserve: a redaction rule needs at least one needle")
	}
	if redactNeedleSets >= 64 {
		panic("asymptoteobserve: redactGate tracks at most 64 needle sets")
	}
	set := &redactNeedles{id: redactNeedleSets, folded: folded, needles: needles}
	redactNeedleSets++
	return set
}

// rule is a case-sensitive rule that cannot match text holding none of needles.
func rule(expr, replacement string, keep func(string) bool, needles ...string) redactionRule {
	return lazyRule(expr, replacement, keep, newRedactNeedles(false, needles...))
}

// foldedRule is a case-insensitive rule that cannot match text whose lower-cased form holds none
// of needles, which must be lower case.
func foldedRule(expr, replacement string, keep func(string) bool, needles ...string) redactionRule {
	return lazyRule(expr, replacement, keep, newRedactNeedles(true, needles...))
}

// curlRule is a foldedRule scoped to a curl command line. A flag curl accepts more than once (two
// -b cookie strings) is redacted at every occurrence on the line.
func curlRule(expr, replacement string) redactionRule {
	r := foldedRule(expr, replacement, nil, "curl")
	r.repeat = true
	return r
}

// lazyRule compiles its pattern the first time text gets past the needles. A hook is a process
// per event, and most events never need most rules: compiling all of them up front cost every
// hook run a few milliseconds before it had read its input.
func lazyRule(expr, replacement string, keep func(string) bool, needles *redactNeedles) redactionRule {
	return redactionRule{pattern: lazyPattern(expr), replacement: replacement, keep: keep, needles: needles}
}

// lazyPattern compiles expr the first time it is called.
//
// The rules whose pattern names a credential (redactLabel, redactFlag) are written as struct
// literals around it rather than through lazyRule: static analysis reads a call whose argument is a
// constant spelling out "password" as returning that password, and would then treat every string
// redaction returns, and every hash of one, as a credential.
func lazyPattern(expr string) func() *regexp.Regexp {
	return sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(expr) })
}

// redactLabelWords are the words every redactLabel match ends in, lower-cased: each marker in its
// alternation ends in one of them (api_key in "key", client_secret in "secret").
var redactLabelWords = []string{"key", "secret", "token", "password", "passwd", "passphrase", "authorization", "credential", "credentials"}

// redactLabelNeedles gate the rules that take a label and redactSeparator. A word that is not
// followed by a separator (`input_tokens`, `keyboard`) cannot start a match. The rules share the
// one set, so the gate searches for it once per text rather than once per rule.
var redactLabelNeedles = func() *redactNeedles {
	set := newRedactNeedles(true, redactLabelWords...)
	set.followedBy = func(rest string) bool {
		_, ok := skipSeparator(rest)
		return ok
	}
	return set
}()

// redactQuotedLabelNeedles gate the rule whose label is closed by a quote before its separator.
var redactQuotedLabelNeedles = func() *redactNeedles {
	set := newRedactNeedles(true, redactLabelWords...)
	set.within = redactLabelNeedles
	set.followedBy = func(rest string) bool {
		rest = strings.TrimPrefix(rest, `\`)
		if rest == "" || !strings.ContainsRune(redactQuotes, rune(rest[0])) {
			return false
		}
		_, ok := skipSeparator(rest)
		return ok
	}
	return set
}()

// redactFlagWords are the words every redactFlag match ends in, lower-cased.
var redactFlagWords = []string{"password", "passwd", "passphrase", "token", "secret", "key"}

// redactFlagNeedles gate the flag rules: a flag's name is followed by `=` or whitespace, and is the
// end of a word that starts with a dash at the start of the text or after whitespace.
var redactFlagNeedles = func() *redactNeedles {
	set := newRedactNeedles(true, redactFlagWords...)
	set.followedBy = func(rest string) bool {
		_, ok := skipFlagSeparator(rest)
		return ok
	}
	set.precededBy = startsFlag
	return set
}()

// redactListedFlagNeedles gate the rule for a flag and its value written as two quoted items of a
// list: the flag's name is opened by a quote and a dash, and closed by a quote, a comma and the
// quote opening the value.
var redactListedFlagNeedles = func() *redactNeedles {
	set := newRedactNeedles(true, redactFlagWords...)
	set.followedBy = func(rest string) bool {
		if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
			return false
		}
		rest = strings.TrimLeft(rest[1:], regexpSpace)
		if !strings.HasPrefix(rest, ",") {
			return false
		}
		rest = strings.TrimLeft(rest[1:], regexpSpace)
		return rest != "" && (rest[0] == '"' || rest[0] == '\'')
	}
	set.precededBy = func(before string) bool {
		start, ok := flagStart(before)
		return ok && start > 0 && (before[start-1] == '"' || before[start-1] == '\'')
	}
	return set
}()

// startsFlag reports whether the text before a flag word ends in what redactFlag puts ahead of it:
// `(?:^|\s)--?[a-z0-9-]*`.
func startsFlag(before string) bool {
	start, ok := flagStart(before)
	return ok && (start == 0 || strings.ContainsRune(regexpSpace, rune(before[start-1])))
}

// flagStart finds the dash that opens the flag the text before a flag word ends in: the start of
// the run of letters, digits and dashes at its end. A pattern's `--?[a-z0-9-]*` can only begin
// where that maximal run does, because what it requires before the dash (whitespace, a quote) is
// not part of the run.
func flagStart(before string) (int, bool) {
	start := len(before)
	for start > 0 {
		c := before[start-1]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			break
		}
		start--
	}
	if start == len(before) || before[start] != '-' {
		return 0, false
	}
	return start, true
}

// redactQuotes are the quote characters redactSeparator and quotedValues know.
const redactQuotes = "\"'`"

// skipSeparator consumes what redactSeparator matches, `(?:\\?["'`])?\s*[:=]\s*`, from the start
// of rest. Taking all the whitespace is what the pattern does too: nothing that may follow it can
// start with whitespace.
func skipSeparator(rest string) (string, bool) {
	if strings.HasPrefix(rest, `\`) {
		if len(rest) < 2 || !strings.ContainsRune(redactQuotes, rune(rest[1])) {
			return "", false
		}
		rest = rest[2:]
	} else if rest != "" && strings.ContainsRune(redactQuotes, rune(rest[0])) {
		rest = rest[1:]
	}
	rest = strings.TrimLeft(rest, regexpSpace)
	if rest == "" || (rest[0] != ':' && rest[0] != '=') {
		return "", false
	}
	return strings.TrimLeft(rest[1:], regexpSpace), true
}

// skipFlagSeparator consumes the separator ending redactFlag, `(?:=|\s+)`, from the start of rest.
func skipFlagSeparator(rest string) (string, bool) {
	if strings.HasPrefix(rest, "=") {
		return rest[1:], true
	}
	if trimmed := strings.TrimLeft(rest, regexpSpace); len(trimmed) < len(rest) {
		return trimmed, true
	}
	return "", false
}

// regexpSpace is \s in Go's regexp syntax: ASCII whitespace, never Unicode.
const regexpSpace = " \t\n\f\r"

// isAuthScheme reports whether a labeled value is an Authorization scheme, alone or with its
// credential already redacted, rather than a credential. `Authorization: Bearer x` is redacted to
// `Authorization: Bearer [REDACTED]` by the header rule, and the assignment rules must not then
// take the scheme for the value and erase what kind of credential the line held.
func isAuthScheme(value string) bool {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 2 || (len(fields) == 2 && fields[1] != redactedValue) {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "bearer", "basic", "token", "digest":
		return true
	}
	return false
}

// isRedactedJWTPrefix keeps the `eyJ` the header rule left in `Bearer eyJ[REDACTED]`, which the
// bearer rule would otherwise take for a token of its own.
func isRedactedJWTPrefix(value string) bool {
	return value == "eyJ"
}

// isRedactedValue keeps a value an earlier pass already replaced, so redaction stays idempotent.
func isRedactedValue(value string) bool {
	return value == redactedValue
}

// isLiteralOrAuthScheme is isAuthScheme, plus the JSON literals that carry no secret.
func isLiteralOrAuthScheme(value string) bool {
	switch value {
	case "null", "true", "false":
		return true
	}
	return isAuthScheme(value)
}

var redactionRules = concatRules([]redactionRule{
	// A private key block. The header must be followed by key material, so source code, a diff or
	// grep output that only names the header keeps the text after it. Without an END line (the
	// block was cut) the rest of the text goes.
	rule(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----(?:\s|\\[rn]|(?:Proc-Type|DEK-Info|Comment|Version):[^\n\\]*)*`+
		`[A-Za-z0-9+/=]{16,}[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----|$)`, redactedValue, nil, "-----BEGIN "),
	// An Authorization header's credential after its scheme: Bearer, Basic, Token, Digest. A JWT
	// keeps its public `eyJ` prefix, as the shape rules below keep theirs, so `Bearer eyJ[REDACTED]`
	// still says a token was there where `Bearer [REDACTED]` may only have been the word "token" or
	// a `$VARIABLE`.
	foldedRule(`(?i)(authorization["']?\s*[:=]\s*["']?(?:bearer|basic|token|digest)[ \t]+(?-i:eyJ)?)([^"'\s,]+)()`, "${1}"+redactedValue, nil, "authorization"),
	foldedRule(`(?i)(\bbearer[ \t]+(?-i:eyJ)?)(?P<value>[a-z0-9._~+/=-]+)`, "${1}"+redactedValue, isRedactedJWTPrefix, "bearer"),
	// A labeled assignment in prose, shell, env files, YAML, TOML or JSON: `token=x`,
	// `export GITHUB_TOKEN="x"`, `{"api_key": "x"}`. The closing quote of a quoted label is allowed
	// between the label and the separator. A quoted value runs to its closing quote; the unquoted
	// rule never starts at a quote, so it leaves a value the quoted rule already replaced alone.
}, quotedRules(quotedAssignment, isAuthScheme), []redactionRule{
	// A quoted label with a bare value is JSON (or a YAML or Python mapping) holding a number, a
	// literal or a structure. The replacement is quoted the way the label is, so the text still
	// parses where something decodes it again (string-encoded tool arguments do); null, true, false
	// and nested objects or arrays are left for their own fields' rules.
	{
		pattern:     lazyPattern(`(?i)(\b` + redactLabel + `(?P<q>` + redactOpenQuote + `)\s*[:=]\s*)(?P<value>` + redactBare + `)`),
		replacement: "${1}${q}" + redactedValue + "${q}", keep: isLiteralOrAuthScheme, needles: redactQuotedLabelNeedles,
	},
	{
		pattern:     lazyPattern(`(?i)(\b` + redactLabel + `\s*[:=]\s*)(?P<value>` + redactUnquoted + `)`),
		replacement: "${1}" + redactedValue, keep: isAuthScheme, needles: redactLabelNeedles,
	},
	// A command-line flag: `--password x`, `--token=x`, `--api-key x`, and quoted, the way agents
	// usually write arguments: `--password "x y"`, `--token='x'`.
}, quotedRules(quotedFlag, nil), []redactionRule{
	{pattern: lazyPattern(`(?i)(` + redactFlag + `)([^\s"'-][^\s"']*)`), replacement: "${1}" + redactedValue, needles: redactFlagNeedles},
	// A flag and its value as two quoted items of an argument list, the way an MCP server's args
	// are written in JSON, TOML and YAML: `"args": ["--api-key", "x"]`.
	{
		pattern:     lazyPattern(`(?i)(["']` + redactFlagName + `["']\s*,\s*["'])(?P<value>(?:[^"'\n\\]|\\[^\n])+)`),
		replacement: "${1}" + redactedValue, keep: isRedactedValue, needles: redactListedFlagNeedles,
	},
	// curl's basic-auth credentials: `-u user:pass`, `-uuser:pass`, `--user user:pass` and
	// `--proxy-user user:pass`. The user name and the colon stay. A bare `-u user` (curl prompts for
	// the password) has nothing to redact, and the rule is limited to curl lines so `docker run -u
	// 1000:1000` is left alone.
	curlRule(`(?i)(\bcurl\b[^\n]*?\s(?:-[a-z]*u\s*|(?:--user|--proxy-user)(?:=|\s+))["']?[^:\s"']*:)([^\s"']+)`, "${1}"+redactedValue),
	// curl's cookie flag with a cookie string: `-b 'sid=x; csrf=y'`, `--cookie sid=x`. Only a value
	// holding `=` is cookies; anything else names a cookie file, which stays. The quotes stay.
	curlRule(`(?i)(\bcurl\b[^\n]*?\s(?:-[a-z]*b\s*|--cookie(?:=|\s+)))(?:(')[^'\n]*=[^'\n]*(')|(")[^"\n]*=[^"\n]*(")|[^\s"'=]+=[^\s"']*)`, "${1}${2}${4}"+redactedValue+"${3}${5}"),
	// A Cookie or Set-Cookie header: the whole value is a session credential, as the field-name rule
	// in SanitizeMap already treats a structured `cookie` field. Only where a header or key can
	// start (a line, a quote, curl's -H), so prose that says "cookie:" keeps its sentence.
	foldedRule(`(?im)((?:^\s*|["']\s*|(?:-H|--header)(?:=|\s+)["']?)(?:set-)?cookie["']?\s*:\s*["']?)([^\n"'\\]+)`, "${1}"+redactedValue, isRedactedValue, "cookie"),
	// The password in a URL's userinfo: `postgres://user:pw@host`, `https://x-access-token:t@github.com`,
	// and with no user name, the way Redis URLs carry a password: `redis://:pw@host`.
	foldedRule(`(?i)(\b[a-z][a-z0-9+.-]*://[^/\s:@"'<>]*:)([^@/\s"'<>]+)(@)`, "${1}"+redactedValue+"${3}", nil, "://"),
	// Credentials recognizable by their own shape, wherever they appear. The type prefix is public
	// and stays (`ghp_[REDACTED]`, `AKIA[REDACTED]`), the way secret scanners report a finding: the
	// line still says a GitHub token or an AWS key was there, which is what detection rules match on
	// in the log, and none of the credential is left.
	rule(`\b(sk-)[A-Za-z0-9_-]{20,}`, "${1}"+redactedValue, nil, "sk-"), // Anthropic sk-ant-, OpenAI sk-proj- and sk-
	// An AWS secret access key has no shape of its own, but where it follows its key id within a few
	// separators (a credentials CSV, `aws configure` output, a table, a JSON array) it is the 40
	// characters after them. This runs before the key id rule below replaces the id it keys on.
	rule(`(\b(?:AKIA|ASIA)[A-Z0-9]{16}\b[\s,:|"']{1,8})[A-Za-z0-9/+]{40}([^A-Za-z0-9/+=]|$)`, "${1}"+redactedValue+"${2}", nil, "AKIA", "ASIA"),
	rule(`\b(AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`, "${1}"+redactedValue, nil, "AKIA", "ASIA", "ABIA", "ACCA"),  // AWS access key id
	rule(`\b(gh[pousr]_)[A-Za-z0-9]{30,}`, "${1}"+redactedValue, nil, "ghp_", "gho_", "ghu_", "ghs_", "ghr_"), // GitHub tokens
	rule(`\b(github_pat_)[A-Za-z0-9_]{22,}`, "${1}"+redactedValue, nil, "github_pat_"),                        // GitHub fine-grained tokens
	rule(`\b(glpat-)[A-Za-z0-9_-]{20,}`, "${1}"+redactedValue, nil, "glpat-"),                                 // GitLab
	rule(`\b(xox[abposr]-)[A-Za-z0-9-]{10,}`, "${1}"+redactedValue, nil, "xox"),                               // Slack
	rule(`\b([rs]k_(?:live|test)_)[A-Za-z0-9]{16,}`, "${1}"+redactedValue, nil, "k_live_", "k_test_"),         // Stripe
	rule(`\b(AIza)[0-9A-Za-z_-]{35}`, "${1}"+redactedValue, nil, "AIza"),                                      // Google API key
	rule(`\b(ya29\.)(?:c\.)?[0-9A-Za-z_-]{20,}`, "${1}"+redactedValue, nil, "ya29."),                          // Google OAuth access token
	rule(`\b(npm_)[A-Za-z0-9]{36}`, "${1}"+redactedValue, nil, "npm_"),                                        // npm
	rule(`\b(hf_)[A-Za-z0-9]{30,}`, "${1}"+redactedValue, nil, "hf_"),                                         // Hugging Face
	// Beacon's own keys and tokens: API keys, device and enforcement keys, MCP tokens and their refresh
	// tokens, and the CLI sign-in's access token, authorization code, client and exchange values.
	rule(`\b(bcn_(?:api|device|mcprt|mcp|enfdev|cli|code|client|exchange)_)[A-Za-z0-9_-]{8,}`, "${1}"+redactedValue, nil, "bcn_"),
	rule(`\b(eyJ)[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, "${1}"+redactedValue, nil, "eyJ"), // JWT
})

func concatRules(groups ...[]redactionRule) []redactionRule {
	var all []redactionRule
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}

func (rule redactionRule) apply(value string) string {
	pattern := rule.pattern()
	if rule.keep == nil {
		out := pattern.ReplaceAllString(value, rule.replacement)
		// Each pass redacts at least one more value, and a redacted value matches no further.
		for pass := 0; rule.repeat && out != value && pass < 16; pass++ {
			value, out = out, pattern.ReplaceAllString(out, rule.replacement)
		}
		return out
	}
	index := pattern.SubexpIndex("value")
	if index < 0 {
		index = 2
	}
	group := 2 * index
	var out []byte
	last := 0
	for _, match := range pattern.FindAllStringSubmatchIndex(value, -1) {
		if match[group] >= 0 && rule.keep(value[match[group]:match[group+1]]) {
			continue
		}
		out = append(out, value[last:match[0]]...)
		out = pattern.ExpandString(out, rule.replacement, value, match)
		last = match[1]
	}
	if out == nil {
		return value
	}
	return string(append(out, value[last:]...))
}

// redactAssignedPattern captures the value of a labeled assignment, quoted with ", ' or a backtick
// (${1} to ${3}) or not (${4}), so a bare copy of it elsewhere in the same text is
// removed too.
var redactAssignedPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + redactLabel + redactSeparator + `(?:` + quotedAlternatives() + `|(` + redactUnquoted + `))`)
})

// quotedAlternatives is every quotedValues entry as one capturing alternative.
func quotedAlternatives() string {
	parts := make([]string, 0, len(quotedValues))
	for _, quoted := range quotedValues {
		parts = append(parts, quoted[0]+`(`+quoted[1]+`)`)
	}
	return strings.Join(parts, "|")
}

// redactWindow is how far past a string's limit redaction reads before the string is cut, so a
// secret that straddles the cut is recognized whole rather than left as a recognizable prefix. It
// covers a 4096-bit RSA private key block.
const redactWindow = 4096

type PrivacyOptions struct {
	RedactSecrets bool
	StringLimit   int
}

func RedactString(value string) string {
	return redactString(value, true)
}

// redactString is RedactString; gated false runs every rule whatever the text holds, which the
// tests use to prove the gate never changes a result.
//
// The gate is what keeps redaction affordable on the hook path, where it runs over every string of
// every event before the agent may continue. Go's regexp engine has no DFA, so the case-insensitive
// label rules cost microseconds on a short string and tens of milliseconds on a tool output of a
// few kilobytes. Almost no text holds a needle of most rules, and a substring search for one is
// orders of magnitude cheaper than the pattern it stands in front of.
func redactString(value string, gated bool) string {
	text := redactGate{value: value, enabled: gated}
	// Remember assignment values before replacing their labeled occurrence so
	// repeated bare copies in the same retained prompt/output are also removed.
	// This is common in model reasoning that quotes `token=value` and later
	// refers to `value` by itself.
	assigned := map[string]struct{}{}
	if text.holds(redactLabelNeedles) {
		for _, match := range redactAssignedPattern().FindAllStringSubmatch(value, -1) {
			copied := strings.Join(match[1:], "")
			if len(copied) >= 8 && copied != redactedValue && !isAuthScheme(copied) {
				assigned[copied] = struct{}{}
			}
		}
	}
	for _, rule := range redactionRules {
		if !text.holds(rule.needles) {
			continue
		}
		text.set(rule.apply(text.value))
	}
	value = text.value
	// Each copy is searched for and removed; none of it survives into the result.
	return replaceAssigned(value, assigned)
}

// replaceAssigned replaces every copy of each assigned value with redactedValue in one pass.
// Replacing them one strings.ReplaceAll at a time scanned the text once per assignment, and both
// grow with the text, so the cost was quadratic: Beacon Cloud redacts uploaded shared traces with
// this function, where that let a small gzip body hold a core for minutes. The values are a set,
// longest first, so where two start at the same byte the longer is removed whole.
func replaceAssigned(value string, assigned map[string]struct{}) string {
	if len(assigned) == 0 {
		return value
	}
	values := slices.SortedFunc(maps.Keys(assigned), func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, 2*len(values))
	for _, copied := range values {
		pairs = append(pairs, copied, redactedValue)
	}
	return strings.NewReplacer(pairs...).Replace(value)
}

// IsSecretKeyName reports whether a structured field's name has a word naming a credential, for
// callers that judge a field by its name alone. IsSecretField judges a value held under it.
func IsSecretKeyName(key string) bool {
	secret, _ := secretKeyName(key)
	return secret
}

// IsSecretField reports whether a value is a credential by the name of the field holding it, the
// rule SanitizeMap applies, for callers that walk structured values of their own. A value that
// only names an auth scheme (`token_type: bearer`) is none, and nor is a number under a name whose
// only credential word is "tokens", which is how a count of model tokens is named
// (`max_tokens: 4096`, `input_tokens: "1200"`), where `GITHUB_TOKENS: abc` is a credential.
func IsSecretField(key string, value interface{}) bool {
	secret, onlyTokens := secretKeyName(key)
	if !secret || onlyTokens && isNumber(value) {
		return false
	}
	text, ok := value.(string)
	return !ok || !isAuthScheme(text)
}

// isNumber reports whether a value is a number, or a string that only spells one.
func isNumber(value interface{}) bool {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return true
	case string:
		digits, point := 0, false
		for i := range len(typed) {
			switch c := typed[i]; {
			case c >= '0' && c <= '9':
				digits++
			case c == '.' && !point:
				point = true
			case c == '-' && i == 0:
			default:
				return false
			}
		}
		return digits > 0
	}
	return false
}

// IsSecretFlag reports whether an argument is a flag named for a secret written on its own, so the
// next argument is its value: `--api-key`, `--token`, `-password`. SanitizeSlice redacts that next
// argument, for callers that walk argument lists of their own.
func IsSecretFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && redactSecretFlagPattern().MatchString(arg)
}

var redactSecretFlagPattern = lazyPattern(`(?i)^` + redactFlagName + `$`)

// redactGate answers whether text holds any of a set of needles, lower-casing it and searching for
// each set at most once per version of the text.
type redactGate struct {
	value   string
	enabled bool
	lower   string
	lowered bool
	// known and present are bit sets over redactNeedles ids: which sets were searched for in this
	// version of the text, and which were found.
	known, present uint64
	// unfoldable is set when the text holds a character Go's case folding equates with an ASCII
	// letter (U+017F folds to s, U+212A to k): a case-insensitive pattern can match "ſecret", which
	// lower-casing does not turn into "secret", so folded rules always run on such text.
	unfoldable bool
}

func (g *redactGate) set(value string) {
	if value != g.value {
		g.value, g.lowered, g.known, g.present = value, false, 0, 0
	}
}

func (g *redactGate) holds(set *redactNeedles) bool {
	if !g.enabled {
		return true
	}
	if set.within != nil && !g.holds(set.within) {
		return false
	}
	bit := uint64(1) << set.id
	if g.known&bit != 0 {
		return g.present&bit != 0
	}
	g.known |= bit
	haystack := g.value
	if set.folded {
		if !g.lowered {
			g.lower, g.lowered = strings.ToLower(g.value), true
			g.unfoldable = strings.ContainsAny(g.value, "\u017f\u212a")
		}
		if g.unfoldable {
			g.present |= bit
			return true
		}
		haystack = g.lower
	}
	for _, needle := range set.needles {
		for from := 0; ; {
			i := strings.Index(haystack[from:], needle)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(needle)
			// The next search starts one byte on, not at end: an occurrence may overlap this one.
			from = start + 1
			if (set.precededBy == nil || set.precededBy(haystack[:start])) && (set.followedBy == nil || set.followedBy(haystack[end:])) {
				g.present |= bit
				return true
			}
		}
	}
	return false
}

// secretKeyName reports whether a structured field's name says its value is a credential, and
// whether the only word that says so is "tokens". The name is split into words at punctuation and
// at camelCase, and a word naming a credential marks it wherever it stands: `GITHUB_TOKEN`,
// `apiKey`, `X-Api-Key`, `X_AUTH`, `SECRET_KEY_BASE`, `DB_PASS`, `client_secret`, `Cookie`. A
// name that runs one into the words before it (`PGPASSWORD`, `githubtoken`) is marked by how it
// ends. `author`, `keyboard` and `PWD` are not secrets.
func secretKeyName(key string) (secret, onlyTokens bool) {
	var name strings.Builder
	name.Grow(len(key))
	start := 0
	// endWord reports whether the word that ends here names a credential other than "tokens", and
	// starts the next one.
	endWord := func() bool {
		word := name.String()[start:]
		start = name.Len()
		if word == "tokens" {
			onlyTokens = true
			return false
		}
		return slices.Contains(redactKeyWords, word)
	}
	for i := range len(key) {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			name.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			// A capital starts a word after a lower-case letter or a digit (`apiKey`), and after
			// other capitals when a lower-case letter follows it (`APIKey`).
			if i > 0 && (isLowerOrDigit(key[i-1]) || isUpper(key[i-1]) && i+1 < len(key) && key[i+1] >= 'a' && key[i+1] <= 'z') && endWord() {
				return true, false
			}
			name.WriteByte(c + 'a' - 'A')
		default:
			if endWord() {
				return true, false
			}
		}
	}
	if endWord() {
		return true, false
	}
	whole := name.String()
	for _, suffix := range redactKeySuffixes {
		if strings.HasSuffix(whole, suffix) {
			return true, false
		}
	}
	return onlyTokens, onlyTokens
}

func isLowerOrDigit(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// redactKeyWords are the words of a field name that make it a credential's, with "tokens", which
// secretKeyName reports apart. "pwd" is not one: PWD is the working directory.
var redactKeyWords = []string{
	"password", "passwords", "passwd", "pass", "passphrase", "secret", "secrets", "token", "key", "keys", "apikey",
	"accesskey", "secretkey", "privatekey", "auth", "authorization", "credential", "credentials", "cookie",
}

// redactKeySuffixes mark a field name that runs a credential's word into the words before it.
var redactKeySuffixes = []string{
	"password", "passwd", "passphrase", "secret", "token", "apikey", "accesskey", "secretkey",
	"privatekey", "authorization", "credential", "credentials", "cookie",
}

// TruncateUTF8 cuts value to at most limit bytes, backing up to the start of a character so a
// multi-byte character is never split into invalid UTF-8.
func TruncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

// TruncateString cuts value to at most limit bytes on a character boundary, marking the cut
// when limit leaves room for the marker.
func TruncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit < 32 {
		return TruncateUTF8(value, limit)
	}
	return TruncateUTF8(value, limit-15) + "...[truncated]"
}

// CleanString redacts before it truncates, so a cut can never leave part of a secret behind.
// Redaction reads only a window past the limit, which keeps a multi-megabyte command output as
// cheap as before.
func CleanString(value string, limit int, redactSecrets bool) string {
	if !redactSecrets {
		return TruncateString(value, limit)
	}
	if limit <= 0 {
		return TruncateString(RedactString(value), limit)
	}
	// Redaction shortens what it matches, so a window that shrank pulls text from near its own cut
	// inside the limit, where an unrecognizable fragment of a secret would survive. Widen the window
	// until a full redactWindow of redacted text is left past the limit, or the whole string fits.
	for window := limit + redactWindow; ; window *= 2 {
		if len(value) <= window {
			return TruncateString(RedactString(value), limit)
		}
		if redacted := RedactString(value[:window]); len(redacted) >= limit+redactWindow {
			return TruncateString(redacted, limit)
		}
	}
}

func SanitizeMap(input map[string]interface{}, opts PrivacyOptions) map[string]interface{} {
	limit := opts.StringLimit
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case string:
			// A value whose field is named for a credential is one, whatever it looks like:
			// `{"password": "hunter2"}` has no shape a pattern could recognize.
			if opts.RedactSecrets && typed != "" && IsSecretField(key, typed) {
				out[key] = redactedValue
				continue
			}
			out[key] = CleanString(typed, limit, opts.RedactSecrets)
		case map[string]interface{}:
			out[key] = SanitizeMap(typed, opts)
		case []interface{}:
			// A multi-valued header (`{"Cookie": ["sid=1"]}`, OTel's http.request.header.*) is as
			// much a credential as a single value.
			if opts.RedactSecrets && IsSecretKeyName(key) {
				out[key] = redactAllStrings(typed, opts)
				continue
			}
			out[key] = SanitizeSlice(typed, opts)
		default:
			out[key] = typed
		}
	}
	return out
}

// redactAllStrings replaces every non-empty string in a slice held under a secret-named field,
// at any depth of nested slices; maps inside it are sanitized by their own field names.
func redactAllStrings(input []interface{}, opts PrivacyOptions) []interface{} {
	out := make([]interface{}, len(input))
	for index, value := range input {
		switch typed := value.(type) {
		case string:
			if typed == "" {
				out[index] = typed
			} else {
				out[index] = redactedValue
			}
		case []interface{}:
			out[index] = redactAllStrings(typed, opts)
		case map[string]interface{}:
			out[index] = SanitizeMap(typed, opts)
		default:
			out[index] = typed
		}
	}
	return out
}

func SanitizeSlice(input []interface{}, opts PrivacyOptions) []interface{} {
	limit := opts.StringLimit
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	out := make([]interface{}, len(input))
	for index, value := range input {
		switch typed := value.(type) {
		case string:
			// A flag named for a secret has its value in the next item: `["--api-key", "x"]`.
			if opts.RedactSecrets && typed != "" && index > 0 && followsSecretFlag(input[index-1]) {
				out[index] = redactedValue
				continue
			}
			out[index] = CleanString(typed, limit, opts.RedactSecrets)
		case map[string]interface{}:
			out[index] = SanitizeMap(typed, opts)
		case []interface{}:
			out[index] = SanitizeSlice(typed, opts)
		default:
			out[index] = typed
		}
	}
	return out
}

func followsSecretFlag(previous interface{}) bool {
	flag, ok := previous.(string)
	return ok && IsSecretFlag(flag)
}

func SanitizeEvent(event Event, maxBytes int) Event {
	event.Message = CleanString(event.Message, DefaultStringLimit, true)
	if event.Tool != nil {
		event.Tool.Command = CleanString(event.Tool.Command, DefaultStringLimit, true)
		event.Tool.Path = TruncateString(event.Tool.Path, DefaultRawStringLimit)
	}
	if event.Command != nil {
		event.Command.Command = CleanString(event.Command.Command, DefaultStringLimit, true)
		event.Command.Output = CleanString(event.Command.Output, DefaultStringLimit, true)
	}
	// Retained content the writers have always emitted but this function never
	// saw, because the struct did not declare the fields. A diff or a block of
	// command output is exactly the kind of text that carries a credential, so
	// these get the same limit and the same secret redaction as prompt.text
	// rather than a weaker one.
	if event.File != nil {
		event.File.Diff = CleanString(event.File.Diff, DefaultStringLimit, true)
	}
	if event.Approval != nil {
		event.Approval.Reason = CleanString(event.Approval.Reason, DefaultStringLimit, true)
	}
	if event.Policy != nil {
		event.Policy.Reason = CleanString(event.Policy.Reason, DefaultStringLimit, true)
	}
	if event.Prompt != nil {
		event.Prompt.Text = CleanString(event.Prompt.Text, DefaultStringLimit, true)
	}
	if event.MCP != nil {
		event.MCP = sanitizeTyped(event.MCP, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if event.SystemContext != nil {
		event.SystemContext = sanitizeTyped(event.SystemContext, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if event.GenAI != nil {
		description := ""
		if event.GenAI.Tool != nil {
			description = event.GenAI.Tool.Description
		}
		event.GenAI = sanitizeTyped(event.GenAI, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
		// A tool description is text the model reads, so it gets the prompt-text limit rather than
		// the raw-attribute one: under 2 KB a padded description would hide its instructions past
		// the point the poisoning rule sees.
		if description != "" && event.GenAI.Tool != nil {
			event.GenAI.Tool.Description = CleanString(description, DefaultStringLimit, true)
		}
	}
	if event.Raw != nil {
		event.Raw = SanitizeMap(event.Raw, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if data, err := json.Marshal(event); err == nil && len(data) > maxBytes {
		event.Truncated = true
	}
	return event
}

func sanitizeTyped[T any](input *T, opts PrivacyOptions) *T {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		out := *input
		return &out
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		out := *input
		return &out
	}
	raw = SanitizeMap(raw, opts)
	data, err = json.Marshal(raw)
	if err != nil {
		out := *input
		return &out
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		fallback := *input
		return &fallback
	}
	return &out
}
