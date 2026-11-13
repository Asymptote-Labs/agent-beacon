package threatrules

import (
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// A prefilter asks whether a text contains each of a pattern's literals (atoms), and a scan asks
// that of the same few texts for every pattern of every rule: on real command lines, some 690
// pattern questions an event about twenty distinct texts (the line, a few spans, the words),
// each answered by its own substring searches, which together read a hundred times the text
// there was. Here the question is turned round. Every atom of every compiled pattern is in one
// table, and a text is read once, by an automaton that reports which atoms it holds (textAtoms).
// A prefilter is then a few bit tests.

// atomTable numbers the atoms of every pattern compiled so far. An atom's number never changes,
// so a prefilter holds its atoms' numbers.
var atomTable struct {
	mu    sync.Mutex
	ids   map[string]uint32 // a canonical atom -> its number, from 1
	atoms []string          // atoms[number-1]
	count atomic.Int32      // len(atoms)
	built atomic.Pointer[automaton]
}

// maxAtoms bounds the table. A pack has a few thousand atoms; past the bound an atom has no
// number and is searched for in the text, as every atom was before.
const maxAtoms = 1 << 15

// atomID returns the number of a canonical atom, or 0 when the table is full.
func atomID(atom string) uint32 {
	if atom == "" {
		return 0
	}
	atomTable.mu.Lock()
	defer atomTable.mu.Unlock()
	if id, ok := atomTable.ids[atom]; ok {
		return id
	}
	if len(atomTable.atoms) >= maxAtoms {
		return 0
	}
	if atomTable.ids == nil {
		atomTable.ids = map[string]uint32{}
	}
	atomTable.atoms = append(atomTable.atoms, atom)
	id := uint32(len(atomTable.atoms))
	atomTable.ids[atom] = id
	atomTable.count.Store(int32(id))
	return id
}

// automaton is an Aho-Corasick automaton over the first n atoms of the table: it reads a
// canonical text a byte at a time and reports every atom that ends at each byte. It is built
// with every transition filled in, so a byte is one table lookup, over the bytes the atoms use
// (class) rather than all 256. A lowercase ASCII letter reads as its uppercase, which is its
// canonical form, so a text of ASCII alone is read as it is written.
type automaton struct {
	n       int
	classes int32
	class   [256]uint16
	next    []int32  // next[state*classes+class]
	ends    []bool   // ends[state]: an atom ends here
	first   []int32  // first[state]..first[state+1] in found
	found   []uint32 // the atoms that end at each state, numbered from 0
}

// atomsFor returns an automaton to read texts with. Atoms numbered after it was built are not
// in it and are searched for in the text instead (textAtoms.holds), so it is rebuilt only when
// enough have been added: a process compiles its rules and then evaluates, and a test that
// compiles a pattern between evaluations does not rebuild for each.
func atomsFor() *automaton {
	a := atomTable.built.Load()
	n := int(atomTable.count.Load())
	if a != nil && n-a.n < max(32, a.n/8) {
		return a
	}
	atomTable.mu.Lock()
	defer atomTable.mu.Unlock()
	if a = atomTable.built.Load(); a != nil && len(atomTable.atoms)-a.n < max(32, a.n/8) {
		return a
	}
	a = buildAutomaton(atomTable.atoms)
	atomTable.built.Store(a)
	return a
}

func buildAutomaton(atoms []string) *automaton {
	a := &automaton{n: len(atoms), classes: 1}
	for _, atom := range atoms {
		for i := 0; i < len(atom); i++ {
			if b := atom[i]; a.class[b] == 0 {
				a.class[b] = uint16(a.classes)
				a.classes++
			}
		}
	}
	for b := byte('a'); b <= 'z'; b++ {
		a.class[b] = a.class[b-'a'+'A']
	}
	// The trie: state 0 is the start, and a transition not yet known is -1.
	nc := a.classes
	row := func() {
		for range nc {
			a.next = append(a.next, -1)
		}
	}
	row()
	var own [][]uint32
	own = append(own, nil)
	for id, atom := range atoms {
		state := int32(0)
		for i := 0; i < len(atom); i++ {
			at := state*nc + int32(a.class[atom[i]])
			if a.next[at] < 0 {
				a.next[at] = int32(len(own))
				row()
				own = append(own, nil)
			}
			state = a.next[at]
		}
		own[state] = append(own[state], uint32(id))
	}
	// Breadth first: a state's failure state is nearer the start, so it is complete when the
	// state is reached, and the state takes over its transitions and its atoms.
	states := int32(len(own))
	fail := make([]int32, states)
	queue := make([]int32, 0, states)
	for c := range nc {
		if s := a.next[c]; s < 0 {
			a.next[c] = 0
		} else {
			queue = append(queue, s)
		}
	}
	for head := 0; head < len(queue); head++ {
		s := queue[head]
		own[s] = append(own[s], own[fail[s]]...)
		for c := range nc {
			t := a.next[s*nc+c]
			if t < 0 {
				a.next[s*nc+c] = a.next[fail[s]*nc+c]
				continue
			}
			fail[t] = a.next[fail[s]*nc+c]
			queue = append(queue, t)
		}
	}
	a.ends, a.first = make([]bool, states), make([]int32, states+1)
	for s, ids := range own {
		a.ends[s] = len(ids) > 0
		a.first[s] = int32(len(a.found))
		a.found = append(a.found, ids...)
	}
	a.first[states] = int32(len(a.found))
	return a
}

// read sets, in present, the bit of every atom canonical text holds. With raw set, text is as
// it is written and is read only while it is ASCII: at the first other byte read stops and
// reports false, and the caller reads the text's canonical form instead.
func (a *automaton) read(text string, raw bool, present []uint64) bool {
	state, nc := int32(0), a.classes
	for i := 0; i < len(text); i++ {
		b := text[i]
		if raw && b >= utf8.RuneSelf {
			return false
		}
		state = a.next[state*nc+int32(a.class[b])]
		if a.ends[state] {
			for _, id := range a.found[a.first[state]:a.first[state+1]] {
				present[id>>6] |= 1 << (id & 63)
			}
		}
	}
	return true
}

// textAtoms is what a scan knows of one text of one event: which atoms it holds, and the answer
// of every pattern that was run on it, since a third of the questions an event is asked are the
// same pattern on the same text from another rule.
type textAtoms struct {
	text string
	auto *automaton
	// present has a bit for each atom of auto the text holds, by number from 0.
	present []uint64
	// canon is the text's canonical form, worked out when the text is not ASCII, or when an atom
	// the automaton does not know has to be searched for.
	canon   string
	canonOK bool
	ran     []patternRun
}

type patternRun struct {
	re      *regexp.Regexp
	matched bool
}

func (t *textAtoms) canonical() string {
	if !t.canonOK {
		t.canon, t.canonOK = canon(t.text), true
	}
	return t.canon
}

// holds reports whether the text holds the atom of a prefilter.
func (t *textAtoms) holds(p *prefilter) bool {
	id := p.id
	if id == 0 || int(id) > t.auto.n {
		return strings.Contains(t.canonical(), p.atom)
	}
	id--
	return t.present[id>>6]&(1<<(id&63)) != 0
}

// matches runs a pattern on the text, once.
func (t *textAtoms) matches(re *regexp.Regexp) bool {
	for i := range t.ran {
		if t.ran[i].re == re {
			return t.ran[i].matched
		}
	}
	matched := re.MatchString(t.text)
	t.ran = append(t.ran, patternRun{re: re, matched: matched})
	return matched
}

// holdsIn is holds for a text whose atoms have been read: holds(canon(text)) by another route.
func (p *prefilter) holdsIn(t *textAtoms) bool {
	switch p.op {
	case pfAtom:
		return t.holds(p)
	case pfAnd:
		for _, s := range p.subs {
			if !s.holdsIn(t) {
				return false
			}
		}
		return true
	case pfOr:
		for _, s := range p.subs {
			if s.holdsIn(t) {
				return true
			}
		}
		return false
	}
	return true
}
