// Package jsonscan walks the members of a JSON object without decoding or validating their values.
//
// A gateway's callout payload wraps the conversation it forwards: LiteLLM's guardrail payload holds
// the messages, tools and a copy of every text; Portkey's webhook holds request.json and
// response.json. Decoding the payload validated and copied all of it, and the codec then read the
// conversation again. With Object, an adapter decodes only its small fields and hands the large
// ones on as bytes of the payload, which the codec validates when it reads them (and its memo lets
// it skip what it validated on earlier calls). A value the adapter never uses is never validated.
package jsonscan

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
)

var errSyntax = errors.New("jsonscan: invalid JSON object")

// Object calls yield for each member of the JSON object b, in order, with the member's name
// unescaped and its value's bytes, which alias b. It checks the object's own syntax and that each
// value is one JSON value with matched brackets and terminated strings, but not what is inside a
// value: a caller that uses a value validates it, typically by decoding it. A name that does not
// unescape to valid UTF-8 is yielded as its raw quoted bytes, which match no field name. Whitespace
// alone may follow the object. Object stops at the first error yield returns.
func Object(b []byte, yield func(name string, value []byte) error) error {
	return Members(b, nil, func(_, name string, value []byte) error { return yield(name, value) })
}

// Members is Object, walking into some members' values in the same pass: a member whose name
// enter accepts and whose value is an object is not yielded itself; its members are, with parent
// set to its name. Every other member is yielded with parent "". Walking in rather than yielding
// the whole value and walking it again reads the bytes once: a gateway envelope's request object
// holds the whole conversation. enter may be nil.
func Members(b []byte, enter func(name string) bool, yield func(parent, name string, value []byte) error) error {
	end, err := object(b, ws(b, 0), "", enter, yield)
	if err != nil {
		return err
	}
	return trailing(b, end)
}

// errFound stops a walk once FirstString has its member.
var errFound = errors.New("found")

// FirstString is the value of the first top-level member of the object b named name, when it is
// a string, and "" otherwise. It reads only as far as that member: a model call names its model
// near the top, ahead of the conversation. Unlike a decoder, which keeps the last of duplicate
// names, it keeps the first.
func FirstString(b []byte, name string) string {
	var out string
	_ = Object(b, func(n string, value []byte) error {
		if n != name {
			return nil
		}
		if len(value) > 0 && value[0] == '"' {
			if s, err := jsontext.AppendUnquote(nil, value); err == nil {
				out = string(s)
			}
		}
		return errFound
	})
	return out
}

// MemberStart is the offset in b of the value of the first top-level member of the object b named
// name, and false when there is none. It reads only as far as that value's start, skipping the
// members before it unvalidated, as Object does. before, when not nil, is handed each member it
// skips: its unescaped name, valid only during the call, and its value.
func MemberStart(b []byte, name string, before func(name, value []byte)) (int, bool) {
	i := ws(b, 0)
	if i >= len(b) || b[i] != '{' {
		return 0, false
	}
	i = ws(b, i+1)
	var buf []byte
	for i < len(b) && b[i] == '"' {
		end, err := skipString(b, i)
		if err != nil {
			return 0, false
		}
		n := b[i+1 : end-1]
		if bytes.IndexByte(n, '\\') >= 0 {
			if buf, err = jsontext.AppendUnquote(buf[:0], b[i:end]); err == nil {
				n = buf
			}
		}
		i = ws(b, end)
		if i >= len(b) || b[i] != ':' {
			return 0, false
		}
		start := ws(b, i+1)
		if string(n) == name {
			return start, start < len(b)
		}
		if i, err = skipValue(b, start); err != nil {
			return 0, false
		}
		if before != nil {
			before(n, b[start:i])
		}
		i = ws(b, i)
		if i >= len(b) || b[i] != ',' {
			return 0, false
		}
		i = ws(b, i+1)
	}
	return 0, false
}

// Elements calls yield with each element of the array starting at b[i] == '[', in order, until
// yield returns false. It reads only as far as the element yield stops at; elements are bracket-
// matched, not validated.
func Elements(b []byte, i int, yield func(elem []byte) bool) error {
	if i >= len(b) || b[i] != '[' {
		return errSyntax
	}
	i = ws(b, i+1)
	if i < len(b) && b[i] == ']' {
		return nil
	}
	for {
		end, err := skipValue(b, i)
		if err != nil {
			return err
		}
		if !yield(b[i:end]) {
			return nil
		}
		i = ws(b, end)
		if i >= len(b) {
			return errSyntax
		}
		switch b[i] {
		case ',':
			i = ws(b, i+1)
		case ']':
			return nil
		default:
			return errSyntax
		}
	}
}

// ValueEnd is the offset just past the value starting at b[i]: a string, an object or array with
// matched brackets, or a literal. It does not validate what is inside.
func ValueEnd(b []byte, i int) (int, error) { return skipValue(b, i) }

// object walks the object starting at b[i] == '{', yielding its members under parent, and returns
// the index just past it. Only the top level (parent "") enters members.
func object(b []byte, i int, parent string, enter func(string) bool, yield func(parent, name string, value []byte) error) (int, error) {
	if i >= len(b) || b[i] != '{' {
		return 0, errSyntax
	}
	i = ws(b, i+1)
	if i < len(b) && b[i] == '}' {
		return i + 1, nil
	}
	var name []byte
	for {
		if i >= len(b) || b[i] != '"' {
			return 0, errSyntax
		}
		end, err := skipString(b, i)
		if err != nil {
			return 0, err
		}
		if name, err = jsontext.AppendUnquote(name[:0], b[i:end]); err != nil {
			name = append(name[:0], b[i:end]...)
		}
		i = ws(b, end)
		if i >= len(b) || b[i] != ':' {
			return 0, errSyntax
		}
		start := ws(b, i+1)
		if parent == "" && enter != nil && start < len(b) && b[start] == '{' && enter(string(name)) {
			end, err = object(b, start, string(name), nil, yield)
		} else if end, err = skipValue(b, start); err == nil {
			err = yield(parent, string(name), b[start:end])
		}
		if err != nil {
			return 0, err
		}
		i = ws(b, end)
		if i >= len(b) {
			return 0, errSyntax
		}
		switch b[i] {
		case ',':
			i = ws(b, i+1)
		case '}':
			return i + 1, nil
		default:
			return 0, errSyntax
		}
	}
}

func trailing(b []byte, i int) error {
	if ws(b, i) != len(b) {
		return errSyntax
	}
	return nil
}

func ws(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// skipString returns the index just past the string starting at b[i] == '"'. It jumps from quote
// to quote: a quote ends the string unless an odd run of backslashes precedes it.
func skipString(b []byte, i int) (int, error) {
	for j := i + 1; ; {
		k := bytes.IndexByte(b[j:], '"')
		if k < 0 {
			return 0, errSyntax
		}
		q := j + k
		n := 0
		for p := q - 1; p >= j && b[p] == '\\'; p-- {
			n++
		}
		if n%2 == 0 {
			return q + 1, nil
		}
		j = q + 1
	}
}

// skipValue returns the index just past the value starting at b[i]: a string, an object or array
// with matched brackets, or a run of literal characters (a number, true, false or null).
func skipValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, errSyntax
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
	case ',', ':', '}', ']':
		return 0, errSyntax
	default:
		j := i
		for j < len(b) && b[j] != ',' && b[j] != '}' && b[j] != ']' && b[j] != ' ' && b[j] != '\t' && b[j] != '\n' && b[j] != '\r' {
			j++
		}
		return j, nil
	}
	var stack [64]byte
	open := stack[:0]
	for j := i; j < len(b); j++ {
		switch c := b[j]; c {
		case '"':
			end, err := skipString(b, j)
			if err != nil {
				return 0, err
			}
			j = end - 1
		case '{':
			open = append(open, '}')
		case '[':
			open = append(open, ']')
		case '}', ']':
			if len(open) == 0 || open[len(open)-1] != c {
				return 0, errSyntax
			}
			if open = open[:len(open)-1]; len(open) == 0 {
				return j + 1, nil
			}
		}
	}
	return 0, errSyntax
}
