package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func readClaudeFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "claude", name))
	if err != nil {
		t.Fatalf("read claude fixture %s: %v", name, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode claude fixture %s: %v", name, err)
	}
	return payload
}

// The result of an MCP tool, as Claude Code 2.1.291 hands it to PostToolUse: a bare list of
// content blocks, with the server's image as an Anthropic block and everything else as text. The
// live event keeps every block in order, records the image's size and digest instead of its bytes,
// and its result text is exactly the text blocks the model read.
func TestClaudeMCPPostToolRecordsCapturedContentBlocks(t *testing.T) {
	payload := readClaudeFixture(t, "post_tool_use_mcp_media.json")
	sent := payload["tool_response"].([]interface{})
	var texts []string
	var png []byte
	for _, item := range sent {
		block := item.(map[string]interface{})
		switch block["type"] {
		case "text":
			texts = append(texts, block["text"].(string))
		case "image":
			var err error
			if png, err = base64.StdEncoding.DecodeString(block["source"].(map[string]interface{})["data"].(string)); err != nil {
				t.Fatal(err)
			}
		}
	}

	_, events := runClaudePostTool(t, payload)
	if len(events) != 1 || events[0]["event"].(map[string]interface{})["action"] != "mcp.tool_invoked" {
		t.Fatalf("events = %#v, want one mcp.tool_invoked", events)
	}
	call := events[0]["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})
	result := call["result"].(map[string]interface{})
	stored := result["content"].([]interface{})
	if len(stored) != len(sent) {
		t.Fatalf("stored %d blocks, Claude sent %d", len(stored), len(sent))
	}
	source := stored[1].(map[string]interface{})["source"].(map[string]interface{})
	digest := sha256.Sum256(png)
	want := map[string]interface{}{"type": "base64", "media_type": "image/png", "bytes": float64(len(png)), "sha256": hex.EncodeToString(digest[:])}
	if !reflect.DeepEqual(source, want) {
		t.Errorf("image source = %#v, want its size and digest in place of its bytes %#v", source, want)
	}
	if text := asymptoteobserve.ToolResultPlainText(result); text != strings.Join(texts, "\n") {
		t.Errorf("result text = %q, want the text blocks Claude sent:\n%q", text, strings.Join(texts, "\n"))
	}
	line, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "iVBORw0KGgo") {
		t.Errorf("the stored event still holds the image's base64: %s", line)
	}
}

// A tool that returned no content blocks reaches the hook as an empty list, and is recorded with
// no result rather than an empty one.
func TestClaudeMCPPostToolWithNoContentRecordsNoResult(t *testing.T) {
	_, events := runClaudePostTool(t, readClaudeFixture(t, "post_tool_use_mcp_empty.json"))
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one", events)
	}
	call := events[0]["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})
	if result, ok := call["result"]; ok {
		t.Errorf("gen_ai.tool.call.result = %#v, want none", result)
	}
}

// MCP's own media shapes, which a runtime that passes blocks through unchanged would send, are
// summarized the same way; anything that is not base64 is left as it is, and the input is not
// modified.
func TestSummarizeEncodedContent(t *testing.T) {
	digest := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	in := []interface{}{
		map[string]interface{}{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("img"))},
		map[string]interface{}{"type": "audio", "mimeType": "audio/wav", "data": base64.StdEncoding.EncodeToString([]byte("audio"))},
		map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"uri": "file:///r.bin", "blob": base64.StdEncoding.EncodeToString([]byte("bin"))}},
		map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"uri": "file:///r.txt", "text": "notes"}},
		map[string]interface{}{"type": "image", "data": "not base64!"},
		map[string]interface{}{"type": "text", "text": "kept"},
		"not a block",
	}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got := summarizeEncodedContent(in)
	want := []interface{}{
		map[string]interface{}{"type": "image", "mimeType": "image/png", "bytes": 3, "sha256": digest("img")},
		map[string]interface{}{"type": "audio", "mimeType": "audio/wav", "bytes": 5, "sha256": digest("audio")},
		map[string]interface{}{"type": "resource", "resource": map[string]interface{}{"uri": "file:///r.bin", "bytes": 3, "sha256": digest("bin")}},
		in[3], in[4], in[5], in[6],
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summarized blocks:\ngot  %#v\nwant %#v", got, want)
	}
	if after, _ := json.Marshal(in); string(after) != string(before) {
		t.Errorf("input was modified:\nbefore %s\nafter  %s", before, after)
	}
}
