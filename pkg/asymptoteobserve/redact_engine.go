package asymptoteobserve

import (
	"iter"
	"math/bits"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// This file is how redaction finds the matches of its rules (privacy.go). The rules, their order,
// their patterns and their replacements are unchanged; what changed is how a rule's matches are
// found, without changing which:
//
//   - The gate used to lower-case the text and run a substring search per needle set. On a few
//     kilobytes of tool output or source code that search was nearly all of redaction's cost. One
//     pass of an Aho-Corasick automaton over the text, folding ASCII case as it reads, now finds
//     every occurrence of every needle of every rule (needleScanner).
//   - A rule's pattern used to run over the whole text once the gate let it through. Go's regexp
//     has no DFA, so that cost tens of nanoseconds a byte for the case-insensitive label rules. A
//     match always holds one of the rule's needles, at a place the rule's start names, so the
//     pattern now runs anchored at those places only, which finds exactly the matches the whole
//     pattern does (see redactionRule.matches).
//   - Redaction stays linear in the text whatever the text holds. A pattern anchored at one place
//     can read on to the end of its line or run, so many places on one long line could cost a
//     quadratic amount; past a budget a rule runs its whole pattern once instead, from a start
//     where it finds the same matches (redactSlot.safeStart). TestRedactionWorkIsLinear counts the
//     work on texts built for the worst case.
//
// Text holding a character Go's case folding equates with an ASCII letter (U+017F folds to s,
// U+212A to k) can match a case-insensitive pattern where no needle occurs, so on such text the
// case-insensitive rules run their whole patterns, as they always did. redactString(value, false)
// runs every rule's whole pattern on any text, and TestRedactGateNeverChangesAResult and
// FuzzRedactGate hold the two to the same result.

// redactStart says where, around an occurrence of one of a rule's needles that passes the needle
// set's precededBy and followedBy (and those of the sets it is within), a match of the rule's
// pattern can begin.
type redactStart uint8

const (
	// startNeedle: the rule's offset bytes before the needle, at a word boundary when the pattern
	// opens with \b.
	startNeedle redactStart = iota
	// startLabel: at a word boundary in the run of [A-Za-z0-9_.-] that ends at the needle, where
	// redactLabel's lazy `[A-Za-z0-9_.-]*?` begins. The pattern opens with \b.
	startLabel
	// startFlag: at the whitespace before the flag that ends at the needle, or at the start of the
	// text where the flag does: redactFlag's `(?:^|\s)`.
	startFlag
	// startListedFlag: at the quote before the flag that ends at the needle.
	startListedFlag
	// startScheme: at a letter on a word boundary in the run of [A-Za-z0-9+.-] before the needle,
	// a URL's `\b[a-z][a-z0-9+.-]*` scheme. The pattern opens with \b.
	startScheme
	// startLine: anywhere on the line of the needle or the blank lines before it. The pattern runs
	// whole from the start of that line, where its (?m)^ and \b see what they see in the whole
	// text.
	startLine
)

// redactSlot is what one rule compiles, on first use.
type redactSlot struct {
	once     sync.Once
	anchored *regexp.Regexp
	boundary bool
	// The assertions the whole pattern makes, which decide where it can be run on a suffix of the
	// text with the same result (safeStart): \b or \B, (?m)^, and ^ or \A.
	wordAssert, lineStart, textStart bool
}

// prepare compiles the rule's pattern anchored at the start of the text, without the \b it opens
// with, if any: the anchored pattern cannot see the byte before its start, so the boundary is
// checked against the whole text instead.
func (s *redactSlot) prepare(rule redactionRule) {
	s.once.Do(func() {
		expr := rule.expr
		for _, prefix := range []string{`(?i)(\b`, `(?i)\b`, `(\b`, `\b`} {
			if strings.HasPrefix(expr, prefix) {
				s.boundary = true
				expr = prefix[:len(prefix)-2] + expr[len(prefix):]
				break
			}
		}
		if (rule.start == startLabel || rule.start == startScheme) && !s.boundary {
			panic("asymptoteobserve: a label or scheme redaction rule must open with \\b: " + rule.expr)
		}
		s.anchored = regexp.MustCompile(`^` + expr)
		parsed, err := syntax.Parse(rule.expr, syntax.Perl)
		if err != nil {
			panic(err)
		}
		var walk func(re *syntax.Regexp)
		walk = func(re *syntax.Regexp) {
			switch re.Op {
			case syntax.OpWordBoundary, syntax.OpNoWordBoundary:
				s.wordAssert = true
			case syntax.OpBeginLine:
				s.lineStart = true
			case syntax.OpBeginText:
				s.textStart = true
			}
			for _, sub := range re.Sub {
				walk(sub)
			}
		}
		walk(parsed)
	})
}

// safeStart returns the latest position at or before q where the rule's whole pattern can run on
// the rest of the text and find what it finds there in the whole text: a position whose assertions
// read the same with the text before it cut off. It is the start of the text, or a position after
// an ASCII byte (so a character boundary however the text before it decodes) that is not a word
// character when the pattern tests word boundaries, and a newline when it tests line starts; a
// pattern that tests the start of the text can only run from it.
func (s *redactSlot) safeStart(t string, q int) int {
	if s.textStart {
		return 0
	}
	from := q
	for ; from > 0; from-- {
		c := t[from-1]
		if c < utf8.RuneSelf && (!s.wordAssert || !isWordByte(c)) && (!s.lineStart || c == '\n') {
			break
		}
	}
	if redactWork != nil {
		redactWork(q - from)
	}
	return from
}

// Anchored matching tries one place per occurrence of a rule's needles, and a pattern anchored at
// one place can read on to the end of the text before it fails: a curl rule reads its whole line.
// A text with many occurrences could make that quadratic. So before trying any, a rule adds up the
// text it would hand the pattern, from each place to the end, and when that passes
// redactAnchoredPerByte times the text's length plus redactAnchoredBase, it runs its whole pattern
// once instead, from a safe start before the first place. Either way the matches are the same, and
// the regexp engine reads at most that budget plus the text once: linear in the text, whatever it
// holds. TestRedactionWorkIsLinear holds this.
var (
	redactAnchoredPerByte = 8
	redactAnchoredBase    = 16 << 10
	// redactWork, when set (by tests), is told the length of every text redaction hands a pattern
	// and of every run it scans back over to find where a match starts.
	redactWork func(n int)
)

// redactEngine is built the first time anything is redacted: building it at package init would
// cost every hook run its allocations whether it redacts or not (TestPackageInitStaysCheap).
type redactEngine struct {
	scanner  needleScanner
	setMasks [64]uint64 // redactNeedles id -> the needles of the set, as scanner needle bits
	rules    []redactSlot
	assigned redactSlot
}

var redactEngineFor = sync.OnceValue(func() *redactEngine {
	e := &redactEngine{rules: make([]redactSlot, len(redactionRules))}
	ids := map[redactNeedle]int{}
	var needles []redactNeedle
	var register func(set *redactNeedles)
	register = func(set *redactNeedles) {
		for ; set != nil; set = set.within {
			for _, text := range set.needles {
				n := redactNeedle{text: text, folded: set.folded}
				id, ok := ids[n]
				if !ok {
					id = len(needles)
					ids[n] = id
					needles = append(needles, n)
				}
				e.setMasks[set.id] |= 1 << id
			}
		}
	}
	for _, rule := range redactionRules {
		register(rule.needles)
	}
	register(redactAssignedRule().needles)
	// Not needles of any rule: they mark text a case-insensitive pattern can match without one.
	for _, text := range []string{"\u017f", "\u212a"} {
		e.scanner.unfoldable |= 1 << len(needles)
		needles = append(needles, redactNeedle{text: text})
	}
	if len(needles) > 64 {
		panic("asymptoteobserve: redaction tracks at most 64 needles")
	}
	e.scanner.build(needles)
	return e
})

// compiledPatterns holds every redaction pattern compiled so far, by expression.
var compiledPatterns sync.Map

// compiledPattern compiles expr the first time it is asked for.
func compiledPattern(expr string) *regexp.Regexp {
	if re, ok := compiledPatterns.Load(expr); ok {
		return re.(*regexp.Regexp)
	}
	re, _ := compiledPatterns.LoadOrStore(expr, regexp.MustCompile(expr))
	return re.(*regexp.Regexp)
}

// redactGate answers whether text holds any of a set of needles: an occurrence of one that
// passes the set's precededBy and followedBy, in a text that holds the set it is within. The
// occurrences are found once per version of the text.
type redactGate struct {
	value   string
	enabled bool
	engine  *redactEngine
	scanned bool
	hits    []redactHit
	found   uint64 // scanner needle bits
	// known and present are bit sets over redactNeedles ids: which sets were looked for in this
	// version of the text, and which were found.
	known, present uint64
}

func (g *redactGate) set(value string) {
	if value != g.value {
		g.value, g.scanned, g.known, g.present = value, false, 0, 0
	}
}

func (g *redactGate) e() *redactEngine {
	if g.engine == nil {
		g.engine = redactEngineFor()
	}
	return g.engine
}

func (g *redactGate) scan() {
	if !g.scanned {
		g.hits, g.found = g.e().scanner.scan(g.value, g.hits[:0])
		g.scanned = true
	}
}

// unfoldable reports whether the text holds U+017F or U+212A, which a case-insensitive pattern
// equates with s and k: such a pattern can match "ſecret", which holds no needle "secret".
func (g *redactGate) unfoldable() bool {
	g.scan()
	return g.found&g.e().scanner.unfoldable != 0
}

// holdsNothing reports whether the text holds no needle at all, and so no set: no rule can match
// it.
func (g *redactGate) holdsNothing() bool {
	g.scan()
	return g.enabled && g.found == 0
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
	if set.folded && g.unfoldable() {
		g.present |= bit
		return true
	}
	g.scan()
	mask := g.e().setMasks[set.id]
	if g.found&mask == 0 {
		return false
	}
	for _, h := range g.hits {
		if mask&(1<<h.id) != 0 && set.passes(g.e(), g.value, h, false) {
			g.present |= bit
			return true
		}
	}
	return false
}

// passes reports whether the occurrence h passes the set's followedBy and precededBy and, with
// within, those of the sets it is within that hold its needle too. For a rule's own set with
// within, that is a condition every match holding the occurrence meets: a narrower set's needle
// ends the same label or flag as the broader set's does.
//
// followedBy is asked first. It reads only the separator after the needle, and it passes only a
// needle that ends its run of letters, digits and dashes, so precededBy, which reads that run back
// from the needle, reads each run once however many needles it holds.
func (set *redactNeedles) passes(engine *redactEngine, t string, h redactHit, within bool) bool {
	masks := &engine.setMasks
	for s := set; s != nil; s = s.within {
		if masks[s.id]&(1<<h.id) != 0 && (s.followedBy != nil && !s.followedBy(t[h.end:]) || s.precededBy != nil && !s.precededBy(t[:h.start])) {
			return false
		}
		if !within {
			break
		}
	}
	return true
}

// matches yields what FindAllStringSubmatchIndex(text, -1) of the rule's pattern returns, in
// order and with the same indexes, each with a pattern ExpandString can expand it with.
//
// FindAll returns, after the end of the previous match, the match that starts leftmost, the one the
// pattern prefers among those starting there. Every match holds an occurrence of one of the rule's
// needles that passes its set's conditions, and begins at a place the rule's start names around
// it. The pattern anchored at such a place (with an opening \b checked against the whole text,
// which the anchored pattern cannot see before its start) finds exactly the match the whole
// pattern prefers among those starting there. So trying the places in order and taking the first
// that matches is what FindAll does, without running the pattern over the text between them.
//
// Where a match can begin anywhere in a run before the needle (startLabel, startScheme), only the
// leftmost place in the run is tried. Whatever a match from a later place in the run takes, a
// match from the earlier one can take too, its lazy or starred run of the same characters taking
// the ones between; so when the earlier place does not match, no place in the run does.
//
// When the places would hand the pattern more text than the budget allows (redactAnchoredPerByte),
// the whole pattern runs once from a safe start before the first of them instead. No match starts
// before the first place, and from a safe start the pattern finds what it finds in the whole text.
func (rule redactionRule) matches(text *redactGate, slot *redactSlot) iter.Seq2[[]int, *regexp.Regexp] {
	return func(yield func([]int, *regexp.Regexp) bool) {
		t := text.value
		whole := func(from int) {
			if redactWork != nil {
				redactWork(len(t) - from)
			}
			re := rule.pattern()
			for _, m := range re.FindAllStringSubmatchIndex(t[from:], -1) {
				for i := range m {
					if m[i] >= 0 {
						m[i] += from
					}
				}
				if !yield(m, re) {
					return
				}
			}
		}
		if !text.enabled || rule.needles.folded && text.unfoldable() {
			whole(0)
			return
		}
		text.scan()
		engine := text.e()
		mask := engine.setMasks[rule.needles.id]
		if text.found&mask == 0 {
			return
		}
		if rule.start == startLine {
			if from, ok := lineStart(engine, t, text.hits, mask, rule.needles); ok {
				whole(from)
			}
			return
		}
		slot.prepare(rule)
		// What the anchored attempts would hand the pattern, at most: each place, to the end.
		first, handed, last := -1, 0, -1
		budget := redactAnchoredPerByte*len(t) + redactAnchoredBase
		for _, h := range text.hits {
			if mask&(1<<h.id) == 0 || !rule.needles.passes(engine, t, h, true) {
				continue
			}
			q := rule.startFor(t, h, 0, slot.boundary)
			if q < 0 || q == last {
				continue
			}
			if first < 0 || q < first {
				first = q
			}
			last = q
			if handed += len(t) - q; handed > budget {
				break
			}
		}
		if first < 0 {
			return
		}
		if handed > budget {
			whole(slot.safeStart(t, first))
			return
		}
		pos, failed := 0, -1
		for _, h := range text.hits {
			if mask&(1<<h.id) == 0 || h.start < pos || !rule.needles.passes(engine, t, h, true) {
				continue
			}
			q := rule.startFor(t, h, pos, slot.boundary)
			if q < 0 || q == failed {
				continue
			}
			if redactWork != nil {
				redactWork(len(t) - q)
			}
			m := slot.anchored.FindStringSubmatchIndex(t[q:])
			if m == nil {
				failed = q
				continue
			}
			for i := range m {
				if m[i] >= 0 {
					m[i] += q
				}
			}
			pos = m[1]
			if !yield(m, slot.anchored) {
				return
			}
		}
	}
}

// startFor returns the leftmost place at or after pos where a match holding h can begin, or -1.
func (rule redactionRule) startFor(t string, h redactHit, pos int, boundary bool) int {
	switch rule.start {
	case startNeedle:
		q := h.start - rule.offset
		if q < pos || boundary && !wordBoundary(t, q) {
			return -1
		}
		return q
	case startLabel, startScheme:
		class, last := &labelClass, h.start
		if rule.start == startScheme {
			class, last = &schemeClass, h.start-1
		}
		run := h.start
		for run > 0 && class[t[run-1]] {
			run--
		}
		if redactWork != nil {
			redactWork(h.start - run)
		}
		for q := max(run, pos); q <= last; q++ {
			if wordBoundary(t, q) && (rule.start == startLabel || isASCIILetter(t[q])) {
				return q
			}
		}
		return -1
	case startFlag, startListedFlag:
		// The needle set's precededBy has checked that what comes before the flag is whitespace,
		// the start of the text, or (listed) a quote.
		q, _ := flagStart(t[:h.start])
		if q > 0 {
			q--
		}
		if q < pos {
			return -1
		}
		return q
	}
	panic("asymptoteobserve: no start for a redaction rule")
}

// lineStart returns the start of a line at or before every place a startLine match holding one of
// the needles can begin: before the needle, an optional `set-`, a quote, whitespace across lines,
// and a few bytes (`=`, `--header`) more.
func lineStart(engine *redactEngine, t string, hits []redactHit, mask uint64, set *redactNeedles) (int, bool) {
	from, ok := len(t), false
	for _, h := range hits {
		if mask&(1<<h.id) == 0 || !set.passes(engine, t, h, true) {
			continue
		}
		b := h.start
		if b >= 4 && strings.EqualFold(t[b-4:b], "set-") {
			b -= 4
		}
		if b > 0 && strings.IndexByte(redactQuotes, t[b-1]) >= 0 {
			b--
		}
		for b > 0 && isRegexpSpace(t[b-1]) {
			b--
		}
		from, ok = min(from, max(0, b-10)), true
	}
	for from > 0 && t[from-1] != '\n' {
		from--
	}
	return from, ok
}

func isRegexpSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// isWordByte is \b's notion of a word character; a byte of a multi-byte character, or an invalid
// one, decodes to a rune that is not one.
func isWordByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

// wordBoundary reports whether \b holds at i < len(t).
func wordBoundary(t string, i int) bool {
	return (i > 0 && isWordByte(t[i-1])) != isWordByte(t[i])
}

func isASCIILetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// labelClass and schemeClass are the runs a startLabel and a startScheme match begin in, with
// capitals: the patterns are case-insensitive.
var labelClass, schemeClass = func() (label, scheme [256]bool) {
	for c := range 256 {
		b := byte(c)
		alnum := isASCIILetter(b) || '0' <= b && b <= '9'
		label[c] = alnum || b == '_' || b == '.' || b == '-'
		scheme[c] = alnum || b == '+' || b == '.' || b == '-'
	}
	return label, scheme
}()

// ---- the needle scanner ----

// redactNeedle is one literal some rule's pattern cannot match without; folded needles are lower
// case and found whatever the case of the text's ASCII letters.
type redactNeedle struct {
	text   string
	folded bool
}

// redactHit is one occurrence of a needle.
type redactHit struct {
	start, end int
	id         int
}

// needleScanner is an Aho-Corasick automaton over every needle, reading the text with its ASCII
// letters lower-cased: one pass finds every occurrence of every needle, overlapping ones included.
//
// A state is the offset of its row in delta, so a step is one add and one load. The states where
// a needle ends are numbered last, from firstOut, so a step that finds none costs one comparison.
type needleScanner struct {
	needles    []redactNeedle
	class      [256]uint8 // byte -> input class; 0 for bytes no needle holds
	delta      []uint32   // state + class -> next state
	out        []uint64   // (state-firstOut)/nc -> the needles ending there
	nc         int        // number of classes, the length of a row
	firstOut   int
	longest    int    // the longest needle's length
	unfoldable uint64 // the needles that are U+017F and U+212A
}

func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func (sc *needleScanner) build(needles []redactNeedle) {
	sc.needles = needles
	var classOf [256]uint8
	n := 1
	for _, nd := range needles {
		sc.longest = max(sc.longest, len(nd.text))
		for i := range len(nd.text) {
			if c := asciiLower(nd.text[i]); classOf[c] == 0 {
				classOf[c] = uint8(n)
				n++
			}
		}
	}
	sc.nc = n
	for b := range 256 {
		sc.class[b] = classOf[asciiLower(byte(b))]
	}
	// The trie.
	next := [][]int{make([]int, n)}
	out := []uint64{0}
	for id, nd := range needles {
		s := 0
		for i := range len(nd.text) {
			c := sc.class[nd.text[i]]
			if next[s][c] == 0 {
				next = append(next, make([]int, n))
				out = append(out, 0)
				next[s][c] = len(next) - 1
			}
			s = next[s][c]
		}
		out[s] |= 1 << id
	}
	// Failure links, breadth first, folded into a complete transition table. Class 0 never
	// continues a needle, so it always returns to the root.
	fail := make([]int, len(next))
	delta := make([]int, len(next)*n)
	var queue []int
	for c := 1; c < n; c++ {
		if t := next[0][c]; t != 0 {
			queue = append(queue, t)
		}
		delta[c] = next[0][c]
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		out[s] |= out[fail[s]]
		for c := 1; c < n; c++ {
			if t := next[s][c]; t != 0 {
				fail[t] = delta[fail[s]*n+c]
				delta[s*n+c] = t
				queue = append(queue, t)
			} else {
				delta[s*n+c] = delta[fail[s]*n+c]
			}
		}
	}
	// Renumber: the root first, then the states no needle ends in, then those some do.
	order := make([]int, 0, len(next))
	for s := range next {
		if out[s] == 0 {
			order = append(order, s)
		}
	}
	sc.firstOut = len(order) * n
	for s := range next {
		if out[s] != 0 {
			order = append(order, s)
			sc.out = append(sc.out, out[s])
		}
	}
	row := make([]int, len(next))
	for i, s := range order {
		row[s] = i * n
	}
	sc.delta = make([]uint32, len(next)*n)
	for s := range next {
		for c := range n {
			sc.delta[row[s]+c] = uint32(row[delta[s*n+c]])
		}
	}
}

// scanLanes is how many places of a long text are read at once. One reading is a chain of
// dependent loads; independent ones overlap.
const scanLanes = 4

// scan appends every needle occurrence in t to hits, ordered by start, and returns them with the
// set of needles found.
func (sc *needleScanner) scan(t string, hits []redactHit) ([]redactHit, uint64) {
	var found uint64
	delta, class, first := sc.delta, &sc.class, uint32(sc.firstOut)
	s, from := uint32(0), 0
	if seg := len(t) / scanLanes; seg >= 64 {
		// Lane k reports the occurrences that end in (k*seg, (k+1)*seg]. It starts reading early
		// enough for the longest needle to end at its first byte, and lane 0 reads that much past
		// its end; what ends outside its part, a lane does not report.
		overlap := sc.longest - 1
		t0 := t[:seg+overlap]
		t1 := t[seg-overlap : 2*seg]
		t2 := t[2*seg-overlap : 3*seg]
		t3 := t[3*seg-overlap : 4*seg]
		var s0, s1, s2, s3 uint32
		for j := 0; j < len(t0) && j < len(t1) && j < len(t2) && j < len(t3); j++ {
			s0 = delta[s0+uint32(class[t0[j]])]
			s1 = delta[s1+uint32(class[t1[j]])]
			s2 = delta[s2+uint32(class[t2[j]])]
			s3 = delta[s3+uint32(class[t3[j]])]
			if s0 >= first || s1 >= first || s2 >= first || s3 >= first {
				for k, st := range [scanLanes]uint32{s0, s1, s2, s3} {
					if end := k*seg - overlap*min(k, 1) + j + 1; st >= first && end > k*seg && end <= (k+1)*seg {
						hits = sc.record(t, st, end, hits, &found)
					}
				}
			}
		}
		// The last lane goes on to the end.
		s, from = s3, scanLanes*seg
	}
	for i := from; i < len(t); i++ {
		if s = delta[s+uint32(class[t[i]])]; s >= first {
			hits = sc.record(t, s, i+1, hits, &found)
		}
	}
	// The automaton finds occurrences by where they end, lane by lane; the rules want them by
	// where they start.
	slices.SortStableFunc(hits, func(a, b redactHit) int { return a.start - b.start })
	return hits, found
}

// record appends the occurrences ending at end, where the automaton is in state s. A needle that
// is not folded must match the text byte for byte.
func (sc *needleScanner) record(t string, s uint32, end int, hits []redactHit, found *uint64) []redactHit {
	for m := sc.out[(int(s)-sc.firstOut)/sc.nc]; m != 0; m &= m - 1 {
		id := bits.TrailingZeros64(m)
		nd := &sc.needles[id]
		start := end - len(nd.text)
		if !nd.folded && t[start:end] != nd.text {
			continue
		}
		*found |= 1 << id
		hits = append(hits, redactHit{start: start, end: end, id: id})
	}
	return hits
}
