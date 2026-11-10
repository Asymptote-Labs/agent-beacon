package threatrules

import (
	"unsafe"

	"github.com/google/cel-go/interpreter"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// eventInput is one event as the rules of a scan read it. Its activations are built on first use:
// one over the event as given, for the rules that read no derived field, and one over a copy with
// the derived fields filled (withDerivedFields), for the rules that do.
type eventInput struct {
	event          *asymptoteobserve.Event
	plain, derived *eventActivation
}

func (in *eventInput) activation(derived bool) *eventActivation {
	if derived {
		if in.derived == nil {
			in.derived = newEventActivation(withDerivedFields(*in.event))
		}
		return in.derived
	}
	if in.plain == nil {
		in.plain = newEventActivation(*in.event)
	}
	return in.plain
}

// eventActivation binds one event to the variable rules address it by, and caches the canonical
// forms of the strings prefilters look at (see prefilter.go), so every rule that matches a regex
// on a field shares one canonicalization of it.
type eventActivation struct {
	e      any
	canons map[canonKey]canonEntry
}

// newEventActivation binds e as rules read it: an indexed object (fastobj.go), or the event itself
// where Env could not install the indexed types.
func newEventActivation(e asymptoteobserve.Event) *eventActivation {
	if a := fastTypes(); a != nil {
		return &eventActivation{e: a.NativeToValue(e)}
	}
	return &eventActivation{e: e}
}

func (a *eventActivation) ResolveName(name string) (any, bool) {
	if name == eventVar {
		return a.e, true
	}
	return nil, false
}

func (a *eventActivation) Parent() interpreter.Activation { return nil }

func (a *eventActivation) canon(s string) string {
	// Keyed by address, not content (see canonKey); the entry keeps s alive so the key stays its own.
	k := canonKey{unsafe.StringData(s), len(s)}
	if c, ok := a.canons[k]; ok {
		return c.canon
	}
	if a.canons == nil {
		a.canons = map[canonKey]canonEntry{}
	}
	c := canon(s)
	a.canons[k] = canonEntry{orig: s, canon: c}
	return c
}

// canonKey identifies a string by its bytes' address and length: hashing a 3 KB tool result on
// every lookup would cost what the cache saves.
type canonKey struct {
	p *byte
	n int
}

// canonEntry keeps the original string so its memory cannot be freed and reused, under the same
// key, by another string while the activation lives (rules can build strings, e.g. lowerAscii()).
type canonEntry struct {
	orig, canon string
}
