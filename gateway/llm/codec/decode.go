package codec

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"slices"
	"strings"
	"unsafe"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Decoding streams each body once through a jsontext.Decoder: large values (message content,
// tool results) decode straight into llm parts instead of being captured as raw JSON and parsed
// again at every level. Content unions implement json.UnmarshalerFrom so struct decoding hands
// them the decoder.
//
// Every decode in this package goes through unmarshal, whose decoder parses the input slice in
// place, so a jsontext.Value read from it stays valid until unmarshal returns, not only until
// the next read; anthropicBlock relies on that to defer content it cannot classify yet.

// decodeOptions are encoding/json's (v1) semantics: member names match struct fields
// case-insensitively, duplicate names are allowed (the last wins) and invalid UTF-8 is replaced
// rather than rejected. Bodies come from clients, and a lenient decoder sees what a lenient
// provider would. The one v1 behavior left out is legacy error reporting, which validates a
// value in full again before every nested decode (each byte is scanned once per nesting level)
// and keeps decoding past a type error; any error fails the whole decode here, so dropping it
// changes nothing but cost.
var decodeOptions = json.JoinOptions(jsonv1.DefaultOptionsV1(), jsonv1.ReportErrorsWithLegacySemantics(false))

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v, decodeOptions) }

// decodeFunc adapts a hand-written decoder to json.UnmarshalerFrom, so unmarshal runs it on a
// pooled decoder, in place, and checks that nothing follows the value.
type decodeFunc func(dec *jsontext.Decoder) error

func (f decodeFunc) UnmarshalJSONFrom(dec *jsontext.Decoder) error { return f(dec) }

// decodeRequestObject streams a request body's top-level object. member decodes the members a
// codec maps and reports whether it consumed the value; every other member is skipped, and those
// outside known are returned, sorted and deduplicated, so new request fields surface in a stable
// order. Member names match exactly, as providers match them.
func decodeRequestObject(body []byte, known map[string]bool, member func(dec *jsontext.Decoder, name []byte) (bool, error)) ([]string, error) {
	var unknown []string
	f := decodeFunc(func(dec *jsontext.Decoder) error {
		return readObject(dec, func(name []byte) error {
			handled, err := member(dec, name)
			if handled || err != nil {
				return err
			}
			if !known[string(name)] {
				unknown = append(unknown, string(name))
			}
			return dec.SkipValue()
		})
	})
	if err := unmarshal(body, &f); err != nil {
		return nil, err
	}
	slices.Sort(unknown)
	return slices.Compact(unknown), nil
}

// readObject reads an object, handing each member name to member, which must consume exactly the
// member's value. The name is valid only until member reads from dec.
func readObject(dec *jsontext.Decoder, member func(name []byte) error) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return eofUnexpected(err)
	}
	if tok.Kind() != '{' {
		return fmt.Errorf("want a JSON object, got %v", tok.Kind())
	}
	for dec.PeekKind() != '}' {
		v, err := dec.ReadValue()
		if err != nil {
			return eofUnexpected(err)
		}
		name := []byte(v[1 : len(v)-1])
		if bytes.IndexByte(name, '\\') >= 0 {
			if name, err = jsontext.AppendUnquote(nil, v); err != nil {
				return err
			}
		}
		if err := member(name); err != nil {
			return err
		}
	}
	_, err = dec.ReadToken()
	return eofUnexpected(err)
}

// readArray reads an array, calling elem to decode each element from dec; null reads as empty.
// Decoding elements one by one keeps large per-element structs out of a growing slice.
func readArray(dec *jsontext.Decoder, elem func() error) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return eofUnexpected(err)
	}
	switch tok.Kind() {
	case 'n':
		return nil
	case '[':
	default:
		return fmt.Errorf("want a JSON array, got %v", tok.Kind())
	}
	for dec.PeekKind() != ']' {
		if err := elem(); err != nil {
			return err
		}
	}
	_, err = dec.ReadToken()
	return eofUnexpected(err)
}

// eofUnexpected reports a body that ends inside a value as truncated, not as a clean end.
func eofUnexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// foldName lowers ASCII capitals, so hand-decoded objects match member names case-insensitively
// as struct decoding under decodeOptions does.
func foldName(name []byte) []byte {
	for _, c := range name {
		if 'A' <= c && c <= 'Z' {
			return bytes.ToLower(name)
		}
	}
	return name
}

// readString reads a string; null reads as "".
func readString(dec *jsontext.Decoder) (string, error) {
	v, err := dec.ReadValue()
	if err != nil {
		return "", eofUnexpected(err)
	}
	switch v.Kind() {
	case '"':
		return unquote(v), nil
	case 'n':
		return "", nil
	}
	return "", fmt.Errorf("want a JSON string, got %v", v.Kind())
}

// unquote decodes a validated JSON string with one allocation; jsontext.Token.String takes two
// for a string with escapes, and message text is most of a body. The buffer is never written
// again, so it can back the string. AppendUnquote fails only on invalid UTF-8, which it
// replaces, as Token.String does.
func unquote(v jsontext.Value) string {
	if len(v) == len(`""`) {
		return ""
	}
	b, _ := jsontext.AppendUnquote(nil, v)
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// readCompact reads a value as compact JSON text, so equal arguments and schemas compare equal
// whatever the client's formatting; null reads as "".
func readCompact(dec *jsontext.Decoder) (string, error) {
	v, err := dec.ReadValue()
	if err != nil {
		return "", eofUnexpected(err)
	}
	if v.Kind() == 'n' {
		return "", nil
	}
	return compactString(v), nil
}

// compactString drops the insignificant whitespace of a valid JSON value, copying it once.
func compactString(v []byte) string {
	var b strings.Builder
	b.Grow(len(v))
	inString := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if inString {
			b.WriteByte(c)
			switch c {
			case '\\':
				i++
				b.WriteByte(v[i])
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '"':
			inString = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// compactText is a JSON value decoded to its compact text.
type compactText string

func (c *compactText) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	s, err := readCompact(dec)
	*c = compactText(s)
	return err
}

// params is the text as tool parameters, nil when the value was null or absent.
func (c compactText) params() []byte {
	if c == "" {
		return nil
	}
	return []byte(c)
}

// jsonText is a field protocols send either as a string (taken as is: free-form tool input, JSON
// arguments already encoded) or as a JSON value (compacted). set reports that the field was
// present and not null.
type jsonText struct {
	text string
	set  bool
}

func (t *jsonText) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var err error
	switch dec.PeekKind() {
	case 'n':
		_, err = dec.ReadToken()
		*t = jsonText{}
	case '"':
		t.text, err = readString(dec)
		t.set = true
	default:
		t.text, err = readCompact(dec)
		t.set = true
	}
	return err
}

// formatFields decodes the response-shaping fields of a request. They are listed in the order
// of keys whatever order the body has them in; a field sent twice keeps its last value.
type formatFields struct {
	keys   []string
	values []string
}

func newFormats(keys ...string) *formatFields {
	return &formatFields{keys: keys, values: make([]string, len(keys))}
}

// read decodes member name if it is a format field.
func (f *formatFields) read(dec *jsontext.Decoder, name []byte) (bool, error) {
	for i, k := range f.keys {
		if k == string(name) {
			var err error
			f.values[i], err = readCompact(dec)
			return true, err
		}
	}
	return false, nil
}

func (f *formatFields) list() []string {
	var out []string
	for _, v := range f.values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// appendText appends a text part, skipping empty text: protocols send "" for absent content
// (an assistant turn that only calls tools), which is not a part.
func appendText(parts []llm.Part, kind llm.PartKind, s string) []llm.Part {
	if s == "" {
		return parts
	}
	return append(parts, llm.Part{Kind: kind, Text: s})
}

func mediaPart(m *llm.Media) llm.Part { return llm.Part{Kind: llm.PartMedia, Media: m} }

func callPart(id, name, args string) llm.Part {
	return llm.Part{Kind: llm.PartToolCall, Call: &llm.ToolCall{ID: id, Name: name, Arguments: args}}
}

func resultPart(callID string, parts []llm.Part, isError bool) llm.Part {
	return llm.Part{Kind: llm.PartToolResult, Result: &llm.ToolResult{CallID: callID, Parts: parts, IsError: isError}}
}

// urlMedia is media referenced by a URL, unpacking a data: URL into its MIME type and inline
// payload so inline media looks the same whichever protocol carried it.
func urlMedia(typ, url string) *llm.Media {
	m := &llm.Media{Type: typ}
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		meta, data, _ := strings.Cut(rest, ",")
		m.MIME, _, _ = strings.Cut(meta, ";")
		m.Data = data
		return m
	}
	m.URL = url
	return m
}

// inlineMedia is media sent inline, as a data: URL or a bare base64 payload.
func inlineMedia(typ, data string) *llm.Media {
	if strings.HasPrefix(data, "data:") {
		return urlMedia(typ, data)
	}
	return &llm.Media{Type: typ, Data: data}
}

// fileMedia is media referenced by a provider file ID. The ID stands in for the URL because it
// is how the media is referenced; the model sees the uploaded file's content.
func fileMedia(typ, fileID string) *llm.Media {
	return &llm.Media{Type: typ, URL: fileID}
}

// apiError is the error object providers put in an error reply or stream event.
type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *apiError) err() error {
	if e.Type == "" {
		return fmt.Errorf("provider error: %s", e.Message)
	}
	return fmt.Errorf("provider error: %s: %s", e.Type, e.Message)
}

// set returns a membership set of names.
func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
