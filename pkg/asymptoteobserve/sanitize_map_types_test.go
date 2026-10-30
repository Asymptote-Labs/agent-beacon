package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// The tests in this file check that SanitizeMap and SanitizeSlice sanitize values of every Go type
// in-process callers put in a map, not only the shapes JSON decoding produces, and that what they
// return always marshals.

const typesTestToken = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"

// typesTestText holds a credential RedactString recognizes and runs past DefaultRawStringLimit.
var typesTestText = "clone with " + typesTestToken + " " + strings.Repeat("x", 3000)

type typesTestLabel string

type typesTestRecord struct {
	Name     string   `json:"name"`
	Password string   `json:"password"`
	Notes    []string `json:"notes"`
}

type typesTestMarshaler struct{ text string }

func (m typesTestMarshaler) MarshalText() ([]byte, error) { return []byte(m.text), nil }

// typesTestValues are values of the types SanitizeMap kept as they were, each holding
// typesTestText, and "hunter2" under a field named for a credential where the type has fields.
func typesTestValues() map[string]interface{} {
	text := typesTestText
	record := typesTestRecord{Name: text, Password: "hunter2", Notes: []string{text}}
	return map[string]interface{}{
		"[]string":                    []string{text},
		"map[string]string":           map[string]string{"note": text, "password": "hunter2"},
		"[]map[string]interface{}":    []map[string]interface{}{{"note": text, "password": "hunter2"}},
		"map[string][]string":         map[string][]string{"note": {text}, "Cookie": {"hunter2"}},
		"struct":                      record,
		"pointer to struct":           &record,
		"*string":                     &text,
		"named string type":           typesTestLabel(text),
		"json.RawMessage":             json.RawMessage(`{"note":"` + text + `","password":"hunter2"}`),
		"[]byte":                      []byte(text),
		"encoding.TextMarshaler":      typesTestMarshaler{text},
		"[]interface{} of []string":   []interface{}{[]string{text}},
		"map[string]interface{} of T": map[string]interface{}{"record": record},
	}
}

// checkSanitized fails unless v holds only the shapes JSON decoding produces, Go's scalar types
// and []byte, and every string in it (and every []byte that is text) is free of the credential and
// within limit.
func checkSanitized(t *testing.T, path string, v interface{}, limit int) {
	t.Helper()
	checkText := func(s string) {
		t.Helper()
		if strings.Contains(s, typesTestToken) || strings.Contains(s, "hunter2") {
			t.Errorf("%s: credential left in %.80q", path, s)
		}
		if len(s) > limit {
			t.Errorf("%s: %d bytes, limit %d", path, len(s), limit)
		}
	}
	switch x := v.(type) {
	case nil, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
	case string:
		checkText(x)
	case []byte:
		checkText(string(x))
	case map[string]interface{}:
		for k, item := range x {
			checkSanitized(t, path+"."+k, item, limit)
		}
	case []interface{}:
		for i, item := range x {
			checkSanitized(t, fmt.Sprintf("%s[%d]", path, i), item, limit)
		}
	default:
		t.Errorf("%s: %T returned as it was given", path, v)
	}
}

func TestSanitizeMapSanitizesEveryType(t *testing.T) {
	opts := PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}
	for name, value := range typesTestValues() {
		t.Run(name, func(t *testing.T) {
			checkSanitized(t, "map", SanitizeMap(map[string]interface{}{"value": value}, opts), DefaultRawStringLimit)
			checkSanitized(t, "slice", SanitizeSlice([]interface{}{value}, opts), DefaultRawStringLimit)
			checkSanitized(t, "secret list", SanitizeMap(map[string]interface{}{"Cookie": []interface{}{value}}, opts), DefaultRawStringLimit)
		})
	}
	// A field named for a credential, and an argument after a flag named for one, are redacted
	// whatever type holds them.
	named := SanitizeMap(map[string]interface{}{
		"password": typesTestLabel("hunter2"),
		"Cookie":   []string{"hunter2"},
		"token":    []byte("hunter2"),
		"args":     []typesTestLabel{"--api-key", "hunter2"},
		"argv":     []interface{}{"--api-key", []byte("hunter2")},
		"flags":    []interface{}{typesTestLabel("--api-key"), "hunter2"},
	}, opts)
	checkSanitized(t, "named", named, DefaultRawStringLimit)
	if data, _ := json.Marshal(named["args"]); string(data) != `["--api-key","[REDACTED]"]` {
		t.Errorf("args = %s, want the value after --api-key redacted", data)
	}
}

// Go's number types, booleans and []byte keep their types: the beacon_privacy processor hands
// SanitizeMap an OTLP attribute map and builds the attributes back from what it returns.
func TestSanitizeMapKeepsScalarAndBytesTypes(t *testing.T) {
	binary := append([]byte{0xff, 0xfe}, bytes.Repeat([]byte{0x80}, 3000)...)
	input := map[string]interface{}{
		"count": int64(812), "port": uint16(443), "ratio": float32(1.5), "cost": 0.25,
		"n": json.Number("12"), "ok": true, "text": []byte(typesTestText), "binary": binary, "none": []byte(nil),
	}
	got := SanitizeMap(input, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	for _, key := range []string{"count", "port", "ratio", "cost", "n", "ok", "none"} {
		if !reflect.DeepEqual(got[key], input[key]) {
			t.Errorf("%s = %#v, want %#v", key, got[key], input[key])
		}
	}
	text, ok := got["text"].([]byte)
	if !ok || !utf8.Valid(text) || len(text) > DefaultRawStringLimit || bytes.Contains(text, []byte(typesTestToken)) {
		t.Errorf("text = %T %.80q, want []byte redacted and cut to the limit", got["text"], got["text"])
	}
	if b, ok := got["binary"].([]byte); !ok || !bytes.Equal(b, binary[:DefaultRawStringLimit]) {
		t.Errorf("binary = %T of %d bytes, want the first %d bytes", got["binary"], len(b), DefaultRawStringLimit)
	}
	// Values JSON cannot write are nil.
	for name, value := range map[string]interface{}{
		"NaN": math.NaN(), "infinity": float32(math.Inf(1)), "json.Number": json.Number(typesTestToken),
	} {
		if got := SanitizeMap(map[string]interface{}{"v": value}, PrivacyOptions{RedactSecrets: true})["v"]; got != nil {
			t.Errorf("%s = %#v, want nil", name, got)
		}
	}
}

// Bytes that are not valid UTF-8 as a whole can still hold readable text, Latin-1 here: a
// credential in them is redacted, and every other byte is kept exactly as it was.
func TestSanitizeMapRedactsBytesThatAreNotUTF8(t *testing.T) {
	latin1 := []byte("caf\xe9 clone with " + typesTestToken + " na\xefve")
	opts := PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}
	want := []byte("caf\xe9 clone with ghp_[REDACTED] na\xefve")
	if got := SanitizeMap(map[string]interface{}{"payload": latin1}, opts)["payload"]; !reflect.DeepEqual(got, want) {
		t.Errorf("payload = %q, want %q", got, want)
	}
	if got := SanitizeSlice([]interface{}{latin1}, opts)[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("item = %q, want %q", got, want)
	}
	// A long one is cut to the limit, every byte before the cut kept.
	long := append(bytes.Repeat([]byte("\xe9t\xe9 "), 600), latin1...)
	got, ok := SanitizeMap(map[string]interface{}{"payload": long}, opts)["payload"].([]byte)
	if !ok || !bytes.Equal(got, long[:DefaultRawStringLimit]) {
		t.Errorf("long payload = %d bytes, want the first %d", len(got), DefaultRawStringLimit)
	}
	// A credential straddling the cut is redacted before it is cut, so no part of it is left (its
	// body would start 8 bytes before the cut).
	cut := append(bytes.Repeat([]byte{'\xe9'}, DefaultRawStringLimit-28), latin1...)
	if got := SanitizeMap(map[string]interface{}{"payload": cut}, opts)["payload"].([]byte); bytes.Contains(got, []byte(typesTestToken[4:8])) {
		t.Errorf("credential straddling the cut left a prefix: %q", got[len(got)-40:])
	}
}

type typesTestMeta struct {
	Runtime string `json:"runtime"`
}

type typesTestServer struct {
	typesTestMeta
	Name       string                 `json:"name"`
	Definition map[string]interface{} `json:"definition,omitempty"`
	Note       string                 `json:"note,omitempty"`
	Internal   string                 `json:"-"`
}

// A value JSON cannot represent turns only itself into nil, not the list or the struct holding it.
func TestSanitizeMapKeepsPartsBesideAnUnrepresentableValue(t *testing.T) {
	servers := []typesTestServer{
		{typesTestMeta{"cursor"}, "first", map[string]interface{}{"command": "npx"}, "", "hidden"},
		{typesTestMeta{"codex"}, "broken", map[string]interface{}{"command": "uvx", "timeout": math.NaN(), "note": typesTestText}, "n", ""},
		{typesTestMeta{"cursor"}, "last", nil, "", ""},
	}
	opts := PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}
	want := []interface{}{
		map[string]interface{}{"runtime": "cursor", "name": "first", "definition": map[string]interface{}{"command": "npx"}},
		map[string]interface{}{"runtime": "codex", "name": "broken", "note": "n", "definition": map[string]interface{}{
			"command": "uvx", "timeout": nil, "note": CleanString(typesTestText, DefaultRawStringLimit, true),
		}},
		map[string]interface{}{"runtime": "cursor", "name": "last"},
	}
	if got := SanitizeMap(map[string]interface{}{"servers": servers}, opts)["servers"]; !reflect.DeepEqual(got, want) {
		t.Errorf("servers = %#v\nwant %#v", got, want)
	}
	if got := SanitizeSlice([]interface{}{servers}, opts)[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("servers in a slice = %#v", got)
	}
	data, err := json.Marshal(CleanValue(servers, DefaultRawStringLimit))
	if wantData, _ := json.Marshal(want); err != nil || !bytes.Equal(data, wantData) {
		t.Errorf("CleanValue = %s, %v\nwant %s", data, err, wantData)
	}
}

type typesTestNode struct {
	Name string         `json:"name"`
	Next *typesTestNode `json:"next"`
}

// Whatever SanitizeMap and SanitizeSlice are given, what they return marshals, and so does a
// CleanedValue.
func TestSanitizeMapOutputAlwaysMarshals(t *testing.T) {
	cycle := &typesTestNode{Name: typesTestText}
	cycle.Next = cycle
	values := []interface{}{
		math.NaN(), math.Inf(-1), float32(math.NaN()), make(chan int), func() {}, cycle, complex(1, 2),
		map[bool]string{true: typesTestText}, json.Number("not a number"), json.RawMessage(`{"open":`),
		struct{ F float64 }{math.NaN()}, []float64{1, math.Inf(1)}, map[string]interface{}{"c": make(chan int)},
		[]interface{}{func() {}, typesTestText}, nil, (*typesTestRecord)(nil), []string(nil),
	}
	for _, value := range typesTestValues() {
		values = append(values, value)
	}
	// Nest each value in the others' containers.
	for _, value := range append([]interface{}(nil), values...) {
		values = append(values,
			map[string]interface{}{"nested": []interface{}{value}},
			[]map[string]interface{}{{"nested": value}},
			struct{ V interface{} }{value},
		)
	}
	opts := PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}
	for i, value := range values {
		for what, out := range map[string]interface{}{
			"SanitizeMap":   SanitizeMap(map[string]interface{}{"v": value, "token": value}, opts),
			"SanitizeSlice": SanitizeSlice([]interface{}{"--token", value, value}, opts),
			"CleanValue":    CleanValue(value, DefaultRawStringLimit),
		} {
			if _, err := json.Marshal(out); err != nil {
				t.Errorf("%s of value %d (%T): %v", what, i, value, err)
			}
			if what != "CleanValue" {
				checkSanitized(t, fmt.Sprintf("%s of value %d (%T)", what, i, value), out, DefaultRawStringLimit)
			}
		}
	}
}

// A CleanedValue keeps the limit CleanValue was given, through SanitizeMap and SanitizeEvent,
// with every string in it redacted and its field names left alone; under a field named for a
// credential, or after a flag named for one, it is still redacted.
func TestCleanValueKeepsItsLimit(t *testing.T) {
	content := "dump " + typesTestToken + " " + strings.Repeat("y", 10000)
	cleaned := CleanValue([]map[string]interface{}{{"text": content, "env_keys": []string{"API_TOKEN"}}}, 64<<10)
	opts := PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}

	event := NewEvent(NewEventOptions{Action: "inventory.snapshot", Harness: HarnessInfo{Name: "test"}})
	event.Raw = SanitizeMap(map[string]interface{}{"configs": cleaned}, opts)
	event = SanitizeEvent(event, 1<<20)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, typesTestToken) {
		t.Fatalf("CleanValue left the credential: %.200s", text)
	}
	if !strings.Contains(text, strings.Repeat("y", 10000)) || !strings.Contains(text, `"env_keys":["API_TOKEN"]`) {
		t.Fatalf("CleanValue's text was cut to the raw limit or its names judged: %.200s", text)
	}

	if data, _ := json.Marshal(CleanValue(strings.Repeat("z", 500), 100)); len(data) > 102 {
		t.Errorf("CleanValue ignored its limit: %d bytes", len(data))
	}
	for name, out := range map[string]interface{}{
		"secret field": SanitizeMap(map[string]interface{}{"password": CleanValue("hunter2", 100)}, opts),
		"secret list":  SanitizeMap(map[string]interface{}{"Cookie": CleanValue([]string{"hunter2"}, 100)}, opts),
		"after a flag": SanitizeSlice([]interface{}{"--token", CleanValue("hunter2", 100)}, opts),
	} {
		if data, _ := json.Marshal(out); strings.Contains(string(data), "hunter2") {
			t.Errorf("%s: %s", name, data)
		}
	}
	if got := SanitizeSlice([]interface{}{"--token", CleanValue([]byte("hunter2"), 100)}, opts)[1]; !reflect.DeepEqual(got, []byte(redactedValue)) {
		t.Errorf("bytes after a flag = %#v, want redacted", got)
	}
}
