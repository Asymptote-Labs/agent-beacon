package asymptoteobserve

import "testing"

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
