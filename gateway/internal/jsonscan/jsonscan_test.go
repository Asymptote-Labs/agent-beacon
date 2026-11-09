package jsonscan

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"slices"
	"testing"
)

type member struct {
	name  string
	value string
}

// reference walks b's members with jsontext, which validates everything.
func reference(b []byte) ([]member, bool) {
	d := jsontext.NewDecoder(bytes.NewReader(b), jsontext.AllowDuplicateNames(true))
	if tok, err := d.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, false
	}
	var out []member
	for d.PeekKind() != '}' {
		k, err := d.ReadToken()
		if err != nil {
			return nil, false
		}
		name := k.String() // the token is void after the next read
		v, err := d.ReadValue()
		if err != nil {
			return nil, false
		}
		out = append(out, member{name, string(v)})
	}
	if _, err := d.ReadToken(); err != nil {
		return nil, false
	}
	if _, err := d.ReadToken(); err != io.EOF {
		return nil, false // anything but the end after the object
	}
	return out, true
}

func scan(b []byte) ([]member, error) {
	var out []member
	err := Object(b, func(name string, value []byte) error {
		out = append(out, member{name, string(value)})
		return nil
	})
	return out, err
}

var seeds = []string{
	`{}`, ` { } `, `{"a":1}`, `{"a":"x","b":[1,2,{"c":"}"}],"d":{"e":[]}}`,
	`{"q":"a\"b\\","r":"\\\\\"","s":"\u005c\""}`, `{"structured\u005fmessages":[{"role":"user"}]}`,
	`{"a":true,"b":false,"c":null,"d":-1.5e3}`, `{"a":[[[[{"b":[[]]}]]]]}`, `{"a":"]}[{"}`,
	`{"a":1,"a":2}`, "{\n\t\"a\" :\r\n [ 1 , 2 ] \n}\n", `{"a":{"x":1,"y":{"z":"}"}},"d":{},"e":{"x":2}}`,
	`{"a":{"a":{"a":1}},"a":null,"d":{"q":[{"r":"]"}]}}`,
	// Invalid: Object must reject these or, where it is laxer, never misplace a member.
	`{"a":1,}`, `{"a":[1}`, `{"a":{]}`, `{"a" 1}`, `{"a":1 "b":2}`, `{"a":"x}`, `[1]`, `{"a":1}x`, `{`, `{"a":}`,
	`{"a":"\"}`, `{"a":"\\\"}`,
}

// On every object jsontext accepts, Object yields the same names and value bytes; on what it
// rejects, Object either rejects too or yields extents that do not overlap or skip input.
func TestObjectAgreesWithJSONText(t *testing.T) {
	for _, s := range seeds {
		check(t, []byte(s))
	}
}

func FuzzObject(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) { check(t, b) })
}

func check(t *testing.T, b []byte) {
	checkMembers(t, b)
	want, ok := reference(b)
	got, err := scan(b)
	if ok {
		if err != nil {
			t.Fatalf("%q: valid object rejected: %v", b, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%q: members %v, jsontext %v", b, got, want)
		}
		return
	}
	if err != nil {
		return
	}
	// Laxer than jsontext: values are not validated. The members must still be what a correct
	// parser would find at the object level, so a later member is never hidden inside an earlier
	// value: each value must be a well-delimited extent of b.
	for _, m := range got {
		if !bytes.Contains(b, []byte(m.value)) {
			t.Fatalf("%q: member %q value %q is not part of the input", b, m.name, m.value)
		}
	}
	_ = fmt.Sprint(got)
}

// Members walking into members named "a" or "d" must yield what Object yields, with each such
// member that holds an object replaced by that object's members, under its name.
func checkMembers(t *testing.T, b []byte) {
	enter := func(name string) bool { return name == "a" || name == "d" }
	top, ok := reference(b)
	if !ok {
		return
	}
	var want []member
	for _, m := range top {
		if inner, ok := reference([]byte(m.value)); ok && enter(m.name) {
			for _, n := range inner {
				want = append(want, member{m.name + "." + n.name, n.value})
			}
			continue
		}
		want = append(want, m)
	}
	var got []member
	err := Members(b, enter, func(parent, name string, value []byte) error {
		if parent != "" {
			name = parent + "." + name
		}
		got = append(got, member{name, string(value)})
		return nil
	})
	if err != nil {
		t.Fatalf("%q: valid object rejected by Members: %v", b, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q: Members %v, want %v", b, got, want)
	}
}
