package mcpconnect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ordered is a JSON object whose keys are written in the order given, so the entry Beacon adds
// reads the way the harness's own `mcp add` writes it.
type ordered []field

type field struct {
	Key   string
	Value any
}

// rawJSON is a value inserted exactly as written, such as an entry's original text.
type rawJSON string

// jsonStyle is the formatting an edit copies from the document it goes into.
type jsonStyle struct {
	unit string // one level of indentation
	eol  string
}

func detectJSONStyle(text string) jsonStyle {
	style := jsonStyle{unit: "  ", eol: "\n"}
	if strings.Contains(text, "\r\n") {
		style.eol = "\r\n"
	}
	// The shallowest indented key is a member of the root object, so its indentation is one
	// level. Deeper keys are multiples of it.
	best := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, `"`) {
			continue
		}
		lead := line[:len(line)-len(trimmed)]
		if lead == "" {
			continue
		}
		if best == "" || len(lead) < len(best) {
			best = lead
		}
	}
	if best != "" {
		style.unit = best
	}
	return style
}

func (s jsonStyle) indent(depth int) string {
	return strings.Repeat(s.unit, depth)
}

func jsonString(value string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimRight(buf.String(), "\n")
}

// renderJSON writes value as it would appear at depth: multi-line with the document's
// indentation, or on one line when the surrounding object is written on one line.
func renderJSON(value any, style jsonStyle, depth int, compact bool, sep string) string {
	switch v := value.(type) {
	case rawJSON:
		return string(v)
	case ordered:
		if len(v) == 0 {
			return "{}"
		}
		parts := make([]string, len(v))
		for i, f := range v {
			parts[i] = jsonString(f.Key) + sep + renderJSON(f.Value, style, depth+1, compact, sep)
		}
		if compact {
			return "{" + strings.Join(parts, ",") + "}"
		}
		inner := style.eol + style.indent(depth+1)
		return "{" + inner + strings.Join(parts, ","+inner) + style.eol + style.indent(depth) + "}"
	case []any:
		if len(v) == 0 {
			return "[]"
		}
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = renderJSON(e, style, depth+1, compact, sep)
		}
		if compact {
			return "[" + strings.Join(parts, ",") + "]"
		}
		inner := style.eol + style.indent(depth+1)
		return "[" + inner + strings.Join(parts, ","+inner) + style.eol + style.indent(depth) + "]"
	case string:
		return jsonString(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		encoded, _ := json.Marshal(v)
		return string(encoded)
	}
}

// renderJSONDocument writes a new document holding only value.
func renderJSONDocument(value ordered, style jsonStyle) string {
	return renderJSON(value, style, 0, false, ": ") + style.eol
}

// nest wraps value in one object per path segment: nest([a b], v) is {"a": {"b": v}}.
func nest(path []string, value any) any {
	for i := len(path) - 1; i >= 0; i-- {
		value = ordered{{Key: path[i], Value: value}}
	}
	return value
}

// span is one member of an object or element of an array: [start, end) runs from the first byte
// of the key (or element) to the last byte of the value.
type span struct{ start, end int }

func memberSpans(n *jsonNode) []span {
	out := make([]span, len(n.members))
	for i, m := range n.members {
		out[i] = span{m.keyStart, m.value.end}
	}
	return out
}

func elemSpans(n *jsonNode) []span {
	out := make([]span, len(n.elems))
	for i, e := range n.elems {
		out[i] = span{e.start, e.end}
	}
	return out
}

// isCompact reports whether a non-empty container is written on a single line.
func isCompact(text string, n *jsonNode, items []span) bool {
	return len(items) > 0 && !strings.Contains(text[n.start:n.end], "\n")
}

// keySeparator is the text a document puts between a key and its value (": " or ":").
func keySeparator(text string, n *jsonNode) string {
	for _, m := range n.members {
		between := text[m.keyStart+len(jsonString(m.key)) : m.value.start]
		if strings.TrimSpace(between) == ":" {
			if strings.HasPrefix(strings.TrimLeft(between, " \t"), ":") && strings.HasSuffix(between, ":") {
				return ":"
			}
			return ": "
		}
	}
	return ": "
}

// itemSeparator is the text a single-line container puts between items ("," or ", ").
func itemSeparator(text string, items []span) string {
	if len(items) < 2 {
		return ","
	}
	between := text[items[0].end:items[1].start]
	if strings.TrimSpace(between) == "," && strings.HasSuffix(between, " ") {
		return ", "
	}
	return ","
}

// insertItem appends rendered as the last item of container n.
//
// The new text goes directly after the last existing value, so removeItem, which deletes from the
// end of the previous value to the end of the removed one, takes out exactly what this put in.
// A trailing comma, a comment after the last item, and everything else in the file stay put.
func insertItem(text string, n *jsonNode, items []span, rendered string, style jsonStyle) string {
	if len(items) > 0 {
		at := items[len(items)-1].end
		if isCompact(text, n, items) {
			return text[:at] + itemSeparator(text, items) + rendered + text[at:]
		}
		return text[:at] + "," + style.eol + style.indent(n.depth+1) + rendered + text[at:]
	}
	open := n.start + 1
	inner := text[open : n.end-1]
	if strings.TrimSpace(inner) == "" {
		return text[:open] + style.eol + style.indent(n.depth+1) + rendered + style.eol + style.indent(n.depth) + text[n.end-1:]
	}
	// The container holds only comments. Keep them after the new item.
	return text[:open] + style.eol + style.indent(n.depth+1) + rendered + text[open:]
}

// removeItem deletes item i of container n, with the separator that belongs to it.
func removeItem(text string, n *jsonNode, items []span, i int) string {
	var from, to int
	switch {
	case i > 0:
		from, to = items[i-1].end, items[i].end
	case len(items) > 1:
		from, to = items[0].start, items[1].start
	default:
		from, to = n.start+1, items[0].end
		// A trailing comma after the only item has to go with it.
		p := &jsonParser{text: text, pos: to}
		if p.skip() == nil && p.pos < n.end-1 && text[p.pos] == ',' {
			to = p.pos + 1
		}
		// When nothing but whitespace is left, close the container up to {} or [], which is
		// what insertItem expanded it from.
		if strings.TrimSpace(text[to:n.end-1]) == "" {
			to = n.end - 1
		}
	}
	return text[:from] + text[to:]
}

// jsonPathError reports a config shape Beacon will not edit around.
type jsonPathError struct{ msg string }

func (e *jsonPathError) Error() string { return e.msg }

// lookupObject walks path from the root, returning the object at the end of it, how far the walk
// got, and the containing object at each step. A path segment that is not an object, or a key that
// appears twice (which harnesses resolve differently), is an error.
func lookupObject(root *jsonNode, path []string) (chain []*jsonNode, err error) {
	node := root
	chain = []*jsonNode{root}
	for _, seg := range path {
		if node.memberCount(seg) > 1 {
			return chain, &jsonPathError{fmt.Sprintf("%q appears more than once", seg)}
		}
		_, child := node.member(seg)
		if child == nil {
			return chain, nil
		}
		if child.kind != jsonObject {
			return chain, &jsonPathError{fmt.Sprintf("%q is not a JSON object", seg)}
		}
		node = child
		chain = append(chain, node)
	}
	return chain, nil
}

// insertJSONMember returns text with path.key set to value. key must not already exist. Objects
// missing from path are created; created reports how many were, so a later removal can take them
// out again once they are empty.
func insertJSONMember(text string, path []string, key string, value any) (updated string, created int, err error) {
	root, err := parseJSONC(text)
	if err != nil {
		return "", 0, err
	}
	style := detectJSONStyle(text)
	if root == nil {
		doc := nest(path, ordered{{Key: key, Value: value}}).(ordered)
		// Keep a blank file's surrounding whitespace out of it; a blank file becomes the document.
		return renderJSONDocument(doc, style), len(path), nil
	}
	if root.kind != jsonObject {
		return "", 0, &jsonPathError{"the document is not a JSON object"}
	}
	chain, err := lookupObject(root, path)
	if err != nil {
		return "", 0, err
	}
	parent := chain[len(chain)-1]
	missing := path[len(chain)-1:]
	insertKey, insertValue := key, value
	if len(missing) > 0 {
		insertKey = missing[0]
		insertValue = nest(missing[1:], ordered{{Key: key, Value: value}})
	} else if parent.memberCount(key) > 0 {
		return "", 0, &jsonPathError{fmt.Sprintf("%q already exists", key)}
	}
	items := memberSpans(parent)
	compact := isCompact(text, parent, items)
	sep := keySeparator(text, parent)
	rendered := jsonString(insertKey) + sep + renderJSON(insertValue, style, parent.depth+1, compact, sep)
	return insertItem(text, parent, items, rendered, style), len(missing), nil
}

// rawMember returns the original text of path.key's value, as JSON with comments stripped.
func rawMember(text string, path []string, key string) (string, bool) {
	root, err := parseJSONC(text)
	if err != nil || root == nil || root.kind != jsonObject {
		return "", false
	}
	chain, err := lookupObject(root, path)
	if err != nil || len(chain) != len(path)+1 {
		return "", false
	}
	_, value := chain[len(chain)-1].member(key)
	if value == nil {
		return "", false
	}
	var buf bytes.Buffer
	writeStandardJSON(&buf, text, value)
	return buf.String(), true
}

// removeJSONMember returns text without path.key. prune is how many of the innermost path objects
// Beacon created; each of those is removed too when the removal leaves it empty.
func removeJSONMember(text string, path []string, key string, prune int) (string, error) {
	root, err := parseJSONC(text)
	if err != nil {
		return "", err
	}
	if root == nil || root.kind != jsonObject {
		return text, nil
	}
	chain, err := lookupObject(root, path)
	if err != nil {
		return "", err
	}
	if len(chain) != len(path)+1 {
		return text, nil
	}
	target := chain[len(chain)-1]
	if target.memberCount(key) > 1 {
		return "", &jsonPathError{fmt.Sprintf("%q appears more than once", key)}
	}
	index, _ := target.member(key)
	if index < 0 {
		return text, nil
	}
	// Climb while the object being emptied is one Beacon created and holds nothing else.
	level := len(path)
	for level > 0 && len(path)-level < prune && len(target.members) == 1 {
		level--
		target = chain[level]
		index, _ = target.member(path[level])
	}
	return removeItem(text, target, memberSpans(target), index), nil
}

// appendJSONElement returns text with value appended to the array at path.key, creating the
// array when it is missing. created reports whether it was.
func appendJSONElement(text string, path []string, key string, value any) (string, bool, error) {
	root, err := parseJSONC(text)
	if err != nil {
		return "", false, err
	}
	if root == nil {
		updated, _, err := insertJSONMember(text, path, key, []any{value})
		return updated, true, err
	}
	chain, err := lookupObject(root, path)
	if err != nil {
		return "", false, err
	}
	if len(chain) != len(path)+1 || chain[len(chain)-1].memberCount(key) == 0 {
		updated, _, err := insertJSONMember(text, path, key, []any{value})
		return updated, true, err
	}
	parent := chain[len(chain)-1]
	if parent.memberCount(key) > 1 {
		return "", false, &jsonPathError{fmt.Sprintf("%q appears more than once", key)}
	}
	_, arr := parent.member(key)
	if arr.kind != jsonArray {
		return "", false, &jsonPathError{fmt.Sprintf("%q is not a JSON array", key)}
	}
	style := detectJSONStyle(text)
	items := elemSpans(arr)
	compact := isCompact(text, arr, items)
	rendered := renderJSON(value, style, arr.depth+1, compact, keySeparator(text, parent))
	return insertItem(text, arr, items, rendered, style), false, nil
}

// removeJSONElements returns text without the elements of the array at path.key that match. With
// prune set, an array left empty is removed as well.
func removeJSONElements(text string, path []string, key string, match func(any) bool, prune bool) (string, error) {
	for {
		root, err := parseJSONC(text)
		if err != nil {
			return "", err
		}
		if root == nil {
			return text, nil
		}
		chain, err := lookupObject(root, path)
		if err != nil {
			return "", err
		}
		if len(chain) != len(path)+1 {
			return text, nil
		}
		parent := chain[len(chain)-1]
		index, arr := parent.member(key)
		if arr == nil || arr.kind != jsonArray {
			return text, nil
		}
		found := -1
		for i, e := range arr.elems {
			var buf bytes.Buffer
			writeStandardJSON(&buf, text, e)
			var decoded any
			decoder := json.NewDecoder(&buf)
			decoder.UseNumber()
			if decoder.Decode(&decoded) == nil && match(decoded) {
				found = i
				break
			}
		}
		if found < 0 {
			return text, nil
		}
		if prune && len(arr.elems) == 1 {
			return removeItem(text, parent, memberSpans(parent), index), nil
		}
		text = removeItem(text, arr, elemSpans(arr), found)
	}
}
