package asymptoteobserve

import "strings"

// This file replaces literal strings in a text in time linear in the text whatever the literals
// hold. It is what strings.NewReplacer(v1, place, v2, place, …).Replace(t) returns, which for long
// literals is not linear: for one literal Go builds a Boyer-Moore table in time quadratic in its
// length, and for several it reads up to the longest literal's length at each byte of the text.

// replaceLiteralsShort is the longest literal strings.Replacer is used for. Up to it, its search
// reads at most that many bytes at each byte of the text, and its one-literal table costs at most
// its square: linear in the text. Past it, one literal is found by replaceLiteral and several by
// literalFinder, each reading every byte of the text a bounded number of times.
const replaceLiteralsShort = 64

// replaceLiterals replaces the occurrences of values, a non-empty set sorted longest first, with
// place, as strings.NewReplacer(values[0], place, values[1], place, …).Replace(t) does: left to
// right, the longest where several start at one byte, none overlapping. It also returns the work
// done, an upper bound on the bytes and steps read, for callers that account for it.
func replaceLiterals(t string, values []string, place string) (string, int) {
	longest, total := len(values[0]), 0
	for _, v := range values {
		total += len(v)
	}
	switch {
	case longest > replaceLiteralsShort && len(values) == 1:
		return replaceLiteral(t, values[0], place)
	case longest > replaceLiteralsShort:
		return newLiteralFinder(values).replace(t, place)
	}
	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		pairs = append(pairs, v, place)
	}
	return strings.NewReplacer(pairs...).Replace(t), (len(t) + total) * longest
}

// replaceLiteral replaces the occurrences of v in t with place, left to right and none
// overlapping, and returns the work done. It is Knuth-Morris-Pratt's search: where no part of v
// is matched it skips to the next byte v starts with, and otherwise each byte read either extends
// the part matched or shortens it.
func replaceLiteral(t, v, place string) (string, int) {
	// border[j] is the length of the longest proper prefix of v[:j+1] that is also its suffix.
	border := make([]int32, len(v))
	for j, k := 1, 0; j < len(v); j++ {
		for k > 0 && v[j] != v[k] {
			k = int(border[k-1])
		}
		if v[j] == v[k] {
			k++
		}
		border[j] = int32(k)
	}
	var b strings.Builder
	last, k, steps := 0, 0, 0
	for i := 0; i < len(t); i++ {
		if k == 0 {
			j := strings.IndexByte(t[i:], v[0])
			if j < 0 {
				break
			}
			i += j
		}
		for k > 0 && t[i] != v[k] {
			k = int(border[k-1])
			steps++
		}
		if t[i] == v[k] {
			k++
		}
		if k == len(v) {
			if last == 0 {
				b.Grow(len(t))
			}
			b.WriteString(t[last : i+1-len(v)])
			b.WriteString(place)
			last, k = i+1, 0
		}
	}
	work := 2*len(v) + len(t) + steps
	if last == 0 {
		return t, work
	}
	b.WriteString(t[last:])
	return b.String(), work
}

// literalFinder is an Aho-Corasick automaton over literals written backwards. Reading a text from
// its end, the state it is in after reading the byte at i tells the longest literal that starts
// at i, so one pass finds, for every byte, the occurrence strings.Replacer would take there.
//
// A state's children are a list, the root's a table: literals are few and most states have one
// child, while a table per state would cost a row per byte of every literal. A state with more
// than literalListMax children also has them in a map, so following an edge reads at most
// literalListMax list entries or one map entry, however many children a state has. A step
// follows at most one edge forward and, on the way, failure links that each shorten the state, so
// a pass costs at most twice the text's length in edge lookups.
//
// A table per wide state would make each lookup one load too, but it costs 1 KiB however few of
// its 256 entries are used, and literals can be as long as the text: a set of literals made to
// branch widely at every depth would cost that much per few bytes of them. The map costs a few
// words per child.
type literalFinder struct {
	root    [256]int32 // the root's child by byte; 0 for none
	label   []byte     // per state, the byte of the edge into it
	child   []int32    // per state, its first child; 0 for none
	sibling []int32    // per state, its parent's next child; 0 for none
	fanout  []int32    // per state, its number of children
	wide    map[uint64]int32
	fail    []int32 // per state, the longest proper suffix of its string that is a state
	longest []int32 // per state, the length of the longest literal its string ends with
	maxLen  int     // the longest literal's length
	work    int     // list entries and map entries read, and links followed
}

// literalListMax is the most children a state's list is searched for an edge; a state with more
// has them in literalFinder.wide.
const literalListMax = 8

// wideEdge is the key of state s's edge by c in literalFinder.wide.
func wideEdge(s int32, c byte) uint64 {
	return uint64(s)<<8 | uint64(c)
}

func newLiteralFinder(values []string) *literalFinder {
	f := &literalFinder{label: []byte{0}, child: []int32{0}, sibling: []int32{0}, fanout: []int32{0}, longest: []int32{0}}
	for _, v := range values {
		s := int32(0)
		for i := len(v) - 1; i >= 0; i-- {
			c := v[i]
			next := f.next(s, c)
			if next <= 0 {
				next = f.add(s, c)
			}
			s = next
		}
		f.longest[s] = int32(len(v))
		f.maxLen = max(f.maxLen, len(v))
	}
	// Failure links, breadth first, so a state's link and its link's longest are known before
	// its children's.
	f.fail = make([]int32, len(f.label))
	queue := make([]int32, 0, len(f.label))
	for _, s := range f.root {
		if s != 0 {
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for x := f.child[s]; x != 0; x = f.sibling[x] {
			f.fail[x] = f.step(f.fail[s], f.label[x])
			if f.longest[x] == 0 {
				f.longest[x] = f.longest[f.fail[x]]
			}
			queue = append(queue, x)
		}
	}
	return f
}

// add makes a child of s by c and returns it. The state that gets its literalListMax+1st child
// has all of them put in the map.
func (f *literalFinder) add(s int32, c byte) int32 {
	x := int32(len(f.label))
	f.label = append(f.label, c)
	f.child = append(f.child, 0)
	f.fanout = append(f.fanout, 0)
	f.longest = append(f.longest, 0)
	if s == 0 {
		f.sibling = append(f.sibling, 0)
		f.root[c] = x
		return x
	}
	f.sibling = append(f.sibling, f.child[s])
	f.child[s] = x
	f.fanout[s]++
	switch {
	case f.fanout[s] == literalListMax+1:
		if f.wide == nil {
			f.wide = map[uint64]int32{}
		}
		for y := f.child[s]; y != 0; y = f.sibling[y] {
			f.wide[wideEdge(s, f.label[y])] = y
		}
	case f.fanout[s] > literalListMax+1:
		f.wide[wideEdge(s, c)] = x
	}
	return x
}

// next returns s's child by c: 0 from the root, and -1 from another state, when it has none.
func (f *literalFinder) next(s int32, c byte) int32 {
	if s == 0 {
		return f.root[c]
	}
	if f.fanout[s] > literalListMax {
		f.work++
		if x, ok := f.wide[wideEdge(s, c)]; ok {
			return x
		}
		return -1
	}
	for x := f.child[s]; x != 0; x = f.sibling[x] {
		f.work++
		if f.label[x] == c {
			return x
		}
	}
	return -1
}

// step returns the state after reading c in state s.
func (f *literalFinder) step(s int32, c byte) int32 {
	for {
		if next := f.next(s, c); next >= 0 {
			return next
		}
		f.work++
		s = f.fail[s]
	}
}

// longestAt returns, for every byte of t, the length of the longest literal that starts there (0
// for none), and the work done: one pass over t from its end.
func (f *literalFinder) longestAt(t string) ([]int32, int) {
	at := make([]int32, len(t))
	return at, f.longestIn(t, at, 0, len(t))
}

// longestIn sets at[i-lo], for every i in [lo, hi), to the length of the longest literal that
// starts at i in t, and returns the work done. The state the pass from the end is in at a byte
// depends only on the bytes up to the longest literal's length after it, so the pass starts that
// far past hi, or at the end: it reads at most that many bytes more than it sets.
func (f *literalFinder) longestIn(t string, at []int32, lo, hi int) int {
	before := f.work
	from := min(len(t), hi+f.maxLen)
	s := int32(0)
	for i := from - 1; i >= lo; i-- {
		s = f.step(s, t[i])
		if i < hi {
			at[i-lo] = f.longest[s]
		}
	}
	return from - lo + f.work - before
}

// replace replaces the literals in t with place, as replaceLiterals does, and returns the work
// done.
func (f *literalFinder) replace(t string, place string) (string, int) {
	at, _ := f.longestAt(t)
	// The states built, the text, and the edges and links followed building and reading.
	work := len(f.label) + len(t) + f.work
	var b strings.Builder
	last := 0
	for i := 0; i < len(t); {
		n := int(at[i])
		if n == 0 {
			i++
			continue
		}
		if last == 0 {
			b.Grow(len(t))
		}
		b.WriteString(t[last:i])
		b.WriteString(place)
		i += n
		last = i
	}
	if last == 0 {
		return t, work
	}
	b.WriteString(t[last:])
	return b.String(), work
}
