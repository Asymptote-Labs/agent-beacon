package asymptoteobserve

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file is how redaction finds a secret written right after another one.
//
// The rules for credentials recognized by their own shape (privacy.go: `\b(ghp_)[A-Za-z0-9]{30,}`
// and the rest) open with \b, so they match a secret that starts a word. A secret written right
// after another one does not start a word, and a pass of the rules used to leave it:
//
//   - A pattern of fixed length stops inside a run of word characters. Of two Google keys back to
//     back, `AIza…AIza…`, the second has a letter before it. Once the first is replaced, `]` is
//     before it, so a second pass redacted what the first left.
//   - An open-ended pattern runs on into the next secret and stops at the first byte its class
//     lacks. Of two GitHub tokens back to back, the first match takes the second's `ghp` and stops
//     at its `_`, and the rest of the second token was left whole by every pass.
//   - A pattern that ends in \b or in a byte that is no key character, the AWS key id's and the
//     AWS secret's, does not match where the next secret follows it with nothing between them.
//   - A rule that runs before the shape rules, for a URL's password, a Bearer token or a curl
//     password, finds no word start after a key that has not been replaced yet; nor does any rule
//     after a bare copy of an assigned value, which is replaced after every rule has run.
//
// So the end of a secret counts as the start of a word, and one pass redacts all of it:
//
//   - When a shape rule matches, redactChain.run looks at what follows. If the match ran on into
//     a secret that starts inside it and ends past it, the match is cut where that secret starts,
//     or, too short to be cut there, runs on to that secret's end; if a secret of any shape rule
//     starts right where the match ends, with no word boundary between them, it is redacted with
//     it; and so on along the chain. Each secret keeps its own prefix: `ghp_[REDACTED]ghp_[REDACTED]`.
//   - A pattern that fails only for the secret right after it matches with the text cut where
//     that secret starts (redactChain.rescue).
//   - After every rule has run and bare copies are replaced, the rules that open with \b and are
//     not label rules are tried once more where a replacement ends right before a word byte
//     (redactAfterMarkers).
//
// Text where no secret meets another redacts as it did. Every check reads a slice whose length
// is bounded by the pattern's own shape, or text that the chain then replaces, so redaction's
// work stays linear; TestRedactionWorkIsLinear counts it.

// redactShapeKind says whether a rule is a shape rule (redactionRule.shape), and which kind.
type redactShapeKind uint8

const (
	shapeNone redactShapeKind = iota
	shapeToken
	// shapeJWT: three dot-separated segments of any length, which redactChain reads itself.
	shapeJWT
)

// redactShape is what the adjacency checks know of one shape rule's pattern.
type redactShape struct {
	rule    int
	needles uint64 // scanner needle bits
	offset  int    // the rule's offset: how many bytes before its needle a match starts
	// tail, when set, is the class of the open-ended repeat the pattern ends in, at least tailMin
	// long, after at most head bytes. Without it the pattern matches at most bound-1 bytes.
	tail    *[256]bool
	tailMin int
	head    int
	// bound is how long a slice from a match's start must be for the pattern to find in it what
	// it finds in the whole text: the same match, or, for an open-ended pattern, one that reaches
	// the slice's end where the whole match runs on (redactChain.shapeAt).
	bound int
	jwt   bool
	// redacted matches the rule's replacement at the start of a text: its first group, at most
	// prefix bytes, and a marker.
	redacted *regexp.Regexp
	prefix   int
	// trailing is the group the replacement ends with, or 0 when it ends in a marker.
	trailing int
}

// redactFollower is a rule that opens with \b and is neither a label rule nor a shape rule: a
// Bearer token, a curl password or cookie, a URL's password. redactAfterMarkers tries it at the
// start of a word that a replacement made.
type redactFollower struct {
	rule    int
	needles uint64
	kind    redactFollowerKind
	// charset is every byte the pattern can match, for followRun.
	charset [256]bool
	// lead, for followLine, is the pattern anchored at its needle with the lazy run after the
	// command word captured as `lead`: from any later place on the line up to that run's end, the
	// pattern ends where it ended from this one (lineFollowerAt).
	lead *regexp.Regexp
}

type redactFollowerKind uint8

const (
	// followRun: a word (its one needle), blanks, and a value: the match lies in the needle, the
	// blanks after it and the run of its charset without blanks after them (Bearer).
	followRun redactFollowerKind = iota
	// followScheme: a URL, which holds no `/` and no whitespace after its `://`.
	followScheme
	// followLine: a curl rule, which reads its whole command line and is applied to it until it
	// stops changing (redactLinesAfterMarkers).
	followLine
)

// redactAdjacency is what the adjacency checks know of the rules, built the first time a chain or
// a marker is looked at.
type redactAdjacency struct {
	shapes    []redactShape // in rule order
	followers []redactFollower
	byRule    []int // rule index -> index into shapes, or -1
}

func (e *redactEngine) adjacency() *redactAdjacency {
	e.adjacentOnce.Do(func() {
		a := &redactAdjacency{byRule: make([]int, len(redactionRules))}
		for i, rule := range redactionRules {
			a.byRule[i] = -1
			parsed, err := syntax.Parse(rule.expr, syntax.Perl)
			if err != nil {
				panic(err)
			}
			if rule.shape != shapeNone {
				a.byRule[i] = len(a.shapes)
				a.shapes = append(a.shapes, shapeOf(i, rule, parsed, e.setMasks[rule.needles.id]))
				continue
			}
			if !opensWithBoundary(rule.expr) || rule.start == startLabel {
				continue
			}
			if len(a.shapes) > 0 {
				panic("asymptoteobserve: a rule that opens with \\b must come before the shape rules: " + rule.expr)
			}
			f := redactFollower{rule: i, needles: e.setMasks[rule.needles.id]}
			switch {
			case rule.start == startScheme:
				f.kind = followScheme
			case rule.repeat:
				f.kind = followLine
				f.lead = leadPattern(rule)
			default:
				f.kind = followRun
				addCharset(&f.charset, parsed)
				for c := utf8.RuneSelf; c < 256; c++ {
					f.charset[c] = true
				}
				if f.charset['['] || f.charset['\n'] || len(rule.needles.needles) != 1 ||
					!strings.Contains(rule.expr, `\b`+rule.needles.needles[0]+`[ \t]+`) {
					panic("asymptoteobserve: a run follower has one needle, a blank after it, and cannot match '[' or a newline: " + rule.expr)
				}
			}
			a.followers = append(a.followers, f)
		}
		e.adjacent = a
	})
	return e.adjacent
}

// opensWithBoundary reports whether a pattern opens with \b, as redactSlot.prepare reads it.
func opensWithBoundary(expr string) bool {
	return slices.ContainsFunc(boundaryPrefixes, func(prefix string) bool { return strings.HasPrefix(expr, prefix) })
}

// leadPattern is a followLine rule's pattern anchored at its one needle, without the \b it opens
// with, and with the lazy run after the needle captured as `lead`.
func leadPattern(rule redactionRule) *regexp.Regexp {
	const run = `\bcurl\b[^\n]*?`
	if len(rule.needles.needles) != 1 || !strings.Contains(rule.expr, "("+run) {
		panic("asymptoteobserve: a line follower opens with `curl` and the lazy run of its line: " + rule.expr)
	}
	return regexp.MustCompile("^" + strings.Replace(rule.expr, "("+run, `(curl\b(?P<lead>[^\n]*?)`, 1))
}

// shapeOf reads a shape rule's pattern: a concatenation that ends, before any trailing assertion,
// either in an open-ended repeat of one class or in something of bounded length.
func shapeOf(index int, rule redactionRule, parsed *syntax.Regexp, needles uint64) redactShape {
	s := redactShape{rule: index, needles: needles, offset: rule.offset, jwt: rule.shape == shapeJWT}
	parts := []*syntax.Regexp{parsed}
	if parsed.Op == syntax.OpConcat {
		parts = parsed.Sub
	}
	for len(parts) > 0 && maxLength(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last.Op == syntax.OpRepeat && last.Max == -1 && last.Sub[0].Op == syntax.OpCharClass {
		s.tail = new([256]bool)
		addCharset(s.tail, last.Sub[0])
		s.tailMin = last.Min
		s.head = 0
		for _, part := range parts[:len(parts)-1] {
			if n := maxLength(part); n < 0 || s.head < 0 {
				s.head = -1
			} else {
				s.head += n
			}
		}
		s.bound = s.head + s.tailMin
	} else if n := maxLength(parsed); n >= 0 {
		s.bound = n + 1
	}
	group := firstGroup(parsed)
	if !strings.HasSuffix(rule.replacement, redactedValue) {
		i := strings.LastIndex(rule.replacement, "${")
		if i >= 0 && strings.HasSuffix(rule.replacement, "}") {
			s.trailing, _ = strconv.Atoi(rule.replacement[i+2 : len(rule.replacement)-1])
		}
	}
	if s.jwt != (s.head < 0) || s.tail == nil && s.bound == 0 || group == nil || maxLength(group) < 0 ||
		s.trailing == 0 && !strings.HasSuffix(rule.replacement, redactedValue) {
		panic("asymptoteobserve: a shape rule's pattern has a shape the adjacency checks do not know: " + rule.expr)
	}
	s.prefix = maxLength(group)
	s.redacted = regexp.MustCompile(`^(?:` + group.Sub[0].String() + `)` + regexp.QuoteMeta(redactedValue))
	return s
}

// firstGroup returns re's first capturing group, or nil.
func firstGroup(re *syntax.Regexp) *syntax.Regexp {
	if re.Op == syntax.OpCapture {
		return re
	}
	for _, sub := range re.Sub {
		if group := firstGroup(sub); group != nil {
			return group
		}
	}
	return nil
}

// maxLength returns the most bytes re can match, or -1 when that is unbounded.
func maxLength(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		n := 0
		for _, r := range re.Rune {
			n += utf8.RuneLen(r)
		}
		return n
	case syntax.OpCharClass:
		if len(re.Rune) > 0 && re.Rune[len(re.Rune)-1] < utf8.RuneSelf {
			return 1
		}
		return utf8.UTFMax
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return utf8.UTFMax
	case syntax.OpCapture, syntax.OpQuest:
		return maxLength(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		n := 0
		for _, sub := range re.Sub {
			m := maxLength(sub)
			if m < 0 {
				return -1
			}
			if re.Op == syntax.OpConcat {
				n += m
			} else {
				n = max(n, m)
			}
		}
		return n
	case syntax.OpRepeat:
		if m := maxLength(re.Sub[0]); re.Max >= 0 && m >= 0 {
			return re.Max * m
		}
		return -1
	case syntax.OpStar, syntax.OpPlus:
		return -1
	}
	return 0 // an assertion or an empty match
}

// addCharset adds every ASCII byte re can match to set.
func addCharset(set *[256]bool, re *syntax.Regexp) {
	switch re.Op {
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if r < utf8.RuneSelf {
				set[r] = true
				if re.Flags&syntax.FoldCase != 0 {
					set[asciiLower(byte(r))] = true
					set[asciiUpper(byte(r))] = true
				}
			}
		}
	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			for r := re.Rune[i]; r <= re.Rune[i+1] && r < utf8.RuneSelf; r++ {
				set[r] = true
			}
		}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		for c := range utf8.RuneSelf {
			set[c] = c != '\n' || re.Op == syntax.OpAnyChar
		}
	}
	for _, sub := range re.Sub {
		addCharset(set, sub)
	}
}

func asciiUpper(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// jwtSegment is the class of a JWT segment's bytes.
var jwtSegment = func() (set [256]bool) {
	for c := range 256 {
		b := byte(c)
		set[c] = isASCIILetter(b) || '0' <= b && b <= '9' || b == '_' || b == '-'
	}
	return set
}()

// redactChain finds the secrets written right after a shape rule's matches in one text.
type redactChain struct {
	engine *redactEngine
	adj    *redactAdjacency
	t      string
	hits   []redactHit
	// The end of the run of JWT segment bytes from segFrom, and the end of a JWT whose first
	// segment ends at restAt (-1: none): each is read once however many places ask.
	segFrom, segEnd int
	restAt, restEnd int
	// What secretAt found where it was asked.
	secrets map[int]redactSecret
	// The end of the run of URL scheme bytes from schemeFrom, and the URL follower's match from
	// the run's first place asked (schemeAt; nil: none), which any letter of the run shares.
	schemeFrom, schemeTo int
	schemeAt             int
	schemeMatch          []int
	// The end of the line holding lineFrom; and, per followLine follower, the places on a line
	// where it finds nothing (from the earliest place where it found nothing to the end of that
	// place's line) and where it finds the match it found from an earlier place (from that place
	// to the end of its lead, and where the match ends).
	lineFrom, lineTo int
	lineFirst        int // the end of lineFrom's own line, at or before lineTo
	lineFailed       [][2]int
	lineFound        [][3]int
	// The end of the run of a followRun follower's charset from runFrom, and per follower the
	// last place followerAt was asked about and its answer.
	runFrom, runTo int
	asked          []redactAsked
	// The assigned values whose copies are removed after every rule has run (overCopy), what
	// running on over them may still read (copyBudget), and whether running on over one, or
	// over an AWS secret that holds a `/`, may hide a match from this pass, as replacing a copy
	// may (copyHidesMatch); matches reports it to the gate.
	copies     *redactCopies
	copyBudget int
	hid        bool
	slashed    bool
}

// redactAsked is followerAt's answer for one follower at a place.
type redactAsked struct {
	at int
	m  []int
}

func newRedactChain(engine *redactEngine, t string, hits []redactHit) *redactChain {
	return &redactChain{engine: engine, adj: engine.adjacency(), t: t, hits: hits, segFrom: -1, restAt: -1,
		schemeFrom: -1, schemeTo: -1, schemeAt: -1, lineFrom: -1, lineTo: -1, lineFirst: -1, runFrom: -1, runTo: -1,
		copyBudget: redactCopyWorkPerByte*len(t) + redactCopyWorkBase}
}

// overCopy may read redactCopyWorkPerByte bytes and copies per byte of the text, and
// redactCopyWorkBase more; past that it leaves items as they are, as main does.
const (
	redactCopyWorkPerByte = 8
	redactCopyWorkBase    = 1 << 16
)

func redactWorked(n int) {
	if redactWork != nil {
		redactWork(n)
	}
}

// pattern is rule i's pattern anchored at the start of the text, without its opening \b.
func (c *redactChain) pattern(i int) *regexp.Regexp {
	slot := &c.engine.rules[i]
	slot.prepare(redactionRules[i])
	return slot.anchored
}

// find runs rule i's anchored pattern on t[q:end].
func (c *redactChain) find(i, q, end int) []int {
	redactWorked(end - q)
	m := c.pattern(i).FindStringSubmatchIndex(c.t[q:end])
	for j := range m {
		if m[j] >= 0 {
			m[j] += q
		}
	}
	return m
}

// search returns the first match of re, a shape rule's whole pattern, in t[pos:], as indexes into
// t. It reads up to the match's end.
func (c *redactChain) search(re *regexp.Regexp, pos int) []int {
	m := re.FindStringSubmatchIndex(c.t[pos:])
	if m == nil {
		redactWorked(len(c.t) - pos)
		return nil
	}
	redactWorked(m[1])
	for j := range m {
		if m[j] >= 0 {
			m[j] += pos
		}
	}
	return m
}

// hitsFrom returns the index of the first hit that starts at or after pos.
func hitsFrom(hits []redactHit, pos int) int {
	return sort.Search(len(hits), func(i int) bool { return hits[i].start >= pos })
}

// hasNeedleAt reports whether one of needles occurs at pos.
func (c *redactChain) hasNeedleAt(needles uint64, pos int) bool {
	for i := hitsFrom(c.hits, pos); i < len(c.hits) && c.hits[i].start == pos; i++ {
		if needles&(1<<c.hits[i].id) != 0 {
			return true
		}
	}
	return false
}

// shapeAt runs shape si's pattern at q, without the \b it opens with, on a slice that decides it:
// the match, and whether it runs on past the slice. Its needle must occur at q.
func (c *redactChain) shapeAt(si, q int) (m []int, open bool) {
	s := &c.adj.shapes[si]
	if s.jwt {
		if end := c.jwtEnd(q, q+3); end >= 0 {
			return c.find(s.rule, q, end), false
		}
		return nil, false
	}
	end := min(len(c.t), q+s.bound)
	m = c.find(s.rule, q, end)
	return m, m != nil && s.tail != nil && m[1] == end && end < len(c.t) && s.tail[c.t[end]]
}

// whole returns the match shapeAt found, run on to its end where it is open.
func (c *redactChain) whole(si, q int, m []int, open bool) []int {
	if !open {
		return m
	}
	s := &c.adj.shapes[si]
	end := m[1]
	for end < len(c.t) && s.tail[c.t[end]] {
		end++
	}
	redactWorked(end - m[1])
	return c.find(s.rule, q, end)
}

// rescue returns a match of shape si at q that ends where the next secret starts, or nil. A
// pattern of bounded length that ends in an assertion or a terminator, the AWS key id's \b and the
// AWS secret's byte that is no key character, fails where a secret follows with nothing between
// them; cut where that secret starts, the text ends right after it, and it matches. The next
// secret may already be replaced, by a rule that runs earlier. The pattern's own match at q, if
// any, must have been tried first.
func (c *redactChain) rescue(si, q int) []int {
	s := &c.adj.shapes[si]
	if s.tail != nil {
		return nil
	}
	for i := hitsFrom(c.hits, q+1); i < len(c.hits) && c.hits[i].start < q+s.bound+1; i++ {
		h := c.hits[i]
		for sj := range c.adj.shapes {
			x := h.start - c.adj.shapes[sj].offset
			if c.adj.shapes[sj].needles&(1<<h.id) == 0 || x <= q || x >= q+s.bound {
				continue
			}
			if m := c.find(s.rule, q, x); m != nil && m[1] == x {
				if _, next := c.secretAt(x); next != nil || c.redactedAt(x) || c.followerStarts(x) {
					return m
				}
			}
		}
	}
	// A URL, Bearer token or curl command right after it.
	for x := q + 1; x < min(len(c.t), q+s.bound); x++ {
		if isASCIILetter(c.t[x]) && c.followerStarts(x) {
			if m := c.find(s.rule, q, x); m != nil && m[1] == x {
				return m
			}
		}
	}
	return nil
}

// redactedAt reports whether a shape rule's replacement starts at x: its prefix and a marker.
func (c *redactChain) redactedAt(x int) bool {
	for si := range c.adj.shapes {
		s := &c.adj.shapes[si]
		if !c.hasNeedleAt(s.needles, x+s.offset) {
			continue
		}
		end := min(len(c.t), x+s.prefix+len(redactedValue))
		redactWorked(end - x)
		if s.redacted.MatchString(c.t[x:end]) {
			return true
		}
	}
	return false
}

// rescueBefore returns the first match rescue finds at a place of shape si from pos up to limit,
// where the pattern opens on a word boundary, or nil.
func (c *redactChain) rescueBefore(si, pos, limit int) []int {
	s := &c.adj.shapes[si]
	if s.tail != nil {
		return nil
	}
	for i := hitsFrom(c.hits, pos+s.offset); i < len(c.hits) && c.hits[i].start-s.offset < limit; i++ {
		q := c.hits[i].start - s.offset
		if s.needles&(1<<c.hits[i].id) == 0 || !wordBoundary(c.t, q) {
			continue
		}
		if m := c.rescue(si, q); m != nil {
			return m
		}
	}
	return nil
}

// secretAt returns the first shape rule, in rule order, whose pattern matches at p or is rescued
// there, and its match: the secret that starts at p when p counts as the start of a word. A rescue
// asks what starts where its match ends, so along a chain of key ids each place is asked about
// more than once; the answers are kept.
func (c *redactChain) secretAt(p int) (int, []int) {
	if found, ok := c.secrets[p]; ok {
		return found.shape, found.match
	}
	shape, match := -1, []int(nil)
	for si := range c.adj.shapes {
		s := &c.adj.shapes[si]
		if !c.hasNeedleAt(s.needles, p+s.offset) {
			continue
		}
		if m, open := c.shapeAt(si, p); m != nil {
			shape, match = si, c.whole(si, p, m, open)
			break
		}
		if m := c.rescue(si, p); m != nil {
			shape, match = si, m
			break
		}
	}
	if c.secrets == nil {
		c.secrets = map[int]redactSecret{}
	}
	c.secrets[p] = redactSecret{shape, match}
	return shape, match
}

// redactSecret is what secretAt found at a place.
type redactSecret struct {
	shape int
	match []int
}

// use is how shape si's matches are yielded: its anchored pattern and its rule.
func (c *redactChain) use(si int) redactUse {
	i := c.adj.shapes[si].rule
	return redactUse{c.pattern(i), &redactionRules[i]}
}

// run yields m, a match of shape si, and the secrets written right after it, and returns where
// the last of them ends, or false when yield asked to stop.
//
// A secret that starts inside the match and ends past it (crossing) is one the match ran on into.
// Where the match is still whole without the part from that secret on, it is cut there and the
// secret is the next item. Where it is not, the two are one item, run on to the second's end: of
// `sk-eyJhbGciOiJIUzI1NiJ9.eyJ…`, the key's pattern takes the JWT's first segment and stops at
// the dot, too short a key without it, and the item takes the whole JWT.
func (c *redactChain) run(si int, m []int, use redactUse, yield func([]int, redactUse) bool) (int, bool) {
	// item is what is yielded, with use and the groups of shape. si and m are the secret it ends
	// with, item itself unless merged. least is the first place item can be cut and stay whole.
	item, shape, merged := m, si, false
	least := c.least(si, m)
	for {
		q, sj, sm := -1, 0, []int(nil)
		if c.adj.shapes[shape].trailing == 0 && c.adj.shapes[si].trailing == 0 {
			q, sj, sm = c.crossing(si, m)
		}
		if q >= 0 {
			var cut []int
			if q >= least && merged {
				cut = slices.Clone(item)
				cut[1] = q
			} else if q >= least {
				if cut = c.find(c.adj.shapes[shape].rule, item[0], q); cut != nil && cut[1] != q {
					cut = nil
				}
			}
			if cut != nil {
				if !yield(cut, use) {
					return 0, false
				}
				item, shape, merged, use, least = sm, sj, false, c.use(sj), c.least(sj, sm)
				si, m = sj, sm
				continue
			}
			// The item keeps all of itself so far, and a cut can now fall only past that. What the
			// secret's own replacement would keep after it, it keeps.
			least, merged = item[1], true
			item = slices.Clone(item)
			item[1] = sm[1]
			if g := c.adj.shapes[sj].trailing; g > 0 && sm[2*g] >= 0 {
				item[1] = sm[2*g]
			}
			// The item is yielded with the first secret's rule: an AWS secret it takes is not seen
			// as one where the replacements are made (redactionRule.applyOnce).
			c.slashed = c.slashed || slashedAWSSecret(redactionRules[c.adj.shapes[sj].rule].expr, c.t, sm)
			si, m = sj, sm
			continue
		}
		// A URL, Bearer token or curl command whose first word the match took: cut the match
		// before it, and the follower is redacted where the marker ends (redactAfterMarkers); too
		// short to be cut there, the item runs on over the follower's match.
		if c.adj.shapes[shape].trailing == 0 {
			if q, fend := c.followerCrossing(m); q >= 0 {
				var cut []int
				if q >= least && merged {
					cut = slices.Clone(item)
					cut[1] = q
				} else if q >= least {
					if cut = c.find(c.adj.shapes[shape].rule, item[0], q); cut != nil && cut[1] != q {
						cut = nil
					}
				}
				if cut == nil {
					cut = slices.Clone(item)
					cut[1] = fend
				}
				return cut[1], yield(cut, use)
			}
		}
		if c.adj.shapes[shape].trailing == 0 {
			item = c.overCopy(item)
		}
		if !yield(item, use) {
			return 0, false
		}
		end := item[1]
		// A replacement that keeps what follows the secret does not end in a marker.
		if g := c.adj.shapes[shape].trailing; g > 0 && item[2*g] < item[2*g+1] ||
			end == len(c.t) || !isWordByte(c.t[end-1]) || !isWordByte(c.t[end]) {
			return end, true
		}
		sj, sm = c.secretAt(end)
		if sm == nil {
			return end, true
		}
		si, m, item, shape, merged, use, least = sj, sm, sm, sj, false, c.use(sj), c.least(sj, sm)
	}
}

// overCopy returns item run on to the end of a copy of an assigned value that starts inside it and
// ends past it, the item having taken the copy's first bytes; or item. The copy is removed after
// every rule has run, which finds it only whole: of `hf_…opaque-flag-value` with `opaque-flag-value`
// assigned, the token's pattern takes `opaque`, and `-flag-value` was left. Such a copy starts
// less than the longest copy's length before the item's end. What it reads, places, copies looked
// at and bytes compared, comes out of the chain's copyBudget, linear in the text; once that is
// spent, items are left as they are.
func (c *redactChain) overCopy(item []int) []int {
	if c.copies == nil || c.copyBudget <= 0 {
		return item
	}
	t, work := c.t, 0
	for x := max(item[0]+1, item[1]-c.copies.longest+1); x < item[1] && x+redactCopyMin <= len(t) && work < c.copyBudget; x++ {
		work++
		for _, v := range c.copies.byFirst[t[x:x+redactCopyMin]] {
			work++
			if x+len(v) <= item[1] {
				continue
			}
			work += len(v)
			if strings.HasPrefix(t[x:], v) {
				item = slices.Clone(item)
				item[1] = x + len(v)
				c.hid = true
			}
		}
	}
	c.copyBudget -= work
	redactWorked(work)
	return item
}

// slashedAWSSecret reports whether m, a match of the pattern expr, is one of the rule for an AWS
// key id and its secret whose secret holds a `/`, where the URL rule, which runs before the shape
// rules, stopped a password: once the secret is replaced, the password can run on to its `@`.
func slashedAWSSecret(expr, t string, m []int) bool {
	return expr == redactAWSPair && strings.IndexByte(t[m[5]:m[6]], '/') >= 0
}

// least returns the first place m, a match of shape si, can be cut and still be a whole match: as
// far into its open-ended tail as the tail's least length. A pattern without one cannot be cut.
func (c *redactChain) least(si int, m []int) int {
	s := &c.adj.shapes[si]
	if s.tail == nil {
		return len(c.t) + 1
	}
	tail := m[1]
	for tail > m[3] && s.tail[c.t[tail-1]] {
		tail--
	}
	redactWorked(m[1] - tail)
	return tail + s.tailMin
}

// crossing looks for a secret that starts inside m, a match of shape si, and ends past it: the
// match ran on into the next secret and stopped at a byte that secret holds, or the secret
// started inside a match of fixed length. It returns where the leftmost such secret starts, its
// shape and its match; or -1.
func (c *redactChain) crossing(si int, m []int) (int, int, []int) {
	t, start, end := c.t, m[0], m[1]
	if end >= len(t) {
		return -1, 0, nil
	}
	stop := t[end]
	best, bestShape := end, -1
	var bestMatch []int
	bestOpen := false
	for sj := range c.adj.shapes {
		s := &c.adj.shapes[sj]
		// run is where the run of the class that could carry s across end starts.
		run := end
		var class *[256]bool
		switch {
		case s.jwt && (jwtSegment[stop] || stop == '.'):
			class = &jwtSegment
		case s.tail != nil && s.tail[stop]:
			class = s.tail
		}
		if class != nil {
			for run > start+1 && class[t[run-1]] {
				run--
			}
			redactWorked(end - run)
		}
		lo := run
		switch {
		case s.jwt:
			if class == nil {
				continue
			}
		case s.tail == nil:
			lo = end - s.bound
		default:
			lo -= s.head
		}
		lo = max(lo, start+1)
		for i := hitsFrom(c.hits, lo+s.offset); i < len(c.hits); i++ {
			h := c.hits[i]
			q := h.start - s.offset
			if q >= best {
				break
			}
			if s.needles&(1<<h.id) == 0 || q < lo {
				continue
			}
			if s.jwt {
				// The leftmost `eyJ` in the run: a later one has a shorter first segment and the
				// same rest. Its first segment runs on from end.
				if jwt := c.jwtEnd(q, end); jwt > end {
					best, bestShape, bestMatch, bestOpen = q, sj, nil, false
				}
				break
			}
			sm, open := c.shapeAt(sj, q)
			if sm == nil {
				continue
			}
			// An open match whose tail runs on unbroken to end crosses it too.
			if sm[1] > end || open && class != nil && run <= sm[1] {
				best, bestShape, bestMatch, bestOpen = q, sj, sm, open
				break
			}
		}
	}
	if bestShape < 0 {
		return -1, 0, nil
	}
	if bestMatch == nil {
		bestMatch, bestOpen = c.shapeAt(bestShape, best)
	}
	return best, bestShape, c.whole(bestShape, best, bestMatch, bestOpen)
}

// jwtEnd returns where the JWT that starts at q ends, or -1. Its first segment holds only segment
// bytes from q+3 to from.
func (c *redactChain) jwtEnd(q, from int) int {
	first := c.segmentEnd(from)
	if first-(q+3) < 8 {
		return -1
	}
	if c.restAt == first {
		return c.restEnd
	}
	t, end := c.t, -1
	if strings.HasPrefix(t[first:], ".eyJ") {
		second := segmentEnd(t, first+4)
		if second-(first+4) >= 8 && second < len(t) && t[second] == '.' {
			if third := segmentEnd(t, second+1); third-(second+1) >= 8 {
				end = third
			}
		}
	}
	c.restAt, c.restEnd = first, end
	return end
}

// segmentEnd returns the end of the run of JWT segment bytes from from.
func (c *redactChain) segmentEnd(from int) int {
	if c.segFrom <= from && from <= c.segEnd {
		return c.segEnd
	}
	c.segFrom, c.segEnd = from, segmentEnd(c.t, from)
	return c.segEnd
}

func segmentEnd(t string, from int) int {
	end := from
	for end < len(t) && jwtSegment[t[end]] {
		end++
	}
	redactWorked(end - from)
	return end
}

// markerBefore returns the end of the first marker at or after from that a word byte follows, or
// -1.
func markerBeforeWord(t string, from int) int {
	for from < len(t) {
		i := strings.Index(t[from:], redactedValue)
		if i < 0 {
			return -1
		}
		p := from + i + len(redactedValue)
		if p < len(t) && isWordByte(t[p]) {
			return p
		}
		from = p
	}
	return -1
}

// redactAfterMarkers redacts what starts right after a replacement, which starts a word only in
// the redacted text: a URL's password, a Bearer token or a curl password right after a key, and
// any secret right after a replaced copy of an assigned value. Where a replacement ends right
// before a word byte, the followers and then the shape rules, in rule order, are tried there; the
// first that matches is applied, and a shape rule's chain after it. Each place is tried once, left
// to right, so a chain that alternates between them is redacted in one pass. The curl rules,
// which read a whole command line and are applied to it until it stops changing, are then
// applied to the line from the first such place where a curl command starts.
//
// It also reports whether it replaced an AWS secret that holds a `/` (slashedAWSSecret).
func (e *redactEngine) redactAfterMarkers(t string) (string, bool) {
	p := markerBeforeWord(t, 0)
	if p < 0 {
		return t, false
	}
	hits, _ := e.scanner.scan(t, nil)
	c := newRedactChain(e, t, hits)
	var out []byte
	last := 0
	emit := func(m []int, use redactUse) bool {
		out = append(out, t[last:m[0]]...)
		out = use.pattern.ExpandString(out, use.rule.replacement, t, m)
		last = m[1]
		c.slashed = c.slashed || slashedAWSSecret(use.rule.expr, t, m)
		return true
	}
	for ; p >= 0; p = markerBeforeWord(t, max(p, last)) {
		for p >= last && p < len(t) {
			if m, use := c.followerAt(p); m != nil {
				// The shape rules run after the followers, also over what a follower keeps of
				// its match, ${1}: a URL whose scheme starts with a key.
				replacement, end := use.rule.replacing(t, m)
				var items [][]int
				var uses []redactUse
				if si, sm := c.secretAt(p); sm != nil && strings.HasPrefix(replacement, "${1}") {
					if chained, _ := c.run(si, sm, c.use(si), func(m []int, use redactUse) bool {
						items, uses = append(items, m), append(uses, use)
						return true
					}); chained > m[3] {
						items = nil
					}
				}
				for i := range items {
					emit(items[i], uses[i])
				}
				if items != nil {
					out = append(out, t[last:m[3]]...)
					replacement = strings.TrimPrefix(replacement, "${1}")
				} else {
					out = append(out, t[last:m[0]]...)
				}
				out = use.pattern.ExpandString(out, replacement, t, m)
				last = end
				break
			}
			si, m := c.secretAt(p)
			if m == nil {
				break
			}
			end, _ := c.run(si, m, c.use(si), emit)
			// The chain checked for a shape rule at its end; a follower may start there.
			if end == len(t) || !isWordByte(t[end]) || len(out) == 0 || out[len(out)-1] != ']' {
				break
			}
			p = end
		}
	}
	if out != nil {
		t = string(append(out, t[last:]...))
	}
	return e.redactLinesAfterMarkers(t), c.slashed
}

// commandLineEnd returns the end of what a curl rule can match from p: its line; the next one,
// since the blank before a flag (`\s`) can be the newline, so the flag can start that line; and,
// since the blanks after a flag (`\s*`) can run over newlines, the next line that is not blank.
func commandLineEnd(t string, p int) int {
	i := strings.IndexByte(t[p:], '\n')
	if i < 0 {
		return len(t)
	}
	next := p + i + 1
	j := strings.IndexByte(t[next:], '\n')
	if j < 0 {
		return len(t)
	}
	next += j
	for next < len(t) && isRegexpSpace(t[next]) {
		next++
	}
	if k := strings.IndexByte(t[next:], '\n'); k >= 0 {
		return next + k
	}
	return len(t)
}

// redactLinesAfterMarkers applies the followLine rules, each as redaction applies it, to every
// line from the first place right after a marker where one of their needles starts.
func (e *redactEngine) redactLinesAfterMarkers(t string) string {
	adj := e.adjacency()
	var out []byte
	last := 0
	for p := markerBeforeWord(t, 0); p >= 0; p = markerBeforeWord(t, max(p, last)) {
		if p < last || !adj.startsLine(t, p) {
			continue
		}
		// A segment whose last line holds another curl command runs on to that command's value.
		end := commandLineEnd(t, p)
		for end < len(t) {
			ls := strings.LastIndexByte(t[:end], '\n') + 1
			if ls <= p || !strings.Contains(strings.ToLower(t[ls:end]), "curl") {
				break
			}
			end = commandLineEnd(t, ls)
		}
		line := t[p:end]
		for _, f := range adj.followers {
			if f.kind != followLine {
				continue
			}
			text := redactGate{value: line, enabled: true, engine: e}
			if rule := redactionRules[f.rule]; text.holds(rule.needles) {
				line = rule.apply(&text, &e.rules[f.rule])
			}
		}
		out = append(append(out, t[last:p]...), line...)
		last = end
	}
	if out == nil {
		return t
	}
	return string(append(out, t[last:]...))
}

// startsLine reports whether one of the followLine rules' needles starts at p.
func (a *redactAdjacency) startsLine(t string, p int) bool {
	for _, f := range a.followers {
		if f.kind != followLine {
			continue
		}
		for _, needle := range redactionRules[f.rule].needles.needles {
			if len(t)-p >= len(needle) && strings.EqualFold(t[p:p+len(needle)], needle) {
				return true
			}
		}
	}
	return false
}

// followerStarts reports whether a follower matches at x: a URL, a Bearer token or a curl command.
func (c *redactChain) followerStarts(x int) bool {
	if m, _ := c.followerAt(x); m != nil {
		return true
	}
	return c.lineFollowerAt(x) != nil
}

// followerCrossing returns where a follower starts whose first word m took, the match running on
// over a URL's scheme, `Bearer` or `curl` and stopping at the byte after it, and where the
// follower's match ends; or -1.
func (c *redactChain) followerCrossing(m []int) (int, int) {
	t, start, e := c.t, m[0], m[1]
	if e >= len(t) || e-1 <= start || c.followerStarts(e) {
		return -1, 0
	}
	// The whole scheme: where the match stopped inside it, at `.`, `-` or `+`, the URL rule found a
	// scheme of its own after that byte.
	if isASCIILetter(t[e-1]) && strings.HasPrefix(t[e:], "://") {
		if fm, _ := c.followerAt(e - 1); fm != nil && fm[1] > e {
			return e - 1, fm[1]
		}
	}
	for _, f := range c.adj.followers {
		if f.kind == followScheme {
			continue
		}
		for _, needle := range redactionRules[f.rule].needles.needles {
			q := e - len(needle)
			if q <= start || !c.hasNeedleAt(f.needles, q) {
				continue
			}
			if f.kind == followLine {
				if fm := c.lineFollowerAt(q); fm != nil {
					return q, fm[1]
				}
			} else if fm, _ := c.followerAt(q); fm != nil && fm[1] > e {
				return q, fm[1]
			}
		}
	}
	return -1, 0
}

// schemeEnd returns the end of the run of URL scheme bytes from p: exactly what the URL pattern's
// case-insensitive `[a-z0-9+.-]*` takes, the ASCII class and the two other characters that fold
// into it, `K` (U+212A, k) and `ſ` (U+017F, s). schemeFollowerAt shares one answer across the run,
// so a byte the pattern stops at must end it.
func (c *redactChain) schemeEnd(p int) int {
	if c.schemeFrom <= p && p <= c.schemeTo {
		return c.schemeTo
	}
	t, end := c.t, p
	for end < len(t) {
		switch {
		case schemeClass[t[end]]:
			end++
			continue
		case strings.HasPrefix(t[end:], "\u212a"):
			end += len("\u212a")
			continue
		case strings.HasPrefix(t[end:], "\u017f"):
			end += len("\u017f")
			continue
		}
		break
	}
	redactWorked(end - p)
	c.schemeFrom, c.schemeTo = p, end
	return end
}

// lineFollowerAt returns the match of the first followLine rule that matches at q, or nil (only
// its start and end: the callers read no group). It hands the pattern slices that double until
// the match ends inside one or one reaches the line's end. A place on a line where the needle
// ends a word and the rule finds nothing means no later place on the line finds anything (from
// the earlier `curl`, the pattern's lazy run takes the bytes up to the later one); and where it
// finds a match, a later place up to the end of that lazy run finds one that ends at the same
// byte, the flags between having failed from the earlier place too. So each line is read a
// bounded number of times however many places on it are asked about.
func (c *redactChain) lineFollowerAt(q int) []int {
	t := c.t
	if c.lineFailed == nil {
		c.lineFailed = make([][2]int, len(c.adj.followers))
		c.lineFound = make([][3]int, len(c.adj.followers))
		for k := range c.lineFailed {
			c.lineFailed[k] = [2]int{-1, -1}
			c.lineFound[k] = [3]int{-1, -1, -1}
		}
	}
	for k, f := range c.adj.followers {
		if f.kind != followLine || !c.hasNeedleAt(f.needles, q) {
			continue
		}
		// The pattern opens with the needle and \b: where a word byte follows the needle it finds
		// nothing, which says nothing of later places.
		word := len(redactionRules[f.rule].needles.needles[0])
		if q+word < len(t) && isWordByte(t[q+word]) {
			continue
		}
		failed, found := &c.lineFailed[k], &c.lineFound[k]
		if failed[0] <= q && q <= failed[1] {
			continue
		}
		if found[0] <= q && q <= found[1] {
			return []int{q, found[2]}
		}
		if q < c.lineFrom || q > c.lineFirst {
			c.lineFrom, c.lineTo = q, commandLineEnd(t, q)
			c.lineFirst = c.lineTo
			if i := strings.IndexByte(t[q:c.lineTo], '\n'); i >= 0 {
				c.lineFirst = q + i
			}
			redactWorked(c.lineTo - q)
		}
		for n := 256; ; n *= 2 {
			end := min(c.lineTo, q+n)
			redactWorked(end - q)
			m := f.lead.FindStringSubmatchIndex(t[q:end])
			if m != nil && q+m[1] < end || end == c.lineTo {
				if m != nil {
					lead := q + m[2*f.lead.SubexpIndex("lead")+1]
					*found = [3]int{q, lead - word, q + m[1]}
					return []int{q, q + m[1]}
				}
				break
			}
		}
		// The places are not asked in order: each shape's rescue asks from its own start.
		if failed[1] == c.lineFirst && failed[0] >= 0 {
			failed[0] = min(failed[0], q)
		} else {
			*failed = [2]int{q, c.lineFirst}
		}
	}
	return nil
}

// followerAt returns the match of the first followRun or followScheme rule, in rule order, that
// matches at p and keeps no value of it, and how to expand it. Each answer is kept for the place
// asked last, and a URL's for every letter of its scheme's run: the places of a run share it.
func (c *redactChain) followerAt(p int) ([]int, redactUse) {
	t := c.t
	if c.asked == nil {
		c.asked = make([]redactAsked, len(c.adj.followers))
		for k := range c.asked {
			c.asked[k].at = -1
		}
	}
	for k := range c.adj.followers {
		f := &c.adj.followers[k]
		var m []int
		switch {
		case f.kind == followLine:
			continue
		case c.asked[k].at == p:
			m = c.asked[k].m
		case f.kind == followScheme:
			if p >= len(t) || !isASCIILetter(t[p]) {
				continue
			}
			m = c.schemeFollowerAt(f, p)
			c.asked[k] = redactAsked{p, m}
		default:
			if !c.hasNeedleAt(f.needles, p) {
				continue
			}
			// The word, the blanks after it, and a run of the charset without blanks. A run
			// follower's pattern needs a blank after its word (adjacency checks it).
			end := p + len(redactionRules[f.rule].needles.needles[0])
			if end >= len(t) || t[end] != ' ' && t[end] != '\t' {
				c.asked[k] = redactAsked{p, nil}
				continue
			}
			for end < len(t) && (t[end] == ' ' || t[end] == '\t') {
				end++
			}
			m = c.find(f.rule, p, min(len(t), c.runEnd(f, end)+1))
			c.asked[k] = redactAsked{p, m}
		}
		if m == nil {
			continue
		}
		rule, re := &redactionRules[f.rule], c.pattern(f.rule)
		if rule.keep != nil {
			index := re.SubexpIndex("value")
			if index < 0 {
				index = 2
			}
			if g := 2 * index; m[g] >= 0 && rule.keep(t[m[g]:m[g+1]]) {
				continue
			}
		}
		return m, redactUse{re, rule}
	}
	return nil, redactUse{}
}

// schemeFollowerAt returns the URL follower's match at p, a letter, or nil. From any letter of a
// run of scheme bytes the pattern reads the run to its `://` and the same text after it, so the
// match from the first letter asked serves every other: only its start moves.
func (c *redactChain) schemeFollowerAt(f *redactFollower, p int) []int {
	t := c.t
	r := c.schemeEnd(p)
	if !strings.HasPrefix(t[r:], "://") {
		return nil
	}
	if c.schemeFrom <= c.schemeAt && c.schemeAt <= r {
		if c.schemeMatch == nil {
			return nil
		}
		m := slices.Clone(c.schemeMatch)
		m[0], m[2] = p, p
		return m
	}
	end := r + 3
	for end < len(t) && t[end] != '/' && !isRegexpSpace(t[end]) {
		end++
	}
	redactWorked(end - r)
	c.schemeAt, c.schemeMatch = p, c.find(f.rule, p, end)
	return c.schemeMatch
}

// runEnd returns the end of the run of follower f's charset, without blanks, from s.
func (c *redactChain) runEnd(f *redactFollower, s int) int {
	if c.runFrom <= s && s <= c.runTo {
		return c.runTo
	}
	end := s
	for end < len(c.t) && f.charset[c.t[end]] && c.t[end] != ' ' && c.t[end] != '\t' {
		end++
	}
	redactWorked(end - s)
	c.runFrom, c.runTo = s, end
	return end
}
