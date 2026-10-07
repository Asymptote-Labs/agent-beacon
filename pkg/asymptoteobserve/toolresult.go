package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// IngestedContentAction reports whether an event action names a tool result whose content came
// from outside the agent -- a file it read, an MCP server's reply -- and so is where indirect
// prompt injection arrives.
//
// This and IngestedContentToolName are the one definition of that scope. The threat-rules engine
// reads them to decide which events expose gen_ai.tool.call.result_text, and the hook adapter
// reads them to decide which tool results it records, so a capture path cannot drop a result the
// engine would have matched on, or record one the engine ignores.
func IngestedContentAction(action string) bool {
	switch action {
	case "file.read", "mcp.tool_invoked":
		return true
	}
	return false
}

// toolNameSeparators folds web_fetch, web-fetch and WebFetch to one spelling.
var toolNameSeparators = strings.NewReplacer("_", "", "-", "")

// IngestedContentToolName reports whether a tool's name says it brings outside content into the
// agent's context whatever action it was classified as: an MCP tool (mcp__server__tool,
// MCP:tool), or a web fetch or search. Those are recorded as tool.invoked or tool.completed on
// most capture paths, so the action alone misses them.
func IngestedContentToolName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "mcp__") || strings.HasPrefix(lower, "mcp:") {
		return true
	}
	switch toolNameSeparators.Replace(lower) {
	case "webfetch", "websearch", "fetch", "fetchurl", "readurl":
		return true
	}
	return false
}

// ToolResultPlainText renders a tool result as text: a string as is; for an object or list (a
// hook's tool_response, an MCP content-block list), every string it contains, with maps in sorted
// key order, one per line. Numbers, booleans and nulls are sizes, counts and flags rather than
// content and contribute nothing. Deterministic for the same result on every run.
//
// Content blocks are the one exception. A list element whose "type" is text, image, audio,
// document, resource or resource_link is an MCP or Anthropic content block, and contributes the
// text the model reads rather than its metadata or its encoded bytes; see skipContentBlockField.
// Only list elements are treated this way, because a tool's own response object can carry a
// "type" too: Claude Code's Read result is {"type": "text", "file": {...}}.
//
// It does no redaction or truncation; callers apply the limit that governs where the text goes.
func ToolResultPlainText(result interface{}) string {
	var parts []string
	collectToolResultStrings(result, &parts)
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// contentBlockTypes are the content-block types a tool result list can hold: MCP's text, image,
// audio, resource and resource_link, and the Anthropic image and document blocks Claude Code
// rewrites MCP media into.
var contentBlockTypes = map[string]bool{
	"text": true, "image": true, "audio": true, "document": true, "resource": true, "resource_link": true,
}

// contentBlock returns a list element as a content block, when it is one.
func contentBlock(item interface{}) (map[string]interface{}, bool) {
	block, ok := item.(map[string]interface{})
	if !ok {
		return nil, false
	}
	blockType, _ := block["type"].(string)
	return block, contentBlockTypes[blockType]
}

// isEncodedBytes reports whether a field of a content block, or of a resource block's embedded
// resource, holds encoded bytes rather than text the model reads: an image's or audio clip's
// base64 "data", an Anthropic block's base64 "source", or an embedded resource's "blob".
//
// It is the one definition both readers of content blocks share: ToolResultPlainText leaves these
// fields out of the text, and SummarizeEncodedContent replaces them with a size and digest.
func isEncodedBytes(key string, value interface{}) bool {
	switch key {
	case "data", "blob":
		_, encoded := value.(string)
		return encoded
	case "source":
		source, ok := value.(map[string]interface{})
		return ok && source["type"] == "base64"
	}
	return false
}

// skipContentBlockField reports whether a field of a content block, or of a resource block's
// embedded resource, is metadata or encoded bytes rather than text the model reads.
//
// Encoded bytes are skipped because they are not text, and because a run of them would otherwise
// use up the result_text cap before a text block that follows them. "sha256" is the digest
// SummarizeEncodedContent records in their place.
func skipContentBlockField(key string, value interface{}) bool {
	switch key {
	case "type", "mimeType", "annotations", "_meta", "sha256":
		return true
	}
	return isEncodedBytes(key, value)
}

// SummarizeEncodedContent returns a list of content blocks with the encoded bytes in each block
// replaced by "bytes" (the decoded size) and "sha256" (of the decoded data), keeping every other
// field and the block order. A value that is not valid base64 is not encoded bytes and is kept.
// The input is not modified.
//
// A capture path stores this rather than the bytes because the writer cuts every string at
// DefaultStringLimit, so a stored copy could never be decoded, and each copy costs that much of
// the event's size budget: enough images and the writer drops the whole result, text included.
// The size and digest still identify the media.
func SummarizeEncodedContent(blocks []interface{}) []interface{} {
	out := make([]interface{}, len(blocks))
	for i, item := range blocks {
		out[i] = item
		if block, ok := contentBlock(item); ok {
			out[i], _ = summarizeEncodedFields(block)
		}
	}
	return out
}

// summarizeEncodedFields returns m with its encoded fields summarized, reporting whether any were.
// An embedded resource and an Anthropic base64 source hold their bytes one level down, and are
// summarized the same way. When nothing changes, m itself is returned.
func summarizeEncodedFields(m map[string]interface{}) (map[string]interface{}, bool) {
	out := maps.Clone(m)
	changed := false
	for key, value := range m {
		if nested, ok := value.(map[string]interface{}); ok {
			if key != "resource" && !isEncodedBytes(key, value) {
				continue
			}
			if summary, ok := summarizeEncodedFields(nested); ok {
				out[key], changed = summary, true
			}
			continue
		}
		if !isEncodedBytes(key, value) {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(value.(string))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(decoded)
		delete(out, key)
		out["bytes"], out["sha256"] = len(decoded), hex.EncodeToString(sum[:])
		changed = true
	}
	if !changed {
		return m, false
	}
	return out, true
}

func collectToolResultStrings(v interface{}, out *[]string) {
	switch typed := v.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*out = append(*out, typed)
		}
	case map[string]interface{}:
		collectMapStrings(typed, nil, out)
	case []interface{}:
		for _, item := range typed {
			if block, ok := contentBlock(item); ok {
				collectMapStrings(block, skipContentBlockField, out)
				continue
			}
			collectToolResultStrings(item, out)
		}
	case nil, bool, float64, float32, int, int64, int32, json.Number:
	default:
		// A typed value from an in-process caller (a mapper's struct or []map) rather than the
		// generic shape a log line decodes to. Normalize it through JSON once; the result is a
		// string, map, list or scalar, all handled above without reaching this case again.
		data, err := json.Marshal(typed)
		if err != nil {
			return
		}
		var generic interface{}
		if json.Unmarshal(data, &generic) == nil {
			collectToolResultStrings(generic, out)
		}
	}
}

// collectMapStrings walks m in sorted key order, leaving out the fields skip names. A content
// block's embedded resource is walked with the same skip, since its blob is encoded bytes too.
func collectMapStrings(m map[string]interface{}, skip func(string, interface{}) bool, out *[]string) {
	for _, key := range slices.Sorted(maps.Keys(m)) {
		value := m[key]
		if skip != nil && skip(key, value) {
			continue
		}
		if resource, ok := value.(map[string]interface{}); ok && skip != nil && key == "resource" {
			collectMapStrings(resource, skip, out)
			continue
		}
		collectToolResultStrings(value, out)
	}
}
