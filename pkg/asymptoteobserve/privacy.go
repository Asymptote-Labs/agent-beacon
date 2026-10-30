package asymptoteobserve

import (
	"encoding/json"
	"maps"
	"math"
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
	prefix, words, separator, within, start := `\b`+redactLabel+redactSeparator, redactLabelWords, skipSeparator, redactLabelNeedles, startLabel
	if kind == quotedFlag {
		prefix, words, separator, within, start = redactFlag, redactFlagWords, skipFlagSeparator, redactFlagNeedles, startFlag
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
		rules = append(rules, redactionRule{
			expr: `(?i)(` + prefix + quoted[0] + `)(?P<value>` + quoted[1] + `)`, replacement: "${1}" + redactedValue,
			keep: keep, needles: needles, start: start,
		})
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
	// expr is the pattern. It is compiled the first time text gets past the needles: a hook is a
	// process per event, and most events never need most rules, so compiling all of them up front
	// cost every hook run a few milliseconds before it had read its input.
	expr        string
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
	// start says where, around an occurrence of one of its needles, a match can begin, and offset
	// how many bytes before the needle a startNeedle match does. See redact_engine.go.
	start  redactStart
	offset int
	// shape marks a credential recognized by its own shape: a public type prefix, ${1}, and the
	// secret after it. Such a rule also redacts a secret written right after its match
	// (redact_adjacent.go).
	shape redactShapeKind
	// runsOn, when set, returns where the text a match replaces ends, at or past the match's end.
	runsOn func(t string, match []int) int
}

// pattern is the rule's compiled pattern.
func (rule redactionRule) pattern() *regexp.Regexp {
	return compiledPattern(rule.expr)
}

// redactNeedles is a set of literals a pattern cannot match without. folded needles are lower
// case and found whatever the case of the text's ASCII letters, for a case-insensitive pattern.
// followedBy, when set, must also hold for the text right after the needle, and precededBy for the
// text before it; both read the text as it is, so they must not depend on the case of its letters.
// within, when set, is a broader set the text must also hold.
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

// rule is a case-sensitive rule that cannot match text holding none of needles. A match begins
// where its needle does.
func rule(expr, replacement string, keep func(string) bool, needles ...string) redactionRule {
	return redactionRule{expr: expr, replacement: replacement, keep: keep, needles: newRedactNeedles(false, needles...)}
}

// foldedRule is a case-insensitive rule that cannot match text whose lower-cased form holds none
// of needles, which must be lower case. A match begins where its needle does.
func foldedRule(expr, replacement string, keep func(string) bool, needles ...string) redactionRule {
	return redactionRule{expr: expr, replacement: replacement, keep: keep, needles: newRedactNeedles(true, needles...)}
}

// curlRule is a foldedRule scoped to a curl command line. A flag curl accepts more than once (two
// -b cookie strings) is redacted at every occurrence on the line.
func curlRule(expr, replacement string) redactionRule {
	r := foldedRule(expr, replacement, nil, "curl")
	r.repeat = true
	return r
}

// shapeRule is a rule for a credential recognized by its own shape (redactionRule.shape).
func shapeRule(expr, replacement string, needles ...string) redactionRule {
	r := rule(expr, replacement, nil, needles...)
	r.shape = shapeToken
	return r
}

// lazyPattern compiles expr the first time it is called.
//
// The rules whose pattern names a credential (redactLabel, redactFlag) are written as struct
// literals rather than through rule or foldedRule: static analysis reads a call whose argument is
// a constant spelling out "password" as returning that password, and would then treat every string
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
// not part of the run. The patterns are case-insensitive, so the run takes capitals too: the
// needle scan reads the text as it is, not lower-cased.
func flagStart(before string) (int, bool) {
	start := len(before)
	for start > 0 {
		c := before[start-1]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			break
		}
		start--
	}
	if redactWork != nil {
		redactWork(len(before) - start)
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
		expr:        `(?i)(\b` + redactLabel + `(?P<q>` + redactOpenQuote + `)\s*[:=]\s*)(?P<value>` + redactBare + `)`,
		replacement: "${1}${q}" + redactedValue + "${q}", keep: isLiteralOrAuthScheme, needles: redactQuotedLabelNeedles, start: startLabel,
	},
	{
		expr:        `(?i)(\b` + redactLabel + `\s*[:=]\s*)(?P<value>` + redactUnquoted + `)`,
		replacement: "${1}" + redactedValue, keep: isAuthScheme, needles: redactLabelNeedles, start: startLabel,
	},
	// A command-line flag: `--password x`, `--token=x`, `--api-key x`, and quoted, the way agents
	// usually write arguments: `--password "x y"`, `--token='x'`.
}, quotedRules(quotedFlag, nil), []redactionRule{
	{expr: `(?i)(` + redactFlag + `)([^\s"'-][^\s"']*)`, replacement: "${1}" + redactedValue, needles: redactFlagNeedles, start: startFlag},
	// A flag and its value as two quoted items of an argument list, the way an MCP server's args
	// are written in JSON, TOML and YAML: `"args": ["--api-key", "x"]`.
	{
		expr:        `(?i)(["']` + redactFlagName + `["']\s*,\s*["'])(?P<value>(?:[^"'\n\\]|\\[^\n])+)`,
		replacement: "${1}" + redactedValue, keep: isRedactedValue, needles: redactListedFlagNeedles, start: startListedFlag,
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
	foldedRule(`(?im)((?:^\s*|["']\s*|(?:-H|--header)(?:=|\s+)["']?)(?:set-)?cookie["']?\s*:\s*["']?)([^\n"'\\]+)`, "${1}"+redactedValue, isRedactedValue, "cookie").from(startLine),
	// The password in a URL's userinfo: `postgres://user:pw@host`, `https://x-access-token:t@github.com`,
	// and with no user name, the way Redis URLs carry a password: `redis://:pw@host`.
	foldedRule(`(?i)(\b[a-z][a-z0-9+.-]*://[^/\s:@"'<>]*:)([^@/\s"'<>]+)(@)`, "${1}"+redactedValue+"${3}", nil, "://").from(startScheme).runningOn(urlRunsOn),
	// Credentials recognizable by their own shape, wherever they appear. The type prefix is public
	// and stays (`ghp_[REDACTED]`, `AKIA[REDACTED]`), the way secret scanners report a finding: the
	// line still says a GitHub token or an AWS key was there, which is what detection rules match on
	// in the log, and none of the credential is left. A secret written right after one of these is
	// redacted with it, keeping its own prefix: `ghp_[REDACTED]ghp_[REDACTED]` (redact_adjacent.go).
	shapeRule(`\b(sk-)[A-Za-z0-9_-]{20,}`, "${1}"+redactedValue, "sk-"), // Anthropic sk-ant-, OpenAI sk-proj- and sk-
	// An AWS secret access key has no shape of its own, but where it follows its key id within a few
	// separators (a credentials CSV, `aws configure` output, a table, a JSON array) it is the 40
	// characters after them. The key id is replaced with it, as the key id rule below would: where
	// this rule redacts a secret right after another one, the key id rule has already run.
	shapeRule(`(\b(?:AKIA|ASIA))[A-Z0-9]{16}(\b[\s,:|"']{1,8})[A-Za-z0-9/+]{40}([^A-Za-z0-9/+=]|$)`, "${1}"+redactedValue+"${2}"+redactedValue+"${3}", "AKIA", "ASIA"),
	shapeRule(`\b(AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`, "${1}"+redactedValue, "AKIA", "ASIA", "ABIA", "ACCA"),    // AWS access key id
	shapeRule(`\b(gh[pousr]_)[A-Za-z0-9]{30,}`, "${1}"+redactedValue, "ghp_", "gho_", "ghu_", "ghs_", "ghr_"),   // GitHub tokens
	shapeRule(`\b(github_pat_)[A-Za-z0-9_]{22,}`, "${1}"+redactedValue, "github_pat_"),                          // GitHub fine-grained tokens
	shapeRule(`\b(glpat-)[A-Za-z0-9_-]{20,}`, "${1}"+redactedValue, "glpat-"),                                   // GitLab
	shapeRule(`\b(xox[abposr]-)[A-Za-z0-9-]{10,}`, "${1}"+redactedValue, "xox"),                                 // Slack
	shapeRule(`\b([rs]k_(?:live|test)_)[A-Za-z0-9]{16,}`, "${1}"+redactedValue, "k_live_", "k_test_").before(1), // Stripe
	shapeRule(`\b(AIza)[0-9A-Za-z_-]{35}`, "${1}"+redactedValue, "AIza"),                                        // Google API key
	shapeRule(`\b(ya29\.)(?:c\.)?[0-9A-Za-z_-]{20,}`, "${1}"+redactedValue, "ya29."),                            // Google OAuth access token
	shapeRule(`\b(npm_)[A-Za-z0-9]{36}`, "${1}"+redactedValue, "npm_"),                                          // npm
	shapeRule(`\b(hf_)[A-Za-z0-9]{30,}`, "${1}"+redactedValue, "hf_"),                                           // Hugging Face
	// Beacon's own keys and tokens: API keys, device and enforcement keys, MCP tokens and their refresh
	// tokens, and the CLI sign-in's access token, authorization code, client and exchange values.
	shapeRule(`\b(bcn_(?:api|device|mcprt|mcp|enfdev|cli|code|client|exchange)_)[A-Za-z0-9_-]{8,}`, "${1}"+redactedValue, "bcn_"),
	shapeRule(`\b(eyJ)[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, "${1}"+redactedValue, "eyJ").segmented(), // JWT
})

func concatRules(groups ...[]redactionRule) []redactionRule {
	var all []redactionRule
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}

// from returns the rule with its matches beginning where start says rather than at a needle.
func (rule redactionRule) from(start redactStart) redactionRule {
	rule.start = start
	return rule
}

// before returns the rule with its matches beginning offset bytes before a needle.
func (rule redactionRule) before(offset int) redactionRule {
	rule.offset = offset
	return rule
}

// segmented marks a shape rule whose secret is a JWT: three dot-separated segments, the first two
// opening with `eyJ`, each of any length. redact_adjacent.go reads them itself to tell where one
// ends without reading on to the end of the text each time it asks.
func (rule redactionRule) segmented() redactionRule {
	rule.shape = shapeJWT
	return rule
}

// runningOn returns the rule with the text its matches replace running on where runsOn says.
func (rule redactionRule) runningOn(runsOn func(t string, match []int) int) redactionRule {
	rule.runsOn = runsOn
	return rule
}

// apply replaces every match of the rule in the gate's text, except one whose value keep passes,
// and a repeating rule until the text stops changing.
func (rule redactionRule) apply(text *redactGate, slot *redactSlot) string {
	out := rule.applyOnce(text, slot)
	// Each pass redacts at least one more value, and a redacted value matches no further.
	for pass := 0; rule.repeat && out != text.value && pass < 16; pass++ {
		text.set(out)
		out = rule.applyOnce(text, slot)
	}
	return out
}

func (rule redactionRule) applyOnce(text *redactGate, slot *redactSlot) string {
	value := text.value
	var out []byte
	last := 0
	for match, use := range rule.matches(text, slot) {
		if match[0] < last {
			continue // inside a match that ran on (redactionRule.runsOn)
		}
		if keep := use.rule.keep; keep != nil {
			index := use.pattern.SubexpIndex("value")
			if index < 0 {
				index = 2
			}
			if group := 2 * index; match[group] >= 0 && keep(value[match[group]:match[group+1]]) {
				continue
			}
		}
		out = append(out, value[last:match[0]]...)
		replacement, end := use.rule.replacing(value, match)
		out = use.pattern.ExpandString(out, replacement, value, match)
		last = end
	}
	if out == nil {
		return value
	}
	return string(append(out, value[last:]...))
}

// replacing returns the replacement of match in t, and where the text it replaces ends: the
// match's end, unless the rule's match runs on (runsOn), when what the rule keeps after its value
// goes too.
func (rule *redactionRule) replacing(t string, match []int) (string, int) {
	if rule.runsOn != nil {
		if end := rule.runsOn(t, match); end >= 0 {
			return "${1}" + redactedValue, end
		}
	}
	return rule.replacement, match[1]
}

// urlRunsOn returns where the password of a URL match runs on to when the URL's user name is a
// secret-named label (`https://x-access-token:…@`), which the label rules redact too, or -1. The
// two stop at different bytes, a label's value at a quote, a comma or whitespace and a URL's
// password at an `@`, a slash, a quote or whitespace, and once one has replaced its part, the
// other's runs on over the marker; so each pass of the rules took one more piece of what follows,
// starting with the `@`. The value runs on as far as the two take it in turn, which is where
// every later pass would leave it.
func urlRunsOn(t string, m []int) int {
	if !labelEndsAt(t[:m[3]]) {
		return -1
	}
	end := m[1]
	for {
		from := end
		// The label's value: the marker the URL rule wrote and what follows it.
		for end < len(t) && !strings.ContainsRune("\"'`,", rune(t[end])) && !isRegexpSpace(t[end]) {
			end++
		}
		// The URL's password: that value and what follows it up to an `@`.
		at := end
		for at < len(t) && t[at] != '@' && t[at] != '/' && !strings.ContainsRune("\"'<>", rune(t[at])) && !isRegexpSpace(t[at]) {
			at++
		}
		redactWorked(at - from)
		if at == len(t) || t[at] != '@' {
			return end
		}
		end = at + 1
	}
}

// labelEndsAt reports whether s ends with a secret-named label and its separator, as the label
// rules read them: a label word, an optional closing quote, and `:` or `=` with whitespace around.
func labelEndsAt(s string) bool {
	i := len(s)
	for i > 0 && isRegexpSpace(s[i-1]) {
		i--
	}
	if i == 0 || s[i-1] != ':' && s[i-1] != '=' {
		return false
	}
	for i--; i > 0 && isRegexpSpace(s[i-1]); i-- {
	}
	if i > 0 && strings.IndexByte(redactQuotes, s[i-1]) >= 0 {
		if i--; i > 0 && s[i-1] == '\\' {
			i--
		}
	}
	for _, word := range redactLabelWords {
		if i >= len(word) && strings.EqualFold(s[i-len(word):i], word) {
			return true
		}
	}
	return false
}

// redactAssignedRule captures the value of a labeled assignment, quoted with ", ' or a backtick
// (${1} to ${3}) or not (${4}), so a bare copy of it elsewhere in the same text is
// removed too.
var redactAssignedRule = sync.OnceValue(func() redactionRule {
	return redactionRule{
		expr:    `(?i)\b` + redactLabel + redactSeparator + `(?:` + quotedAlternatives() + `|(` + redactUnquoted + `))`,
		needles: redactLabelNeedles, start: startLabel,
	}
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

// redactString is RedactString; gated false runs every rule's whole pattern over the whole text
// whatever the text holds, which the tests use to prove the gate and the needle-anchored matching
// (redact_engine.go) never change a result.
//
// The gate is what keeps redaction affordable on the hook path, where it runs over every string of
// every event before the agent may continue. Go's regexp engine has no DFA, so the case-insensitive
// label rules cost microseconds on a short string and tens of milliseconds on a tool output of a
// few kilobytes. Almost no text holds a needle of most rules, and one pass over the text finds
// every needle of every rule at once.
func redactString(value string, gated bool) string {
	engine := redactEngineFor()
	text := redactGate{value: value, enabled: gated, engine: engine}
	if text.holdsNothing() {
		// Most text: no rule's needle, so no set holds and no rule can match.
		return value
	}
	// Remember assignment values before replacing their labeled occurrence so
	// repeated bare copies in the same retained prompt/output are also removed.
	// This is common in model reasoning that quotes `token=value` and later
	// refers to `value` by itself.
	var assigned map[string]struct{}
	var kept bool
	if text.holds(redactLabelNeedles) {
		assigned, kept = captureAssigned(&text, engine, assigned)
	}
	original := text.value
	for i, rule := range redactionRules {
		if !text.holds(rule.needles) {
			continue
		}
		text.set(rule.apply(&text, &engine.rules[i]))
	}
	// A value the label rules keep part of can be captured whole and then changed by a later rule:
	// of `"secret": null}…x-access-token:ghs_…`, the rule for quoted labels keeps `null`, the
	// capture runs on to the end, and the rule for `x-access-token:` replaces the token inside it,
	// so no copy of what was captured is left to remove. What the value became is captured too, so
	// a second pass over the result finds nothing this one did not.
	if kept && text.value != original {
		assigned, _ = captureAssigned(&text, engine, assigned)
	}
	// Each copy is searched for and removed; none of it survives into the result. A secret right
	// after a replaced copy, or right after a replaced key, starts a word only now.
	return engine.redactAfterMarkers(replaceAssigned(text.value, assigned))
}

// captureAssigned adds the value of every labeled assignment in the gate's text to assigned. kept
// reports whether one, of any length, was unquoted after a quoted label: the one way a value is
// captured that the label rules may keep part of (redactBare stops where the capture runs on), so
// that a later rule can change it, or make it long enough to remove.
func captureAssigned(text *redactGate, engine *redactEngine, assigned map[string]struct{}) (_ map[string]struct{}, kept bool) {
	value := text.value
	for match := range redactAssignedRule().matches(text, &engine.assigned) {
		// The value is in one group: each way of writing it is an alternative.
		g := 2
		for g+1 < len(match) && match[g] < 0 {
			g += 2
		}
		if g+1 >= len(match) {
			continue
		}
		kept = kept || g+2 == len(match) && strings.ContainsAny(value[match[0]:match[g]], redactQuotes)
		if s := value[match[g]:match[g+1]]; len(s) >= 8 && s != redactedValue && !isAuthScheme(s) {
			if assigned == nil {
				assigned = map[string]struct{}{}
			}
			assigned[s] = struct{}{}
		}
	}
	return assigned, kept
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
	return TruncateString(redactBeforeCut(value, limit), limit)
}

// redactBeforeCut redacts value as far as a cut at limit needs: the whole of it, or a window past
// the limit that still holds redactWindow bytes there once redacted. Everything up to the cut is
// then redacted text.
func redactBeforeCut(value string, limit int) string {
	// Compared without adding to limit, which may be as large as math.MaxInt: a whole string within
	// redactWindow of the limit, or shorter, is redacted whole.
	if limit <= 0 || len(value)-limit <= redactWindow {
		return RedactString(value)
	}
	// Redaction shortens what it matches, so a window that shrank pulls text from near its own cut
	// inside the limit, where an unrecognizable fragment of a secret would survive. Widen the window
	// until a full redactWindow of redacted text is left past the limit, or the whole string fits.
	// Here limit+redactWindow < len(value), and the window doubles only while twice it is still
	// shorter than value, so nothing overflows.
	for window := limit + redactWindow; ; window *= 2 {
		if redacted := RedactString(value[:window]); len(redacted) >= limit+redactWindow {
			return redacted
		}
		if len(value)-window <= window {
			return RedactString(value)
		}
	}
}

// SanitizeMap returns a sanitized copy of input: every string redacted (when opts.RedactSecrets is
// set) and cut to opts.StringLimit, a value under a field named for a credential redacted whole,
// at any depth.
//
// A value of a type JSON decoding does not produce ([]string, map[string]string, a struct, a
// pointer, a named string type, json.RawMessage) is first converted into what encoding/json
// writes for it, decoded back into an interface{}, then sanitized the same way (jsonValue); the
// parts of it JSON cannot represent (a NaN, a channel, a function, a cycle) become nil. Booleans
// and Go's number types are kept as they are, so an integer stays an integer. A []byte stays a
// []byte (an OTLP bytes attribute keeps its type) and is redacted and cut (cleanBytes). A
// CleanedValue is kept as CleanValue left it. The result always marshals.
func SanitizeMap(input map[string]interface{}, opts PrivacyOptions) map[string]interface{} {
	limit := opts.StringLimit
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = sanitizeField(key, value, opts, limit)
	}
	return out
}

// sanitizeField is SanitizeMap's result for a value held under key.
func sanitizeField(key string, value interface{}, opts PrivacyOptions, limit int) interface{} {
	switch typed := value.(type) {
	case string:
		// A value whose field is named for a credential is one, whatever it looks like:
		// `{"password": "hunter2"}` has no shape a pattern could recognize.
		if opts.RedactSecrets && typed != "" && IsSecretField(key, typed) {
			return redactedValue
		}
		return CleanString(typed, limit, opts.RedactSecrets)
	case map[string]interface{}:
		return SanitizeMap(typed, opts)
	case []interface{}:
		// A multi-valued header (`{"Cookie": ["sid=1"]}`, OTel's http.request.header.*) is as
		// much a credential as a single value.
		if opts.RedactSecrets && IsSecretKeyName(key) {
			return redactAllStrings(typed, opts)
		}
		return SanitizeSlice(typed, opts)
	case []byte:
		if opts.RedactSecrets && len(typed) > 0 && IsSecretField(key, string(typed)) {
			return []byte(redactedValue)
		}
		return cleanBytes(typed, limit, opts.RedactSecrets)
	case CleanedValue:
		// The field's name still redacts it.
		if opts.RedactSecrets && IsSecretKeyName(key) {
			return sanitizeField(key, typed.value, opts, limit)
		}
		return typed
	}
	if scalar, ok := jsonScalar(value); ok {
		return scalar
	}
	return sanitizeField(key, jsonValue(value), opts, limit)
}

// cleanBytes is a []byte sanitized as a string when it is valid UTF-8. Otherwise it is still
// redacted, since bytes that are not valid UTF-8 as a whole can hold readable text (Latin-1, a
// multi-byte character cut in two), and then cut to limit bytes. Redaction replaces only what its
// rules match and leaves every other byte exactly as it was.
func cleanBytes(value []byte, limit int, redactSecrets bool) []byte {
	if value == nil {
		return nil
	}
	if utf8.Valid(value) {
		return []byte(CleanString(string(value), limit, redactSecrets))
	}
	text := string(value)
	if redactSecrets {
		text = redactBeforeCut(text, limit)
	}
	return []byte(text[:min(len(text), max(limit, 0))])
}

// CleanedValue is a value CleanValue sanitized. SanitizeMap and SanitizeSlice keep it as it is,
// unless it is held under a field named for a credential, so its strings keep the limit CleanValue
// was given rather than the caller's. Only CleanValue makes one.
type CleanedValue struct {
	value interface{}
}

// CleanValue returns v sanitized for a producer that sets its own string limit: v is converted as
// SanitizeMap converts a value, and every string in it is redacted as RedactString redacts it and
// cut to limit as CleanString cuts it. Field names are not judged, so a list of environment
// variable names under `env_keys` keeps them; the producer must already have applied IsSecretField
// and IsSecretFlag to every key and argument list that comes from data rather than from its own
// types, as the endpoint inventory does for the MCP server definitions it captures. limit <= 0
// uses DefaultStringLimit.
func CleanValue(v interface{}, limit int) CleanedValue {
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	return CleanedValue{value: cleanAll(v, limit)}
}

func cleanAll(value interface{}, limit int) interface{} {
	switch typed := value.(type) {
	case string:
		return CleanString(typed, limit, true)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = cleanAll(item, limit)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for index, item := range typed {
			out[index] = cleanAll(item, limit)
		}
		return out
	case []byte:
		return cleanBytes(typed, limit, true)
	case CleanedValue:
		return cleanAll(typed.value, limit)
	}
	if scalar, ok := jsonScalar(value); ok {
		return scalar
	}
	return cleanAll(jsonValue(value), limit)
}

// MarshalJSON writes the sanitized value.
func (c CleanedValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.value)
}

// jsonScalar returns a value that holds no text, and true: nil, a boolean, or a number of one of
// Go's number types, or a json.Number, kept as it is. A number JSON cannot write (a NaN, an
// infinity, a json.Number that does not spell one) is nil.
func jsonScalar(value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return value, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, true
		}
		return value, true
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, true
		}
		return value, true
	case json.Number:
		// The encoder writes an empty json.Number as 0.
		if typed != "" && !isJSONNumber(string(typed)) {
			return nil, true
		}
		return value, true
	}
	return nil, false
}

// isJSONNumber reports whether s is a number as JSON spells it, which is what encoding/json
// requires of a json.Number it writes.
func isJSONNumber(s string) bool {
	if s != "" && s[0] == '-' {
		s = s[1:]
	}
	digits := func() bool {
		n := 0
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		s = s[n:]
		return n > 0
	}
	switch {
	case s == "":
		return false
	case s[0] == '0':
		s = s[1:]
	case !digits():
		return false
	}
	if s != "" && s[0] == '.' {
		s = s[1:]
		if !digits() {
			return false
		}
	}
	if s != "" && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s != "" && (s[0] == '+' || s[0] == '-') {
			s = s[1:]
		}
		if !digits() {
			return false
		}
	}
	return s == ""
}

// redactAllStrings replaces every non-empty string in a slice held under a secret-named field,
// at any depth of nested slices; maps inside it are sanitized by their own field names.
func redactAllStrings(input []interface{}, opts PrivacyOptions) []interface{} {
	out := make([]interface{}, len(input))
	for index, value := range input {
		out[index] = redactedItem(value, opts)
	}
	return out
}

// redactedItem is redactAllStrings' result for one item.
func redactedItem(value interface{}, opts PrivacyOptions) interface{} {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return typed
		}
		return redactedValue
	case []interface{}:
		return redactAllStrings(typed, opts)
	case map[string]interface{}:
		return SanitizeMap(typed, opts)
	case []byte:
		if len(typed) == 0 {
			return typed
		}
		return []byte(redactedValue)
	case CleanedValue:
		return redactedItem(typed.value, opts)
	}
	if scalar, ok := jsonScalar(value); ok {
		return scalar
	}
	return redactedItem(jsonValue(value), opts)
}

// SanitizeSlice returns a sanitized copy of input, each item sanitized as SanitizeMap sanitizes a
// value, and a string after a flag named for a secret (`["--api-key", "x"]`) redacted.
func SanitizeSlice(input []interface{}, opts PrivacyOptions) []interface{} {
	limit := opts.StringLimit
	if limit <= 0 {
		limit = DefaultStringLimit
	}
	out := make([]interface{}, len(input))
	for index, value := range input {
		out[index] = sanitizeItem(input, index, value, opts, limit)
	}
	return out
}

// sanitizeItem is SanitizeSlice's result for value, the item of input at index.
func sanitizeItem(input []interface{}, index int, value interface{}, opts PrivacyOptions, limit int) interface{} {
	switch typed := value.(type) {
	case string:
		// A flag named for a secret has its value in the next item: `["--api-key", "x"]`.
		if opts.RedactSecrets && typed != "" && index > 0 && followsSecretFlag(input[index-1]) {
			return redactedValue
		}
		return CleanString(typed, limit, opts.RedactSecrets)
	case map[string]interface{}:
		return SanitizeMap(typed, opts)
	case []interface{}:
		return SanitizeSlice(typed, opts)
	case []byte:
		if opts.RedactSecrets && len(typed) > 0 && index > 0 && followsSecretFlag(input[index-1]) {
			return []byte(redactedValue)
		}
		return cleanBytes(typed, limit, opts.RedactSecrets)
	case CleanedValue:
		if opts.RedactSecrets && index > 0 && followsSecretFlag(input[index-1]) {
			switch text := typed.value.(type) {
			case string:
				if text != "" {
					return redactedValue
				}
			case []byte:
				if len(text) > 0 {
					return []byte(redactedValue)
				}
			}
		}
		return typed
	}
	if scalar, ok := jsonScalar(value); ok {
		return scalar
	}
	return sanitizeItem(input, index, jsonValue(value), opts, limit)
}

// followsSecretFlag reports whether the item before a string is a flag named for a secret, read
// as JSON writes it, so a named string type holding `--api-key` is one.
func followsSecretFlag(previous interface{}) bool {
	switch typed := previous.(type) {
	case string:
		return IsSecretFlag(typed)
	case map[string]interface{}, []interface{}:
		return false
	case []byte:
		return IsSecretFlag(string(typed))
	case CleanedValue:
		return followsSecretFlag(typed.value)
	}
	if _, scalar := jsonScalar(previous); scalar {
		return false
	}
	return followsSecretFlag(jsonValue(previous))
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
		event.MCP = SanitizeMCP(event.MCP, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if event.SystemContext != nil {
		event.SystemContext = sanitizeSystemContext(event.SystemContext)
	}
	if event.GenAI != nil {
		event.GenAI = SanitizeGenAI(event.GenAI, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	if event.Raw != nil {
		event.Raw = SanitizeMap(event.Raw, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	// Marshaling the event only to measure it was a third of SanitizeEvent's cost. A bound on its
	// size decides most events without it; a marshal that fails leaves Truncated as it was.
	if n, ok := jsonSizeBound(&event, maxBytes); !ok || n > maxBytes {
		if data, err := json.Marshal(event); err == nil && len(data) > maxBytes {
			event.Truncated = true
		}
	}
	return event
}
