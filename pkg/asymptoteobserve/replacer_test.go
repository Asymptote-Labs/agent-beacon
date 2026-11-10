package asymptoteobserve

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The literal searches replace what strings.Replacer does, over literals that overlap, contain
// each other, start or end alike, and texts made of their pieces: short literals, the same
// literals stretched past replaceLiteralsShort so replaceLiterals takes its own searches too, and
// many literals over a wider alphabet, whose states have more children than a list is searched for
// (literalListMax).
func TestLiteralSearchesAgreeWithReplacer(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 9))
	wide := 0
	for _, c := range []struct {
		alphabet      string
		scale, values int
	}{{"ab", 1, 5}, {"ab", 10, 5}, {"abcdefghijklmnopqrst", 1, 60}, {"abcdefghijklmnopqrst", 10, 60}} {
		piece := func(n int) string {
			b := make([]byte, 0, n*c.scale)
			for range n {
				b = append(b, strings.Repeat(string(c.alphabet[r.IntN(len(c.alphabet))]), c.scale)...)
			}
			return string(b)
		}
		for range 10000 {
			set := map[string]struct{}{}
			for range 1 + r.IntN(c.values) {
				set[piece(1+r.IntN(4))+"ab"[:r.IntN(3)]] = struct{}{}
			}
			values := slices.SortedFunc(maps.Keys(set), func(a, b string) int {
				if len(a) != len(b) {
					return len(b) - len(a)
				}
				return strings.Compare(a, b)
			})
			var text strings.Builder
			for range r.IntN(12) {
				if r.IntN(3) == 0 {
					text.WriteString(piece(1 + r.IntN(4)))
				} else {
					text.WriteString(values[r.IntN(len(values))])
				}
			}
			pairs := make([]string, 0, 2*len(values))
			for _, v := range values {
				pairs = append(pairs, v, "#")
			}
			want := strings.NewReplacer(pairs...).Replace(text.String())
			f := newLiteralFinder(values)
			if f.wide != nil {
				wide++
			}
			if got, _ := f.replace(text.String(), "#"); got != want {
				t.Fatalf("literals %q in %q: got %q, want %q", values, text.String(), got, want)
			}
			if got, _ := replaceLiterals(text.String(), values, "#"); got != want {
				t.Fatalf("replaceLiterals(%q, %q): got %q, want %q", text.String(), values, got, want)
			}
			if len(values) == 1 {
				if got, _ := replaceLiteral(text.String(), values[0], "#"); got != want {
					t.Fatalf("literal %q in %q: got %q, want %q", values[0], text.String(), got, want)
				}
			}
		}
	}
	if wide < 1000 {
		t.Fatalf("only %d literal sets had a state with more than %d children", wide, literalListMax)
	}
}
