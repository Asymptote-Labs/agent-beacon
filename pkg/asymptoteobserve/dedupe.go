package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"
)

const (
	// EndpointDuplicateWindow is intentionally short: it catches overlapping
	// hook/OTLP reports for the same runtime action without collapsing separate
	// tool calls that happen later in the same session.
	EndpointDuplicateWindow = 2 * time.Second
	endpointDedupeTailBytes = 256 * 1024
)

type endpointDedupeEvent struct {
	action  string
	harness string
	key     string
	callID  string
	ts      time.Time
}

// IsDuplicateEndpointEvent reports whether candidateLine duplicates a recently
// appended endpoint event in path. It is best called while the runtime log lock
// is already held by the caller.
func IsDuplicateEndpointEvent(path string, candidateLine []byte, window time.Duration) bool {
	candidate, ok := endpointDedupeCandidate(candidateLine)
	if !ok {
		return false
	}
	data, err := readEndpointDedupeTail(path)
	if err != nil {
		return false
	}
	effectiveWindow := endpointDedupeWindow(candidate.action, window)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		existing, ok := endpointDedupeCandidate(line)
		if !ok || existing.key != candidate.key {
			continue
		}
		// Same-harness events are normally preserved because two adjacent calls
		// can legitimately target the same file or command. A stable tool call
		// ID makes an exact duplicate safe to collapse.
		if existing.harness == candidate.harness {
			if existing.callID == "" || candidate.callID == "" || existing.callID != candidate.callID {
				continue
			}
		}
		diff := candidate.ts.Sub(existing.ts)
		if diff < 0 {
			diff = -diff
		}
		if diff <= effectiveWindow {
			return true
		}
	}
	return false
}

func readEndpointDedupeTail(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > endpointDedupeTailBytes {
		offset = size - endpointDedupeTailBytes
	}
	buf := make([]byte, size-offset)
	_, err = f.ReadAt(buf, offset)
	if err != nil && len(buf) == 0 {
		return nil, err
	}
	return buf, nil
}

func endpointDedupeCandidate(line []byte) (endpointDedupeEvent, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return endpointDedupeEvent{}, false
	}
	var event map[string]interface{}
	if err := json.Unmarshal(line, &event); err != nil {
		return endpointDedupeEvent{}, false
	}
	action := nestedString(event, "event", "action")
	if !dedupeAction(action) {
		return endpointDedupeEvent{}, false
	}
	sessionID := nestedString(event, "session", "id")
	if sessionID == "" {
		return endpointDedupeEvent{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, stringValue(event["timestamp"]))
	if err != nil || ts.IsZero() {
		return endpointDedupeEvent{}, false
	}
	harness := strings.ToLower(nestedString(event, "harness", "name"))
	callID := nestedString(event, "gen_ai", "tool", "call", "id")
	target := dedupeTarget(action, event)
	if target == "" {
		return endpointDedupeEvent{}, false
	}
	workspace := firstNonEmptyString(
		nestedString(event, "session", "working_directory"),
		stringValue(event["repository"]),
	)
	key := strings.Join([]string{
		strings.ToLower(action),
		sessionID,
		strings.ToLower(workspace),
		target,
	}, "\x00")
	return endpointDedupeEvent{action: strings.ToLower(action), harness: harness, key: key, callID: callID, ts: ts.UTC()}, true
}

func endpointDedupeWindow(action string, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = EndpointDuplicateWindow
	}
	if strings.EqualFold(action, "tool.completed") && fallback < 10*time.Second {
		return 10 * time.Second
	}
	return fallback
}

func dedupeAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "mcp.tool_invoked",
		"command.executed",
		"file.read",
		"file.modified",
		"tool.invoked",
		"tool.completed",
		"tool.failed":
		return true
	default:
		return false
	}
}

func dedupeTarget(action string, event map[string]interface{}) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "mcp.tool_invoked":
		return canonicalTarget("mcp",
			nestedString(event, "mcp", "server"),
			firstNonEmptyString(
				nestedString(event, "mcp", "tool"),
				nestedString(event, "tool", "name"),
				stringValue(event["message"]),
			),
		)
	case "command.executed":
		return canonicalTarget("command", nestedString(event, "command", "command"), nestedString(event, "tool", "command"))
	case "file.read", "file.modified":
		return canonicalTarget("file", nestedString(event, "file", "path"), nestedString(event, "file", "operation"))
	case "tool.completed":
		return canonicalTarget("tool.completed", stringValue(event["model"]), stringValue(event["message"]))
	case "tool.invoked", "tool.failed":
		return canonicalTarget("tool", nestedString(event, "tool", "name"), nestedString(event, "tool", "path"), stringValue(event["message"]))
	default:
		return ""
	}
}

func canonicalTarget(prefix string, values ...string) string {
	var parts []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.TrimPrefix(value, "mcp:")
		value = strings.TrimPrefix(value, "mcp__")
		value = strings.ReplaceAll(value, "__", ":")
		if value != "" && value != "<nil>" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return prefix + ":" + strings.Join(parts, "|")
}

func nestedString(root map[string]interface{}, keys ...string) string {
	var current interface{} = root
	for _, key := range keys {
		next, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current = next[key]
	}
	return stringValue(current)
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		return strings.TrimSpace(toString(typed))
	}
}

func toString(value interface{}) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return strings.Trim(string(data), `"`)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
