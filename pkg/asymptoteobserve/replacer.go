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
// child, while a table per state would cost a row per byte of every literal. A step follows at
// most one edge forward and, on the way, failure links that each shorten the state, so a pass
// costs at most twice the text's length in steps, each reading at most 256 edges.
type literalFinder struct {
	root    [256]int32 // the root's child by byte; 0 for none
	label   []byte     // per state, the byte of the edge into it
	child   []int32    // per state, its first child; 0 for none
	sibling []int32    // per state, its parent's next child; 0 for none
	fail    []int32    // per state, the longest proper suffix of its string that is a state
	longest []int32    // per state, the length of the longest literal its string ends with
	work    int        // edges and links followed
}

func newLiteralFinder(values []string) *literalFinder {
	f := &literalFinder{label: []byte{0}, child: []int32{0}, sibling: []int32{0}, longest: []int32{0}}
	for _, v := range values {
		s := int32(0)
		for i := len(v) - 1; i >= 0; i-- {
			c := v[i]
			next := f.next(s, c)
			if next <= 0 {
				next = int32(len(f.label))
				f.label = append(f.label, c)
				f.child = append(f.child, 0)
				f.longest = append(f.longest, 0)
				if s == 0 {
					f.sibling = append(f.sibling, 0)
					f.root[c] = next
				} else {
					f.sibling = append(f.sibling, f.child[s])
					f.child[s] = next
				}
			}
			s = next
		}
		f.longest[s] = int32(len(v))
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

// next returns s's child by c: 0 from the root, and -1 from another state, when it has none.
func (f *literalFinder) next(s int32, c byte) int32 {
	if s == 0 {
		return f.root[c]
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

// replace replaces the literals in t with place, as replaceLiterals does, and returns the work
// done.
func (f *literalFinder) replace(t string, place string) (string, int) {
	// From the end: the longest literal that starts at each byte, latest first.
	type literalAt struct{ start, n int }
	var starts []literalAt
	s := int32(0)
	for i := len(t) - 1; i >= 0; i-- {
		s = f.step(s, t[i])
		if n := f.longest[s]; n > 0 {
			starts = append(starts, literalAt{i, int(n)})
		}
	}
	work := len(f.label) + len(t) + f.work
	if len(starts) == 0 {
		return t, work
	}
	var b strings.Builder
	b.Grow(len(t))
	end := 0
	for k := len(starts) - 1; k >= 0; k-- {
		if c := starts[k]; c.start >= end {
			b.WriteString(t[end:c.start])
			b.WriteString(place)
			end = c.start + c.n
		}
	}
	b.WriteString(t[end:])
	return b.String(), work
}
