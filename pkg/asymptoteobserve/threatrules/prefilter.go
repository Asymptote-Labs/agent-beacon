package threatrules

import (
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A prefilter is a necessary condition for a regex to match, over the literal strings the text
// must contain: when it does not hold, the regex cannot match and is not run. Rule regexes are
// long case-insensitive alternations with bounded gaps (`(ignore|disregard)\s+...\s+(rules)`),
// the slowest case for Go's regexp, which walks the text rune by rune, case-folding each one; most
// texts contain none of a rule's literals, and a substring search rejects them at memory speed.
// This is RE2's FilteredRE2 idea, derived mechanically from each pattern as it is compiled.
//
// Atoms and text are compared in fold-canonical form: every rune is replaced by the smallest rune
// of its Unicode simple-fold orbit, the equivalence Go's regexp uses for (?i). Canonicalizing is a
// per-rune mapping, so a substring of the text maps to a substring of its canonical form, and two
// runes match under (?i) exactly when their canonical forms are equal. A plain lowercase would not
// do: (?i)s matches ſ (U+017F), which no lowercasing turns into s. Case-sensitive patterns are
// compared the same way, which only makes their prefilter weaker, never wrong.
//
// TestPrefilterIsSound and FuzzPrefilterIsSound check the one property results depend on: a
// regex never matches a text its prefilter rejects.
type prefilter struct {
	op   pfOp
	atom string // canonical; op == pfAtom
	subs []*prefilter
}

type pfOp uint8

const (
	pfTrue pfOp = iota // no constraint: the regex must run
	pfAtom             // the canonical text contains atom
	pfAnd
	pfOr
)

// maxExact bounds the exact-string sets carried through concatenation and alternation.
const maxExact = 16

var pfAlways = &prefilter{op: pfTrue}

// holds reports whether the prefilter holds for canonical text.
func (p *prefilter) holds(text string) bool {
	switch p.op {
	case pfAtom:
		return strings.Contains(text, p.atom)
	case pfAnd:
		for _, s := range p.subs {
			if !s.holds(text) {
				return false
			}
		}
		return true
	case pfOr:
		for _, s := range p.subs {
			if s.holds(text) {
				return true
			}
		}
		return false
	}
	return true
}

// compilePrefilter derives the prefilter of a pattern, parsed as regexp.Compile parses it.
func compilePrefilter(pattern string) (*prefilter, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	return analyze(re).match(), nil
}

// pfInfo is what a subexpression tells about the text it matches: either the exact set of
// canonical strings it can match (exact != nil), or a prefilter it implies.
type pfInfo struct {
	exact map[string]bool
	pf    *prefilter
}

func exactly(ss ...string) pfInfo {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return pfInfo{exact: m}
}

// match turns an exact set into a prefilter: the text contains one of the strings. The empty
// string is contained in every text, so a set holding it does not constrain.
func (in pfInfo) match() *prefilter {
	if in.exact == nil {
		return in.pf
	}
	if in.exact[""] {
		return pfAlways
	}
	atoms := make([]string, 0, len(in.exact))
	for s := range in.exact {
		atoms = append(atoms, s)
	}
	slices.Sort(atoms) // a stable order, so a rule's work does not vary between runs
	subs := make([]*prefilter, len(atoms))
	for i, s := range atoms {
		subs[i] = &prefilter{op: pfAtom, atom: s}
	}
	return anyOf(subs...)
}

func analyze(re *syntax.Regexp) pfInfo {
	switch re.Op {
	case syntax.OpLiteral:
		return exactly(canonRunes(re.Rune))
	case syntax.OpCharClass:
		// A small class is the set of its runes; a wide one (including every negated class)
		// does not constrain.
		var rs []string
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i+1]-re.Rune[i] >= 4 || len(rs) >= 4 {
				return pfInfo{pf: pfAlways}
			}
			for r := re.Rune[i]; r <= re.Rune[i+1]; r++ {
				rs = append(rs, string(canonRune(r)))
			}
		}
		return exactly(rs...)
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return exactly("")
	case syntax.OpCapture:
		return analyze(re.Sub[0])
	case syntax.OpPlus:
		return pfInfo{pf: analyze(re.Sub[0]).match()}
	case syntax.OpRepeat:
		if re.Min == 0 {
			return pfInfo{pf: pfAlways}
		}
		return pfInfo{pf: analyze(re.Sub[0]).match()}
	case syntax.OpConcat:
		return concat(re.Sub)
	case syntax.OpAlternate:
		subs := make([]pfInfo, len(re.Sub))
		exact := map[string]bool{}
		for i, s := range re.Sub {
			subs[i] = analyze(s)
			if exact != nil && subs[i].exact != nil && len(exact)+len(subs[i].exact) <= maxExact {
				for k := range subs[i].exact {
					exact[k] = true
				}
			} else {
				exact = nil
			}
		}
		if exact != nil {
			return pfInfo{exact: exact}
		}
		pfs := make([]*prefilter, len(subs))
		for i, s := range subs {
			pfs[i] = s.match()
		}
		return pfInfo{pf: anyOf(pfs...)}
	}
	// OpAnyChar, OpAnyCharNotNL, OpStar, OpQuest, OpNoMatch: no constraint.
	return pfInfo{pf: pfAlways}
}

// concat carries exact sets through a concatenation as their cross product while it stays small;
// whatever cannot stay exact becomes a conjunct.
func concat(subs []*syntax.Regexp) pfInfo {
	var conj []*prefilter
	cur := map[string]bool{"": true}
	for _, s := range subs {
		in := analyze(s)
		if in.exact != nil && len(cur)*len(in.exact) <= maxExact {
			next := make(map[string]bool, len(cur)*len(in.exact))
			for a := range cur {
				for b := range in.exact {
					next[a+b] = true
				}
			}
			cur = next
			continue
		}
		conj = append(conj, pfInfo{exact: cur}.match())
		if in.exact != nil {
			cur = in.exact
		} else {
			conj = append(conj, in.pf)
			cur = map[string]bool{"": true}
		}
	}
	if len(conj) == 0 {
		return pfInfo{exact: cur}
	}
	conj = append(conj, pfInfo{exact: cur}.match())
	return pfInfo{pf: allOf(conj...)}
}

func allOf(subs ...*prefilter) *prefilter {
	var keep []*prefilter
	for _, s := range subs {
		switch s.op {
		case pfTrue:
		case pfAnd:
			keep = append(keep, s.subs...)
		default:
			keep = append(keep, s)
		}
	}
	switch len(keep) {
	case 0:
		return pfAlways
	case 1:
		return keep[0]
	}
	return &prefilter{op: pfAnd, subs: keep}
}

func anyOf(subs ...*prefilter) *prefilter {
	var keep []*prefilter
	for _, s := range subs {
		switch s.op {
		case pfTrue:
			return pfAlways
		case pfOr:
			keep = append(keep, s.subs...)
		default:
			keep = append(keep, s)
		}
	}
	switch len(keep) {
	case 0:
		return pfAlways
	case 1:
		return keep[0]
	}
	return &prefilter{op: pfOr, subs: keep}
}

// canonRune is the smallest rune of r's simple-fold orbit.
func canonRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		m = min(m, f)
	}
	return m
}

func canonRunes(rs []rune) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteRune(canonRune(r))
	}
	return b.String()
}

// canon is s in fold-canonical form. Invalid UTF-8 becomes U+FFFD, as regexp decodes it.
func canon(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			b = append(b, c)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		b = utf8.AppendRune(b, canonRune(r))
		i += n
	}
	return string(b)
}
