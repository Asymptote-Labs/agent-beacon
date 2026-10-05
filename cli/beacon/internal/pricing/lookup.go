package pricing

import (
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Match names the spelling rung at which a model matched a catalog key.
type Match string

const (
	// MatchExact: the model string, trimmed, is a catalog key.
	MatchExact Match = "exact"
	// MatchCanonical: equal after Beacon's own NormalizeModelName on both sides, which
	// lowercases and drops a "/"-separated provider prefix (anthropic/, openai/, gemini/,
	// vertex_ai/, openrouter/google/, ...).
	MatchCanonical Match = "canonical"
	// MatchDottedVersion: equal after also writing a dot between two digits as a dash, so
	// Copilot's claude-sonnet-4.6 meets Anthropic's claude-sonnet-4-6.
	MatchDottedVersion Match = "dotted_version"
	// MatchProviderPrefix: equal after also dropping Bedrock's dotted route prefixes (a
	// us./eu./apac./au./jp./global./us-gov. inference-profile region, then anthropic.) and
	// the -v1:0 model-version suffix they come with.
	MatchProviderPrefix Match = "provider_prefix"
)

// Reductions Lookup may apply to the model string, in this order, before retrying every rung.
const (
	// StrippedContextWindow: a trailing context-window marker such as Claude Code's [1m].
	// The marker names the context size the client asked for; the price of a long prompt is
	// the model's long-context band, which Cost applies from the token counts.
	StrippedContextWindow = "context_window"
	// StrippedDateSnapshot: a trailing snapshot date (-20250929, @20250929, -2025-08-07),
	// priced as the undated alias.
	StrippedDateSnapshot = "date_snapshot"
	// StrippedEffortSuffix: a trailing reasoning-effort label (-minimal, -low, -medium,
	// -high, -xhigh, -thinking) that some runtimes append to the model name. Effort changes
	// how many tokens are spent, not what each costs.
	StrippedEffortSuffix = "effort_suffix"
)

// Values of Resolution.Source: which price list the rates came from.
const (
	SourceCatalog  = "catalog"
	SourceOverride = "override"
)

// Resolution is the outcome of Lookup.
type Resolution struct {
	// Model is the string that was looked up.
	Model string
	// Key is the entry that priced it, and Rates its entry. Empty when unpriced. Source says
	// whether Key is a catalog key (SourceCatalog) or a model in an overrides file
	// (SourceOverride).
	Key    string
	Rates  Rates
	Source string
	// Alias is set when the model matched an alias in an overrides file rather than a priced
	// entry; Key is then the alias's target. Match and Stripped describe how the model met the
	// alias, since the alias-to-target step is always exact.
	Alias string
	// Match is the spelling rung that matched; Stripped lists the reductions that were
	// needed first, in the order applied. Together they say how loose the match was.
	Match    Match
	Stripped []string
	// Candidates is set when the model was left unpriced because several entries matched at
	// the same rung with different rates. When the overrides decided the tie, they are names
	// from the overrides file.
	Candidates []string
}

// table is a set of priced names and the spelling indexes Lookup runs over. The catalog is one;
// an overrides file is another.
type table struct {
	models map[string]Rates
	keys   []string
	// index[r] maps a key spelled at rung r to every key with that spelling.
	index [numRungs]map[string][]string
}

func newTable(models map[string]Rates) *table {
	t := &table{models: models}
	for key := range models {
		t.keys = append(t.keys, key)
	}
	sort.Strings(t.keys)
	for r := range t.index {
		t.index[r] = make(map[string][]string, len(t.keys))
	}
	for _, key := range t.keys {
		for r := rung(0); r < numRungs; r++ {
			spelled := spell(r, key)
			if spelled == "" {
				continue
			}
			t.index[r][spelled] = append(t.index[r][spelled], key)
		}
	}
	return t
}

type rung int

const (
	rungExact rung = iota
	rungCanonical
	rungDotted
	rungProvider
	numRungs
)

var rungMatch = [numRungs]Match{MatchExact, MatchCanonical, MatchDottedVersion, MatchProviderPrefix}

// spell writes s the way rung r compares it. Each rung includes the ones before it. Spelling
// rungs are applied to the model and the catalog key alike: they are different ways of writing
// one name. The reductions in Lookup are applied only to the model, because they turn a
// specific name into a more general one and the reverse would be invention.
func spell(r rung, s string) string {
	s = strings.TrimSpace(s)
	if r == rungExact {
		return s
	}
	s = asymptoteobserve.NormalizeModelName(s)
	if r >= rungProvider {
		s = stripRoutePrefix(s)
	}
	if r >= rungDotted {
		s = foldVersionDots(s)
	}
	return s
}

// bedrockRegions are the inference-profile prefixes Bedrock puts in front of a model id.
var bedrockRegions = []string{"us-gov.", "global.", "apac.", "us.", "eu.", "au.", "jp."}

var bedrockVersionSuffix = regexp.MustCompile(`-v\d+(:\d+)?$`)

// stripRoutePrefix removes a Bedrock route from an already-canonical name:
// us.anthropic.claude-sonnet-4-5-20250929-v1:0 becomes claude-sonnet-4-5-20250929. Only
// Anthropic's vendor prefix is removed, because it is the only Bedrock family the catalog keeps
// and the others (deepseek.v3.2) do not leave a model name behind.
func stripRoutePrefix(s string) string {
	rest := s
	for _, region := range bedrockRegions {
		if strings.HasPrefix(rest, region) {
			rest = rest[len(region):]
			break
		}
	}
	if !strings.HasPrefix(rest, "anthropic.") {
		return s
	}
	rest = strings.TrimPrefix(rest, "anthropic.")
	return bedrockVersionSuffix.ReplaceAllString(rest, "")
}

// foldVersionDots rewrites a dot between two digits as a dash. Only between digits, so a
// dotted route prefix survives for the rung that understands it.
func foldVersionDots(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	b := []byte(s)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && isDigit(s[i-1]) && isDigit(s[i+1]) {
			b[i] = '-'
		}
	}
	return string(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

var reductions = []struct {
	name string
	re   *regexp.Regexp
}{
	{StrippedContextWindow, regexp.MustCompile(`\[\d+[km]\]$`)},
	{StrippedDateSnapshot, regexp.MustCompile(`([-@]\d{8}|-\d{4}-\d{2}-\d{2})$`)},
	{StrippedEffortSuffix, regexp.MustCompile(`-(minimal|low|medium|high|xhigh|thinking)$`)},
}

// Lookup resolves what a runtime called a model to one catalog entry.
//
// The model is tried at four spelling rungs -- exact, canonical, dotted version, provider
// prefix (see the Match constants) -- and the first rung with any match decides. If that fails,
// trailing decorations are stripped one at a time (context-window marker, then date snapshot,
// then effort suffix, repeated until none is left) and all four rungs are tried again after each. There is no substring,
// prefix or edit-distance matching: claude-4.5-sonnet is not claude-sonnet-4-5, and a guess
// that prices the wrong model is worse than no price.
//
// When several catalog keys match at the deciding rung, they must agree on every rate (as
// claude-opus-4-6 does on Anthropic and on Vertex AI); otherwise the model is left unpriced and
// Resolution.Candidates names the disagreeing keys. Lookup does not fall through to a looser
// rung after a tie, since a looser rung can only match more keys.
func (c *Catalog) Lookup(model string) (Resolution, bool) {
	res, ok := c.table.lookup(model)
	if ok || len(res.Candidates) > 0 {
		res.Source = SourceCatalog
	}
	return res, ok
}

func (c *table) lookup(model string) (Resolution, bool) {
	res := Resolution{Model: model}
	query := strings.TrimSpace(model)
	if query == "" {
		return res, false
	}
	if priced, decided := c.matchSpellings(query, &res); decided {
		return res, priced
	}
	current := asymptoteobserve.NormalizeModelName(query)
	// Passes repeat until none of the reductions applies, because a runtime can stack
	// decorations in any order: claude-sonnet-4-6-20260101-high only exposes its date once the
	// effort suffix is off. Every strip shortens the name, so the loop ends.
	for stripped := true; stripped; {
		stripped = false
		for _, red := range reductions {
			reduced := red.re.ReplaceAllString(current, "")
			if reduced == current || reduced == "" {
				continue
			}
			current = reduced
			stripped = true
			res.Stripped = append(res.Stripped, red.name)
			if priced, decided := c.matchSpellings(current, &res); decided {
				return res, priced
			}
		}
	}
	res.Stripped = nil
	return res, false
}

// matchSpellings runs the spelling rungs for one query. decided reports that some rung
// matched; priced reports that its candidates agreed on a price.
func (c *table) matchSpellings(query string, res *Resolution) (priced, decided bool) {
	for r := rung(0); r < numRungs; r++ {
		spelled := spell(r, query)
		keys := c.index[r][spelled]
		if len(keys) == 0 {
			continue
		}
		first := c.models[keys[0]]
		for _, k := range keys[1:] {
			if !samePrice(first, c.models[k]) {
				res.Candidates = append([]string(nil), keys...)
				res.Match = rungMatch[r]
				return false, true
			}
		}
		res.Key = preferredKey(keys, query)
		res.Rates = c.models[res.Key]
		res.Match = rungMatch[r]
		return true, true
	}
	return false, false
}

// preferredKey picks which of several equally priced keys to report: the one spelled exactly
// like the query if there is one, else the shortest (the bare model id over a routed copy),
// else the first in sort order. keys is already sorted.
func preferredKey(keys []string, query string) string {
	canonical := asymptoteobserve.NormalizeModelName(query)
	best := keys[0]
	for _, k := range keys {
		if k == query || k == canonical {
			return k
		}
		if len(k) < len(best) {
			best = k
		}
	}
	return best
}

func samePrice(a, b Rates) bool {
	return a.RateSet == b.RateSet && reflect.DeepEqual(a.Bands, b.Bands)
}
