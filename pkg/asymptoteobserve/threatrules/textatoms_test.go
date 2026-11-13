package threatrules

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// tableAtoms returns the atoms of every pattern compiled so far.
func tableAtoms() []string {
	atomTable.mu.Lock()
	defer atomTable.mu.Unlock()
	return append([]string(nil), atomTable.atoms...)
}

// Reading a text's atoms with the automaton is another route to what a prefilter asks, and a
// rule stops firing if the two ever differ. Every prefilter of the corpus is asked of every
// fixture string and its fold-case spellings both ways: with an automaton that knows every
// atom, with one that knows half of them, so that a prefilter mixes atoms it has read with
// atoms it searches for, and with one that knows none.
func TestTextAtomsAgreeWithPrefilter(t *testing.T) {
	rules := corpusRules(t)
	var filters []*prefilter
	for _, p := range corpusPatterns(t, rules) {
		pf, err := compilePrefilter(p)
		if err != nil {
			t.Fatal(err)
		}
		if pf.op != pfTrue {
			filters = append(filters, pf)
		}
	}
	texts := []string{"", "a\xffb", "my ſecret", "\u212aey", "É/é", strings.Repeat("rm -rf / ", 400)}
	for _, text := range eventStrings(t, fixtureEvents(t, rules)) {
		texts = append(texts, foldVariants(text)...)
	}
	if len(filters) < 100 || len(texts) < 500 {
		t.Fatalf("corpus too small to mean anything: %d prefilters, %d texts", len(filters), len(texts))
	}
	// A prefilter's answer for a text, by the search, is the same for every automaton.
	want := make([][]bool, len(texts))
	for i, text := range texts {
		c := canon(text)
		want[i] = make([]bool, len(filters))
		for j, pf := range filters {
			want[i][j] = pf.holds(c)
		}
	}
	atoms := tableAtoms()
	for _, tc := range []struct {
		name  string
		known []string
	}{
		{"every atom", atoms},
		{"half the atoms", atoms[:len(atoms)/2]},
		{"no atom", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auto := buildAutomaton(tc.known)
			failures := 0
			for i, text := range texts {
				read := (&eventActivation{atoms: auto}).atomsOf(text)
				for j, pf := range filters {
					if got := pf.holdsIn(read); got != want[i][j] {
						t.Errorf("prefilter %d on %q: %v by the automaton, %v by the search", j, text, got, want[i][j])
						if failures++; failures > 10 {
							t.FailNow()
						}
					}
				}
			}
		})
	}
}

// The automaton reports exactly the atoms a text holds, for atoms that overlap, repeat, and
// are the start, the end or the middle of one another, in any case and with runes outside
// ASCII. A text is asked about with its own beginning, which shares its address: the scan
// remembers the text it was last asked about by address.
func TestAutomatonFindsEveryAtom(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	letters := []string{"a", "B", "b", "-", "/", "é", "É", "ſ", "s", "K", "\u212a", " "}
	random := func(most int) string {
		var b strings.Builder
		for range 1 + rng.IntN(most) {
			b.WriteString(letters[rng.IntN(len(letters))])
		}
		return b.String()
	}
	for round := range 200 {
		var atoms []string
		seen := map[string]bool{}
		for range 1 + rng.IntN(40) {
			if atom := canon(random(5)); !seen[atom] {
				seen[atom] = true
				atoms = append(atoms, atom)
			}
		}
		auto := buildAutomaton(atoms)
		act := &eventActivation{atoms: auto}
		for range 50 {
			text := random(30)
			if rng.IntN(8) == 0 {
				text += "\xff" + random(5)
			}
			for _, part := range []string{text, text[:len(text)/2], text} {
				read := act.atomsOf(part)
				if read.text != part {
					t.Fatalf("round %d: asked about %q, answered for %q", round, part, read.text)
				}
				for id, atom := range atoms {
					got := read.present[id>>6]&(1<<(id&63)) != 0
					if want := strings.Contains(canon(part), atom); got != want {
						t.Fatalf("round %d: atom %q in %q: automaton %v, search %v (atoms %q)", round, atom, part, got, want, atoms)
					}
				}
			}
		}
	}
}

// An atom numbered after the automaton was built is not in it, and is searched for.
func TestAtomNewerThanTheAutomatonIsSearchedFor(t *testing.T) {
	auto := buildAutomaton(tableAtoms())
	pf, err := compilePrefilter(`zqxjkv-newer-than-the-automaton`)
	if err != nil {
		t.Fatal(err)
	}
	if pf.op != pfAtom || int(pf.id) <= auto.n {
		t.Fatalf("prefilter %+v is not one atom numbered after the automaton's %d", pf, auto.n)
	}
	for text, want := range map[string]bool{
		"echo ZQXJKV-newer-than-the-AUTOMATON": true,
		"echo zqxjkv-newer":                    false,
	} {
		if got := pf.holdsIn((&eventActivation{atoms: auto}).atomsOf(text)); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}
