package threatrules

import (
	"reflect"
	"time"

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
	canons map[string]string // a string a prefilter looked at -> its canonical form
	// The tier of sizes the event is within (matcher.go), measured on first use.
	measured     bool
	sTier, lTier int
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
	if c, ok := a.canons[s]; ok {
		return c
	}
	if a.canons == nil {
		a.canons = map[string]string{}
	}
	c := canon(s)
	a.canons[s] = c
	return c
}

// tier returns the smallest tier holding every string and list a rule can read of the activation's
// event, worked out on first use, and false when none does.
func (a *eventActivation) tier() (s, l int, ok bool) {
	if !a.measured {
		a.measured = true
		a.sTier, a.lTier = -1, -1
		if o, isObj := a.e.(*fastObj); isObj {
			sz := eventSizes{}
			if sz.walk(o.t.a, o.rv) {
				a.sTier, a.lTier = tierOf(stringTiers[:], sz.maxString), tierOf(listTiers[:], sz.maxList)
			}
		}
	}
	return a.sTier, a.lTier, a.sTier >= 0 && a.lTier >= 0
}

func tierOf(tiers []int, n int) int {
	for i, t := range tiers {
		if n <= t {
			return i
		}
	}
	return -1
}

// eventSizes is the largest string or bytes value and the longest list or map an event holds where
// a rule can read it: the fields the CEL environment exposes, which leaves out interface{} values
// (a tool's raw result, raw attributes) that no rule can address.
type eventSizes struct{ maxString, maxList int }

// walk measures v, of a type the event's CEL types hold, and reports false for a value it does
// not know how to measure.
func (z *eventSizes) walk(a *fastAdapter, v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		z.maxString = max(z.maxString, v.Len())
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
	case reflect.Pointer:
		if !v.IsNil() {
			return z.walk(a, v.Elem())
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			z.maxString = max(z.maxString, v.Len())
			return true
		}
		z.maxList = max(z.maxList, v.Len())
		for i := range v.Len() {
			if !z.walk(a, v.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		z.maxList = max(z.maxList, v.Len())
		for it := v.MapRange(); it.Next(); {
			if !z.walk(a, it.Key()) || !z.walk(a, it.Value()) {
				return false
			}
		}
	case reflect.Struct:
		ft, ok := a.types[v.Type()]
		if !ok {
			// time.Time, which CEL reads as a timestamp; nothing else reaches here.
			return v.Type() == reflect.TypeFor[time.Time]()
		}
		for _, index := range ft.sized {
			if !z.walk(a, v.Field(index)) {
				return false
			}
		}
	default:
		return false
	}
	return true
}
