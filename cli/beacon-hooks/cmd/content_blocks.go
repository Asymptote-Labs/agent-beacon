package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// summarizeEncodedContent replaces the encoded bytes in a list of content blocks with their
// decoded size and SHA-256, keeping every other field and the block order.
//
// Claude Code delivers an MCP tool's image as an Anthropic image block with its base64 under
// source.data. MCP's own image and audio blocks carry it under data, and an embedded resource
// under blob. The writer cuts every string at 4096 bytes, so a stored copy could never be decoded,
// and each one costs that much of the 64 KiB event budget: enough screenshots and the writer drops
// the whole result, text blocks included. The size and digest still identify the media, which
// Claude Code saves under the session's tool-results directory and names in the next block.
func summarizeEncodedContent(blocks []interface{}) []interface{} {
	out := make([]interface{}, len(blocks))
	for i, item := range blocks {
		out[i] = item
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		switch block["type"] {
		case "image", "audio", "document":
			if source, ok := block["source"].(map[string]interface{}); ok && source["type"] == "base64" {
				if summary, changed := summarizeEncodedField(source, "data"); changed {
					out[i] = withField(block, "source", summary)
				}
			} else if summary, changed := summarizeEncodedField(block, "data"); changed {
				out[i] = summary
			}
		case "resource":
			if resource, ok := block["resource"].(map[string]interface{}); ok {
				if summary, changed := summarizeEncodedField(resource, "blob"); changed {
					out[i] = withField(block, "resource", summary)
				}
			}
		}
	}
	return out
}

// summarizeEncodedField returns a copy of m with the base64 string under key replaced by
// "bytes" and "sha256" of the decoded data. A value that is not a base64 string is not encoded
// bytes, and m is returned unchanged.
func summarizeEncodedField(m map[string]interface{}, key string) (map[string]interface{}, bool) {
	encoded, ok := m[key].(string)
	if !ok {
		return m, false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return m, false
	}
	sum := sha256.Sum256(decoded)
	out := withField(m, "bytes", len(decoded))
	delete(out, key)
	out["sha256"] = hex.EncodeToString(sum[:])
	return out, true
}

// withField returns a shallow copy of m with key set to value.
func withField(m map[string]interface{}, key string, value interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}
