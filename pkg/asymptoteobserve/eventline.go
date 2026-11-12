package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// eventLineMessageLimit is what an oversized event's message is cut to.
const eventLineMessageLimit = 1024

// FitEventLine returns the line a runtime-log writer appends for one event: within maxBytes, and with
// event.id stamped when the event has none. Every writer -- the hook adapter, the CLI and the
// collector exporter -- encodes its own event type and hands the result here, so what an oversized
// event loses, and in what order, is decided once.
//
// line is the writer's encoding of the event, carrying no event.id unless the event already has one.
// stamp re-encodes the writer's event with event.id set; it is nil when the event already has an id.
// A line that fits is kept as the writer encoded it, so stamp is only called for one. An oversized
// line is decoded and cut down, each step on top of the last, until it fits:
//
//  1. raw goes, the message is cut to 1 KiB, and field_truncated is set;
//  2. retained content goes -- diffs, command output, prompt text, model input and output, tool
//     arguments and results -- and the content marker says it was held back;
//  3. only the blocks that identify and order the event remain.
//
// An event still over maxBytes is an error. The id is derived from the bytes about to be written, so
// a cut can never leave it describing an event that no longer exists, and an event already at the
// limit keeps its bytes and goes without one: the limit is a hard contract, and refusing an event
// over forty bytes of identity would be the worse trade.
func FitEventLine(line []byte, maxBytes int, stamp func(id string) ([]byte, error)) ([]byte, error) {
	if len(line) <= maxBytes {
		return stampEventLine(line, maxBytes, stamp)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	// Numbers stay as written: a sequence or token count past 2^53 must not come back rounded.
	decoder.UseNumber()
	var event map[string]interface{}
	if err := decoder.Decode(&event); err != nil {
		return nil, fmt.Errorf("decode oversized event: %w", err)
	}
	for _, cut := range []func(map[string]interface{}) map[string]interface{}{
		dropRawAndMessage, compactRetainedContent, minimalEvent,
	} {
		event = cut(event)
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		if len(data) > maxBytes {
			continue
		}
		info, ok := event["event"].(map[string]interface{})
		if stamp == nil || !ok {
			return data, nil
		}
		return stampEventLine(data, maxBytes, func(id string) ([]byte, error) {
			info["id"] = id
			return json.Marshal(event)
		})
	}
	return nil, fmt.Errorf("event exceeds %d bytes after truncation", maxBytes)
}

// stampEventLine is line with event.id set to the id derived from line, as stamp encodes it, or line
// itself when there is nothing to stamp or the stamped line would not fit.
func stampEventLine(line []byte, maxBytes int, stamp func(id string) ([]byte, error)) ([]byte, error) {
	if stamp == nil {
		return line, nil
	}
	stamped, err := stamp(EventIDForLine(line))
	if err != nil {
		return nil, err
	}
	if len(stamped) > maxBytes {
		return line, nil
	}
	return stamped, nil
}

func dropRawAndMessage(event map[string]interface{}) map[string]interface{} {
	delete(event, "raw")
	if message, ok := event["message"].(string); ok {
		event["message"] = TruncateString(message, eventLineMessageLimit)
	}
	event["field_truncated"] = true
	return event
}

// compactRetainedContent drops an event's retained text and records that it did, so a reader can
// still tell what the event was about and that its content was held back rather than never
// captured. The hash and byte count in the content marker describe the whole redacted text and stay.
func compactRetainedContent(event map[string]interface{}) map[string]interface{} {
	if file, ok := event["file"].(map[string]interface{}); ok {
		delete(file, "diff")
	}
	if command, ok := event["command"].(map[string]interface{}); ok {
		delete(command, "output")
	}
	if prompt, ok := event["prompt"].(map[string]interface{}); ok {
		delete(prompt, "text")
	}
	if genAI, ok := event["gen_ai"].(map[string]interface{}); ok {
		delete(genAI, "input")
		delete(genAI, "output")
		if tool, ok := genAI["tool"].(map[string]interface{}); ok {
			if call, ok := tool["call"].(map[string]interface{}); ok {
				delete(call, "arguments")
				delete(call, "result")
			}
		}
		// An event whose only gen_ai content was the turn text drops the object rather than
		// writing "gen_ai":{}.
		if len(genAI) == 0 {
			delete(event, "gen_ai")
		}
	}
	if content, ok := event["content"].(map[string]interface{}); ok {
		content["included"] = false
		content["truncated"] = true
	}
	return event
}

// minimalEventKeys are the top-level blocks an event keeps when nothing else fits: what it was,
// when, in what order, from whom and which session, and its provenance markers.
var minimalEventKeys = []string{
	"timestamp", "sequence", "vendor", "product", "schema_version", "event", "severity",
	"endpoint", "user", "harness", "origin", "run", "session", "trace",
	"error", "tool", "file", "command", "mcp", "approval", "policy",
	"content", "destination", "health", "model", "repository",
	"branch", "message",
}

func minimalEvent(event map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{"field_truncated": true}
	for _, key := range minimalEventKeys {
		if value, ok := event[key]; ok && value != nil {
			out[key] = value
		}
	}
	if genAI, ok := event["gen_ai"].(map[string]interface{}); ok {
		summary := map[string]interface{}{}
		for _, key := range []string{"operation", "usage", "response", "provider"} {
			if value := genAI[key]; value != nil {
				summary[key] = value
			}
		}
		if tool, ok := genAI["tool"].(map[string]interface{}); ok {
			toolSummary := map[string]interface{}{"name": tool["name"], "type": tool["type"]}
			if call, ok := tool["call"].(map[string]interface{}); ok {
				toolSummary["call"] = map[string]interface{}{"id": call["id"]}
			}
			summary["tool"] = toolSummary
		}
		if len(summary) > 0 {
			out["gen_ai"] = summary
		}
	}
	return out
}
