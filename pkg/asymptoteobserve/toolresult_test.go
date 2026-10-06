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
		{"mcp blocks", map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "one"}, map[string]interface{}{"type": "image", "data": "base64-secret", "mimeType": "image/png"}, map[string]interface{}{"type": "text", "text": "two"}}}, "one\ntwo"},
		{"empty MCP blocks", map[string]interface{}{"content": []interface{}{}}, ""},
		{"MCP media only", map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "image", "data": "base64-secret"}}}, ""},
		{"MCP textual resource", map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"text": "embedded instructions", "uri": "notes://item"}}}}, "embedded instructions\nnotes://item\nresource"},
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
