package threatrules

import (
	"reflect"
	"time"
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

// eventActivation binds one event to the variable rules address it by, and holds what the scan
// has worked out of the event's texts (textatoms.go), so every rule that matches a pattern on a
// field shares one reading of it.
type eventActivation struct {
	e any
	// texts are the texts a pattern was asked of; lastText is the one asked of last. atoms is
	// the automaton they are read with, and textSlab and bitSlab what is left of the allocation
	// their records and bit sets are cut from.
	texts    map[string]*textAtoms
	lastText *textAtoms
	atoms    *automaton
	textSlab []textAtoms
	bitSlab  []uint64
	// The class of sizes the event is within (matcher.go), measured on first use.
	measured bool
	sizes    sizeClass
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

// atomsOf returns what the scan knows of a text of the event: its atoms, read on first use, and
// the patterns run on it. Calls come in runs on the same text (every pattern a rule asks of one
// word), so the last answer is tried first, by the text's address rather than its bytes.
func (a *eventActivation) atomsOf(s string) *textAtoms {
	if t := a.lastText; t != nil && len(s) == len(t.text) && unsafe.StringData(s) == unsafe.StringData(t.text) {
		return t
	}
	t, ok := a.texts[s]
	if !ok {
		if a.texts == nil {
			a.texts = map[string]*textAtoms{}
		}
		t = a.readText(s)
		a.texts[s] = t
	}
	a.lastText = t
	return t
}

// readText reads the atoms of a text the scan has not seen. An event has some twenty texts,
// most of them words, and their records and bit sets are cut from one allocation for every
// textsAtOnce of them.
func (a *eventActivation) readText(s string) *textAtoms {
	if a.atoms == nil {
		a.atoms = atomsFor()
	}
	words := (a.atoms.n + 63) / 64
	if len(a.textSlab) == 0 {
		a.textSlab = make([]textAtoms, textsAtOnce)
		a.bitSlab = make([]uint64, textsAtOnce*words)
	}
	t := &a.textSlab[0]
	t.text, t.auto, t.present = s, a.atoms, a.bitSlab[:words:words]
	a.textSlab, a.bitSlab = a.textSlab[1:], a.bitSlab[words:]
	if !a.atoms.read(s, true, t.present) {
		clear(t.present)
		a.atoms.read(t.canonical(), false, t.present)
	}
	return t
}

const textsAtOnce = 16

// class returns the smallest size class holding every string and list a rule can read of the
// activation's event, worked out on first use, and false when none does.
func (a *eventActivation) class() (sizeClass, bool) {
	if !a.measured {
		a.measured = true
		a.sizes = noSizeClass
		if o, isObj := a.e.(*fastObj); isObj {
			sz := eventSizes{}
			if sz.walk(o.t.a, o.rv, 0) {
				a.sizes = classOf(sz)
			}
		}
	}
	return a.sizes, a.sizes != noSizeClass
}

// eventSizes is how large the values of an event are where a rule can read them: the fields the
// CEL environment exposes, which leaves out interface{} values (a tool's raw result, raw
// attributes) that no rule can address. The values are measured in four classes, by how many
// lists and maps a value is inside, because that is what decides how often a rule reads one:
//
//   - long: the largest string or bytes value that is in no list, or in the entries of one (a
//     command line, a prompt, a tool result; a parsed command's name, args and span);
//   - word: the largest inside a list or map of an entry (a parsed command's words, the paths
//     it reads and writes, its environment);
//   - top: the longest list or map that is in no other (the parsed commands of a line);
//   - nested: the longest inside the entries of another (the words of one command).
//
// A rule loops over the commands, and for each over its words, so the cost of what it asks of
// a word is multiplied by top and by nested. One size for every string and one for every list
// charged each of a command's words as if it were as long as the whole line, and each
// command as if it had as many words as the line has commands.
type eventSizes struct{ long, word, top, nested int }

// walk measures v, of a type the event's CEL types hold and inside depth lists or maps, and
// reports false for a value it does not know how to measure.
func (z *eventSizes) walk(a *fastAdapter, v reflect.Value, depth int) bool {
	switch v.Kind() {
	case reflect.String:
		z.text(v.Len(), depth)
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
	case reflect.Pointer:
		if !v.IsNil() {
			return z.walk(a, v.Elem(), depth)
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			z.text(v.Len(), depth)
			return true
		}
		z.entries(v.Len(), depth)
		for i := range v.Len() {
			if !z.walk(a, v.Index(i), depth+1) {
				return false
			}
		}
	case reflect.Map:
		z.entries(v.Len(), depth)
		for it := v.MapRange(); it.Next(); {
			if !z.walk(a, it.Key(), depth+1) || !z.walk(a, it.Value(), depth+1) {
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
			if !z.walk(a, v.Field(index), depth) {
				return false
			}
		}
	default:
		return false
	}
	return true
}

// text records a string or bytes value of n bytes inside depth lists or maps. A string's size
// to CEL is its code points, which are no more than its bytes.
func (z *eventSizes) text(n, depth int) {
	if depth <= longDepth {
		z.long = max(z.long, n)
	} else {
		z.word = max(z.word, n)
	}
}

// entries records a list or map of n entries inside depth lists or maps.
func (z *eventSizes) entries(n, depth int) {
	if depth == 0 {
		z.top = max(z.top, n)
	} else {
		z.nested = max(z.nested, n)
	}
}

// longDepth is the deepest a string can be inside lists and maps and still be measured as long.
const longDepth = 1
