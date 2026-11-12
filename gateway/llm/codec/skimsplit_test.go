package codec

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/jsonscan"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// sameSkim asserts that skimming body with the caller's split decodes exactly as skimming it
// alone does, or fails as it does.
func sameSkim(t *testing.T, p llm.Protocol, body []byte, split [][]byte) {
	t.Helper()
	want, werr := SkimRequest(p, body)
	got, gerr := SkimSplitRequest(p, body, split)
	if (werr == nil) != (gerr == nil) {
		t.Fatalf("%s: split skim error %v, plain skim error %v\nbody: %s", p, gerr, werr, body)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: split skim differs:\n got %+v\nwant %+v\nbody: %s", p, got, want, body)
	}
}

// split is the adapter's split of body's message array: jsonscan's elements, nil when it finds no
// array there.
func split(t *testing.T, p llm.Protocol, body []byte) [][]byte {
	t.Helper()
	var elems [][]byte
	_ = jsonscan.Split(body, nil, func(array bool, path ...string) bool { return array && len(path) == 1 && path[0] == arrayMember(p) }, func(_, name string, _ []byte, e [][]byte) error {
		if name == arrayMember(p) {
			elems = e
		}
		return nil
	})
	return elems
}

// A body skimmed with the caller's split of its message array decodes as it does alone, and the
// split stands in for the array's scan; a split that is not exactly that array's elements is
// ignored.
func TestSkimSplit(t *testing.T) {
	for _, p := range skimProtocols {
		resetSkimMemo()
		elems := append(append([]string{}, skimElements[p]...), skimTurn[p])
		body := []byte(skimBody(p, elems...))
		good := split(t, p, body)
		start, _ := jsonscan.MemberStart(body, arrayMember(p), nil)
		if scanned, _ := scanArray(body, start); !reflect.DeepEqual(good, scanned) {
			t.Fatalf("%s: split %d elements, the boundary scan %d", p, len(good), len(scanned))
		}
		for _, warm := range []bool{false, true} { // a miss validates each element; a hit replays it
			if plan := planSkim(body, p, memoSeeds[p], good); plan == nil || !plan.split {
				t.Fatalf("%s: the split of the array was not taken (warm %v)", p, warm)
			}
			sameSkim(t, p, body, good)
		}
		copies := make([][]byte, len(good))
		for i, e := range good {
			copies[i] = bytes.Clone(e)
		}
		slices := func(f func([][]byte) [][]byte) [][]byte { return f(append([][]byte{}, good...)) }
		for name, bad := range map[string][][]byte{
			"copies":   copies,
			"missing":  good[:len(good)-1],
			"skipped":  append(append([][]byte{}, good[:1]...), good[2:]...),
			"reversed": slices(func(s [][]byte) [][]byte { s[0], s[1] = s[1], s[0]; return s }),
			"shrunk":   slices(func(s [][]byte) [][]byte { s[0] = s[0][:len(s[0])-1]; return s }),
			"grown":    slices(func(s [][]byte) [][]byte { s[len(s)-1] = s[len(s)-1][:len(s[len(s)-1])+1]; return s }),
			"another":  split(t, p, []byte(skimBody(p, elems...))),
		} {
			if plan := planSkim(body, p, memoSeeds[p], bad); plan == nil || plan.split {
				t.Fatalf("%s: split %s was taken", p, name)
			}
			sameSkim(t, p, body, bad)
		}
		// Two elements given as one are separated as the array separates them, so the split is
		// taken, but the extent is not one value and the skim decodes the body without it.
		merged := slices(func(s [][]byte) [][]byte { s[0] = s[0][:len(s[0])+1+len(s[1])]; return append(s[:1], s[2:]...) })
		resetSkimMemo()
		sameSkim(t, p, body, merged)
	}
}

// A body the plain skim rejects is rejected with the adapter's split too, wherever the split finds
// elements a scan of the array would not, and an element nested beyond the plan's depth is not
// taken from a split.
func TestSkimSplitInvalid(t *testing.T) {
	for _, p := range skimProtocols {
		elems := strings.Join(skimElements[p], ",")
		turn := skimTurn[p]
		bad := skimInvalid[p][0]
		// A message valid alone (9,999 levels) and too deep in the body (10,001; jsontext allows 10,000).
		deep := `{"role":"user","content":"x","extra":` + strings.Repeat("[", 9998) + strings.Repeat("]", 9998) + `}`
		for _, inner := range []string{
			elems + "," + bad + "," + turn,
			elems + ",tru," + turn,
			elems + ",1 2," + turn,
			elems + `,"a""b",` + turn,
			elems + `,{"role":"user","content":"x"}},` + turn,
			elems + `,[{"role":"user"]},` + turn,
			elems + "," + deep + "," + turn,
			elems + `,{"role":"user","content":"` + strings.Repeat(`\\`, 3) + `"},` + turn,
		} {
			for _, warm := range []bool{false, true} {
				resetSkimMemo()
				if warm {
					SkimRequest(p, []byte(skimBody(p, elems, turn)))
				}
				body := []byte(skimBody(p, inner))
				sameSkim(t, p, body, split(t, p, body))
			}
		}
	}
}
