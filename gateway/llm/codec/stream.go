package codec

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"math/bits"
	"unicode/utf8"
	"unsafe"
)

// Streams are decoded event by event. Most events are deltas: a few bytes of text or tool input
// in an envelope that never changes shape. Each protocol's stream decoder reads those with a
// fast path that reads the event's JSON in one validating pass (jsonReader), picks out the
// members it needs and appends their decoded text straight into the block being assembled, with
// no allocation. An event the fast path does not recognize exactly (another type, a member it
// does not handle, a member name that could match case-insensitively, a repeated member, a value
// of an unexpected type, JSON it does not accept) goes to the decoder the event always went to,
// so the result and every error are what they were.

// jsonReader reads one JSON value, validating it as the decoder does (decodeOptions: RFC 8259,
// invalid UTF-8 within strings and duplicate names allowed), while a fast path picks members
// out of it. Every method reports false for input the decoder would reject, and may report false
// for input it would accept (nesting deeper than maxFastDepth): a false only sends the event to
// the decoder. A fast path acts on what it read only once done reports true.
type jsonReader struct {
	b     []byte
	i     int
	depth int
	plain bool // the last string read is ASCII without escapes
}

const maxFastDepth = 64

func newJSONReader(b []byte) *jsonReader {
	r := &jsonReader{b: b}
	r.ws()
	return r
}

// done reports that nothing but whitespace follows the value read.
func (r *jsonReader) done() bool {
	r.ws()
	return r.i == len(r.b)
}

func (r *jsonReader) ws() {
	for r.i < len(r.b) {
		switch r.b[r.i] {
		case ' ', '\t', '\r', '\n':
			r.i++
		default:
			return
		}
	}
}

// is consumes c if it is next.
func (r *jsonReader) is(c byte) bool {
	if r.i < len(r.b) && r.b[r.i] == c {
		r.i++
		return true
	}
	return false
}

// skip reads any value.
func (r *jsonReader) skip() bool {
	if r.i >= len(r.b) {
		return false
	}
	switch r.b[r.i] {
	case '"':
		_, ok := r.quoted()
		return ok
	case '{':
		return r.object(func([]byte) bool { return r.skip() }, true)
	case '[':
		return r.array(r.skip)
	case 't':
		return r.literal("true")
	case 'f':
		return r.literal("false")
	case 'n':
		return r.literal("null")
	}
	return r.number()
}

// object reads an object, calling member with each member's name, which must read the member's
// value. Unless anyName, it reports false for a name that is escaped or not lower-case ASCII
// (see plainName): struct decoding could match it to a field case-insensitively.
func (r *jsonReader) object(member func(name []byte) bool, anyName bool) bool {
	if !r.is('{') || r.depth >= maxFastDepth {
		return false
	}
	r.depth++
	r.ws()
	if !r.is('}') {
		for {
			q, ok := r.quoted()
			if !ok {
				return false
			}
			name := q[1 : len(q)-1]
			if !anyName && (!r.plain || !plainName(name)) {
				return false
			}
			r.ws()
			if !r.is(':') {
				return false
			}
			r.ws()
			if !member(name) {
				return false
			}
			r.ws()
			if r.is('}') {
				break
			}
			if !r.is(',') {
				return false
			}
			r.ws()
		}
	}
	r.depth--
	return true
}

// array reads an array, calling elem to read each element.
func (r *jsonReader) array(elem func() bool) bool {
	if !r.is('[') || r.depth >= maxFastDepth {
		return false
	}
	r.depth++
	r.ws()
	if !r.is(']') {
		for {
			if !elem() {
				return false
			}
			r.ws()
			if r.is(']') {
				break
			}
			if !r.is(',') {
				return false
			}
			r.ws()
		}
	}
	r.depth--
	return true
}

func (r *jsonReader) literal(s string) bool {
	if !bytes.HasPrefix(r.b[r.i:], []byte(s)) {
		return false
	}
	r.i += len(s)
	return true
}

// isNull reads a null if one is next.
func (r *jsonReader) isNull() bool { return r.literal("null") }

// number reads -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?.
func (r *jsonReader) number() bool {
	r.is('-')
	switch {
	case r.is('0'):
	case r.i < len(r.b) && '1' <= r.b[r.i] && r.b[r.i] <= '9':
		r.digits()
	default:
		return false
	}
	if r.is('.') && !r.digits() {
		return false
	}
	if r.is('e') || r.is('E') {
		if !r.is('+') {
			r.is('-')
		}
		if !r.digits() {
			return false
		}
	}
	return true
}

// digits reads one or more digits.
func (r *jsonReader) digits() bool {
	start := r.i
	for r.i < len(r.b) && '0' <= r.b[r.i] && r.b[r.i] <= '9' {
		r.i++
	}
	return r.i > start
}

// intValue reads a value decoded into an int: an integer that fits. It reports false for anything
// else, null included, which the decoder handles.
func (r *jsonReader) intValue() (int, bool) {
	start := r.i
	if !r.number() {
		return 0, false
	}
	v := r.b[start:r.i]
	neg := v[0] == '-'
	if neg {
		v = v[1:]
	}
	if len(v) > 18 {
		return 0, false
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

// quoted reads a string, returning it as in the input: no raw control characters, only valid
// escapes, any other byte, valid UTF-8 or not. It sets r.plain when the string is ASCII without
// escapes, so that it decodes to its bytes between the quotes.
func (r *jsonReader) quoted() ([]byte, bool) {
	if !r.is('"') {
		return nil, false
	}
	b, start, i := r.b, r.i-1, r.i
	r.plain = true
	for {
		if i+8 <= len(b) {
			w := binary.LittleEndian.Uint64(b[i:])
			m := quoteOrEscape(w) | (w-lsb*0x20)&^w&msb | w&msb // the lowest flagged byte is exact
			if m == 0 {
				i += 8
				continue
			}
			i += bits.TrailingZeros64(m) >> 3
		}
		if i >= len(b) {
			return nil, false
		}
		switch c := b[i]; {
		case c == '"':
			r.i = i + 1
			return b[start:r.i], true
		case c == '\\':
			r.plain = false
			if i+1 >= len(b) {
				return nil, false
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(b) {
					return nil, false
				}
				for _, h := range b[i+2 : i+6] {
					if !('0' <= h && h <= '9' || 'a' <= h && h <= 'f' || 'A' <= h && h <= 'F') {
						return nil, false
					}
				}
				i += 6
			default:
				return nil, false
			}
		case c < 0x20:
			return nil, false
		case c >= utf8.RuneSelf:
			r.plain = false
			i++
		default:
			i++
		}
	}
}

// stringOrNull reads a value decoded into a string, setting *q to it as in the input; null
// leaves *q as it is.
func (r *jsonReader) stringOrNull(q *[]byte) bool {
	if r.isNull() {
		return true
	}
	s, ok := r.quoted()
	if ok {
		*q = s
	}
	return ok
}

// validJSON reports that b is one JSON value the decoder accepts, with optional surrounding
// whitespace; false may also mean it is nested deeper than maxFastDepth.
func validJSON(b []byte) bool {
	r := newJSONReader(b)
	return r.skip() && r.done()
}

// appendUnquoted appends the decoded string q to dst, as unquote decodes it.
func appendUnquoted(dst, q []byte) []byte {
	inner := q[1 : len(q)-1]
	if bytes.IndexByte(inner, '\\') < 0 && utf8.Valid(inner) {
		return append(dst, inner...)
	}
	dst, _ = jsontext.AppendUnquote(dst, q)
	return dst
}

// bytesString is b as a string, without copying; b must not change afterwards.
func bytesString(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

// setString sets *dst to the decoded string q, allocating only when it differs.
func setString(dst *string, q []byte) {
	inner := q[1 : len(q)-1]
	if bytes.IndexByte(inner, '\\') < 0 && utf8.Valid(inner) {
		if string(inner) != *dst {
			*dst = string(inner)
		}
		return
	}
	*dst = unquote(q)
}

// unquotedType is a type member's decoded value, not copied unless escaped.
func unquotedType(q []byte) []byte {
	if q == nil {
		return nil
	}
	return unquoted(q, bytes.IndexByte(q, '\\') >= 0)
}

// jsonString is a string read by a fast path: as in the input, and whether it is plain.
type jsonString struct {
	q     []byte
	plain bool
}

// string reads a string.
func (r *jsonReader) string() (jsonString, bool) {
	q, ok := r.quoted()
	return jsonString{q, r.plain}, ok
}

// appendTo appends the decoded string to dst.
func (s jsonString) appendTo(dst []byte) []byte {
	if s.plain {
		return append(dst, s.q[1:len(s.q)-1]...)
	}
	return appendUnquoted(dst, s.q)
}

// set sets *dst to the decoded string, allocating only when it differs.
func (s jsonString) set(dst *string) {
	if s.plain {
		if inner := s.q[1 : len(s.q)-1]; string(inner) != *dst {
			*dst = string(inner)
		}
		return
	}
	setString(dst, s.q)
}

// empty reports that the string decodes to "".
func (s jsonString) empty() bool { return len(s.q) == 2 }
