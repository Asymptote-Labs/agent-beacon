package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestIngestedContentScope(t *testing.T) {
	for action, want := range map[string]bool{
		"file.read": true, "mcp.tool_invoked": true,
		"file.modified": false, "command.executed": false, "tool.invoked": false, "tool.completed": false, "": false,
	} {
		if got := IngestedContentAction(action); got != want {
			t.Errorf("IngestedContentAction(%q) = %v, want %v", action, got, want)
		}
	}
	for name, want := range map[string]bool{
		"WebFetch": true, "web_fetch": true, "web-fetch": true, "WebSearch": true, "web_search": true,
		"fetch": true, "fetch_url": true, "read_url": true, "mcp__tickets__get_issue": true, "MCP:get_organizations": true,
		"Read": false, "Bash": false, "Write": false, "TodoWrite": false, "Task": false, "": false, "prefetch": false,
	} {
		if got := IngestedContentToolName(name); got != want {
			t.Errorf("IngestedContentToolName(%q) = %v, want %v", name, got, want)
		}
	}
	for target, want := range map[string]bool{
		"https://docs.example/guide": true, "HTTP://docs.example/guide": true, " https://x.test ": true,
		"https://": false, "/repo/NOTES.md": false, "notes.md": false, "file:///repo/NOTES.md": false,
		"skill://review": false, "mcp://notes/1": false, "ftp://files.example/a": false, "": false,
	} {
		if got := IngestedContentTarget(target); got != want {
			t.Errorf("IngestedContentTarget(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestToolResultPlainText(t *testing.T) {
	cases := []struct {
		name   string
		result interface{}
		want   string
	}{
		{"string", "  hello  ", "hello"},
		{"nil", nil, ""},
		{"claude read", map[string]interface{}{"type": "text", "file": map[string]interface{}{"filePath": "/a", "content": "body", "numLines": 1.0}}, "body\n/a\ntext"},
		{"mcp blocks", blocks(map[string]interface{}{"type": "text", "text": "one"}, map[string]interface{}{"type": "image", "data": "aW1n", "mimeType": "image/png"}, map[string]interface{}{"type": "text", "text": "two"}), "one\ntwo"},
		{"empty MCP blocks", blocks(), ""},
		{"MCP media only", blocks(map[string]interface{}{"type": "audio", "data": "YXVk", "mimeType": "audio/wav"}), ""},
		// Claude Code rewrites an MCP image into an Anthropic block before its hook sees it.
		{"anthropic image source", blocks(map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "aW1n"}}, map[string]interface{}{"type": "text", "text": "after"}), "after"},
		{"MCP textual resource", blocks(map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"text": "embedded instructions", "uri": "notes://item", "mimeType": "text/plain"}}), "embedded instructions\nnotes://item"},
		{"MCP binary resource before text", blocks(map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"blob": "Ymlu", "uri": "file:///raw.bin", "mimeType": "application/octet-stream"}}, map[string]interface{}{"type": "text", "text": "after"}), "file:///raw.bin\nafter"},
		{"MCP resource link", blocks(map[string]interface{}{"type": "resource_link", "uri": "file:///r.pdf", "name": "report", "mimeType": "application/pdf"}), "report\nfile:///r.pdf"},
		// A capture path that replaced the bytes with their size and digest contributes neither.
		{"summarized media", blocks(map[string]interface{}{"type": "image", "mimeType": "image/png", "bytes": 165.0, "sha256": "ab12"}, map[string]interface{}{"type": "text", "text": "after"}), "after"},
		// Only metadata and encoded strings are skipped: a block's other text is kept.
		{"text block keeps other fields", blocks(map[string]interface{}{"type": "text", "text": "body", "annotations": map[string]interface{}{"audience": []interface{}{"user"}}, "citations": []interface{}{map[string]interface{}{"cited_text": "cited"}}}), "cited\nbody"},
		{"media data that is not a string", blocks(map[string]interface{}{"type": "image", "data": map[string]interface{}{"caption": "kept"}}), "kept"},
		// Outside a list, a typed object is walked like any other.
		{"typed object outside a list", map[string]interface{}{"type": "image", "data": "not a block"}, "not a block\nimage"},
		{"typed response object", map[string]interface{}{"type": "success", "result": "response body"}, "response body\nsuccess"},
		{"plain list", []interface{}{"first", "second"}, "first\nsecond"},
		{"scalars only", map[string]interface{}{"code": 200.0, "ok": true}, ""},
		{"typed", []map[string]string{{"text": "typed"}}, "typed"},
	}
	for _, tc := range cases {
		if got := ToolResultPlainText(tc.result); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// blocks is a hook's tool_response for an MCP tool: the content blocks under "content".
func blocks(items ...interface{}) map[string]interface{} {
	return map[string]interface{}{"content": items}
}

// Every shape isEncodedBytes names is summarized: MCP data, an embedded resource's blob, and the
// Anthropic base64 source Claude Code sends. Anything that is not base64 is kept, other fields and
// the block order are kept, and the input is not modified.
func TestSummarizeEncodedContent(t *testing.T) {
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	digest := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	in := []interface{}{
		map[string]interface{}{"type": "image", "mimeType": "image/png", "data": encode("img")},
		map[string]interface{}{"type": "audio", "mimeType": "audio/wav", "data": encode("audio")},
		map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": encode("png")}},
		map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"uri": "file:///r.bin", "blob": encode("bin")}},
		map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"uri": "file:///r.txt", "text": "notes"}},
		map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "url", "url": "https://example.test/a.png"}},
		map[string]interface{}{"type": "image", "data": "not base64!"},
		map[string]interface{}{"type": "text", "text": "kept"},
		map[string]interface{}{"data": encode("not a block")},
		"not a map",
	}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got := SummarizeEncodedContent(in)
	want := []interface{}{
		map[string]interface{}{"type": "image", "mimeType": "image/png", "bytes": 3, "sha256": digest("img")},
		map[string]interface{}{"type": "audio", "mimeType": "audio/wav", "bytes": 5, "sha256": digest("audio")},
		map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "bytes": 3, "sha256": digest("png")}},
		map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"uri": "file:///r.bin", "bytes": 3, "sha256": digest("bin")}},
		in[4], in[5], in[6], in[7], in[8], in[9],
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summarized blocks:\ngot  %#v\nwant %#v", got, want)
	}
	if after, _ := json.Marshal(in); string(after) != string(before) {
		t.Errorf("input was modified:\nbefore %s\nafter  %s", before, after)
	}
	// What the summary leaves behind contributes nothing to the rule text but the text block.
	if text := ToolResultPlainText(map[string]interface{}{"content": got[:4]}); text != "file:///r.bin" {
		t.Errorf("summarized media contributes %q to the rule text, want only the resource URI", text)
	}
}
