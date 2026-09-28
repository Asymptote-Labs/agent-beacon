package mcpconnect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The harness configs Beacon edits are JSON files the user also edits by hand, and two of them
// (OpenCode's opencode.jsonc, VS Code's mcp.json) are JSONC: comments and trailing commas are
// legal. Decoding one into a map and encoding it back would drop every comment, reorder keys and
// reindent the file, so Beacon never does that. This file is a small parser that keeps the byte
// span of every value, so an edit can splice text in or out at an exact offset and leave every
// other byte where it was. verifyJSONEdit then parses the result and checks that the edit changed
// only the one entry Beacon owns.

type jsonKind byte

const (
	jsonObject jsonKind = 'o'
	jsonArray  jsonKind = 'a'
	jsonScalar jsonKind = 's'
)

type jsonNode struct {
	kind  jsonKind
	start int // offset of the first byte of the value
	end   int // offset one past the last byte of the value
	depth int // nesting depth; the document's root value is 0

	members []jsonMember // objects
	elems   []*jsonNode  // arrays
}

type jsonMember struct {
	key      string
	keyStart int
	value    *jsonNode
}

func (n *jsonNode) member(key string) (int, *jsonNode) {
	if n == nil || n.kind != jsonObject {
		return -1, nil
	}
	for i, m := range n.members {
		if m.key == key {
			return i, m.value
		}
	}
	return -1, nil
}

func (n *jsonNode) memberCount(key string) int {
	count := 0
	if n == nil {
		return 0
	}
	for _, m := range n.members {
		if m.key == key {
			count++
		}
	}
	return count
}

type jsonParser struct {
	text string
	pos  int
}

// parseJSONC parses a JSON or JSONC document. An empty or whitespace-only document has no root.
func parseJSONC(text string) (*jsonNode, error) {
	p := &jsonParser{text: text}
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.pos == len(text) {
		return nil, nil
	}
	root, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.pos != len(text) {
		return nil, p.errorf("unexpected content after the document")
	}
	return root, nil
}

func (p *jsonParser) errorf(format string, args ...any) error {
	line := strings.Count(p.text[:min(p.pos, len(p.text))], "\n") + 1
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

// skip moves past whitespace and comments.
func (p *jsonParser) skip() error {
	for p.pos < len(p.text) {
		switch c := p.text[p.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
		case c == '/' && p.pos+1 < len(p.text) && p.text[p.pos+1] == '/':
			end := strings.IndexByte(p.text[p.pos:], '\n')
			if end < 0 {
				p.pos = len(p.text)
			} else {
				p.pos += end
			}
		case c == '/' && p.pos+1 < len(p.text) && p.text[p.pos+1] == '*':
			end := strings.Index(p.text[p.pos+2:], "*/")
			if end < 0 {
				return p.errorf("unterminated comment")
			}
			p.pos += end + 4
		default:
			return nil
		}
	}
	return nil
}

func (p *jsonParser) value(depth int) (*jsonNode, error) {
	if p.pos >= len(p.text) {
		return nil, p.errorf("unexpected end of document")
	}
	if depth > 200 {
		return nil, p.errorf("document is nested too deeply")
	}
	switch p.text[p.pos] {
	case '{':
		return p.object(depth)
	case '[':
		return p.array(depth)
	case '"':
		start := p.pos
		if _, err := p.str(); err != nil {
			return nil, err
		}
		return &jsonNode{kind: jsonScalar, start: start, end: p.pos, depth: depth}, nil
	default:
		start := p.pos
		for p.pos < len(p.text) && !strings.ContainsRune(",}] \t\r\n/", rune(p.text[p.pos])) {
			p.pos++
		}
		if p.pos == start {
			return nil, p.errorf("unexpected %q", p.text[p.pos])
		}
		literal := p.text[start:p.pos]
		if !json.Valid([]byte(literal)) {
			return nil, p.errorf("invalid value %q", literal)
		}
		return &jsonNode{kind: jsonScalar, start: start, end: p.pos, depth: depth}, nil
	}
}

// str scans a string starting at the opening quote and returns its decoded value.
func (p *jsonParser) str() (string, error) {
	start := p.pos
	p.pos++
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case '\\':
			p.pos += 2
		case '"':
			p.pos++
			var decoded string
			if err := json.Unmarshal([]byte(p.text[start:p.pos]), &decoded); err != nil {
				return "", p.errorf("invalid string: %v", err)
			}
			return decoded, nil
		case '\n':
			return "", p.errorf("unterminated string")
		default:
			p.pos++
		}
	}
	return "", p.errorf("unterminated string")
}

func (p *jsonParser) object(depth int) (*jsonNode, error) {
	node := &jsonNode{kind: jsonObject, start: p.pos, depth: depth}
	p.pos++
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.text) {
			return nil, p.errorf("unterminated object")
		}
		if p.text[p.pos] == '}' {
			p.pos++
			node.end = p.pos
			return node, nil
		}
		if len(node.members) > 0 {
			if p.text[p.pos] != ',' {
				return nil, p.errorf("expected , or } in object")
			}
			p.pos++
			if err := p.skip(); err != nil {
				return nil, err
			}
			if p.pos < len(p.text) && p.text[p.pos] == '}' { // trailing comma
				p.pos++
				node.end = p.pos
				return node, nil
			}
		}
		if p.pos >= len(p.text) || p.text[p.pos] != '"' {
			return nil, p.errorf("expected a quoted key")
		}
		keyStart := p.pos
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.text) || p.text[p.pos] != ':' {
			return nil, p.errorf("expected : after key %q", key)
		}
		p.pos++
		if err := p.skip(); err != nil {
			return nil, err
		}
		value, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		node.members = append(node.members, jsonMember{key: key, keyStart: keyStart, value: value})
	}
}

func (p *jsonParser) array(depth int) (*jsonNode, error) {
	node := &jsonNode{kind: jsonArray, start: p.pos, depth: depth}
	p.pos++
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.text) {
			return nil, p.errorf("unterminated array")
		}
		if p.text[p.pos] == ']' {
			p.pos++
			node.end = p.pos
			return node, nil
		}
		if len(node.elems) > 0 {
			if p.text[p.pos] != ',' {
				return nil, p.errorf("expected , or ] in array")
			}
			p.pos++
			if err := p.skip(); err != nil {
				return nil, err
			}
			if p.pos < len(p.text) && p.text[p.pos] == ']' {
				p.pos++
				node.end = p.pos
				return node, nil
			}
		}
		value, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		node.elems = append(node.elems, value)
	}
}

// decodeJSONC parses a JSON or JSONC document into plain Go values, the way the harness reads it.
// Numbers stay json.Number so a comparison never loses precision. An empty document is an empty
// object, which is how every harness Beacon edits treats a blank config file.
func decodeJSONC(text string) (map[string]any, error) {
	root, err := parseJSONC(text)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{}, nil
	}
	if root.kind != jsonObject {
		return nil, fmt.Errorf("the document is not a JSON object")
	}
	var buf bytes.Buffer
	writeStandardJSON(&buf, text, root)
	decoder := json.NewDecoder(&buf)
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// writeStandardJSON re-emits a parsed node as strict JSON: the same values, without comments or
// trailing commas.
func writeStandardJSON(buf *bytes.Buffer, text string, n *jsonNode) {
	switch n.kind {
	case jsonObject:
		buf.WriteByte('{')
		for i, m := range n.members {
			if i > 0 {
				buf.WriteByte(',')
			}
			key, _ := json.Marshal(m.key)
			buf.Write(key)
			buf.WriteByte(':')
			writeStandardJSON(buf, text, m.value)
		}
		buf.WriteByte('}')
	case jsonArray:
		buf.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeStandardJSON(buf, text, e)
		}
		buf.WriteByte(']')
	default:
		buf.WriteString(text[n.start:n.end])
	}
}
