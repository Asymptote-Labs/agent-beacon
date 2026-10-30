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
	action    string
	harness   string
	sessionID string
	key       string
	callID    string
	ts        time.Time
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
	sessionBytes := dedupeSessionNeedle(candidate.sessionID)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if sessionBytes != nil && !mayDuplicate(line, sessionBytes, candidate.action) {
			continue
		}
		existing, ok := endpointDedupeCandidate(line)
		if !ok {
			continue
		}
		if duplicateEndpointEvents(existing, candidate, effectiveWindow) {
			return true
		}
	}
	return false
}

// DuplicateEndpointLines reports whether candidate duplicates existing under the
// same rules the writer applies when appending to a runtime log.
//
// Exported for readers that must agree with the writer about what one action is
// -- the sandbox verifier, which counts captured events against a scenario's
// expectations, and anything else that would otherwise keep a second copy of
// these rules. A second copy is the thing this change exists to stop: two
// implementations of one rule have already drifted apart here once, silently,
// and a duplicated suppression rule is no safer than a duplicated harness name
// was.
//
// window is the caller's base window; the action-specific floor still applies.
func DuplicateEndpointLines(existing, candidate []byte, window time.Duration) bool {
	parsedCandidate, ok := endpointDedupeCandidate(candidate)
	if !ok {
		return false
	}
	parsedExisting, ok := endpointDedupeCandidate(existing)
	if !ok {
		return false
	}
	return duplicateEndpointEvents(parsedExisting, parsedCandidate,
		endpointDedupeWindow(parsedCandidate.action, window))
}

// duplicateEndpointEvents is the one pairwise decision both entry points share,
// kept on parsed events so the tail scan does not reparse the candidate for
// every line it compares it against.
func duplicateEndpointEvents(existing, candidate endpointDedupeEvent, effectiveWindow time.Duration) bool {
	if existing.key != candidate.key {
		return false
	}
	// An equal, non-empty tool call ID is the runtime's own statement that these
	// two lines describe one call, and it settles the question on its own: no
	// window, and no interest in which capture path reported it.
	//
	// The window is deliberately not applied here. A hook writes when the tool
	// runs and the collector writes when its batch flushes, so the two reports
	// of one call routinely land five seconds apart -- past the two-second
	// window that is the only thing that ever collapsed them.
	if existing.callID != "" && existing.callID == candidate.callID {
		return true
	}
	// Without matching IDs, only the timing heuristic is left, and only across
	// capture paths: two adjacent calls from one path can legitimately touch the
	// same file or command, so same-harness lookalikes are preserved.
	//
	// Unequal IDs are not evidence of two calls unless one path assigned both.
	// Beacon's paths do not share an ID space -- OpenCode's own call ID and the
	// span ID the collector sees for the same call are simply different names --
	// so across harnesses the IDs are ignored rather than trusted to disagree.
	if existing.harness == candidate.harness {
		return false
	}
	diff := candidate.ts.Sub(existing.ts)
	if diff < 0 {
		diff = -diff
	}
	return diff <= effectiveWindow
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
	ts, err := ParseTimestamp(stringValue(event["timestamp"]))
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
	return endpointDedupeEvent{action: strings.ToLower(action), harness: harness, sessionID: sessionID, key: key, callID: callID, ts: ts.UTC()}, true
}

// dedupeSessionNeedle is the bytes every log line describing sessionID holds, or nil when there is
// no such guarantee.
//
// Decoding a line is what the tail scan costs: a hook pays it for every line of the last 256 KiB
// of the log, before every event it writes. A line can only duplicate the candidate when its
// session.id and its event.action are the same (the action compared without case), and a value of
// printable ASCII without a quote or backslash has one spelling in JSON that does not escape
// ordinary characters, so a line that does not hold both verbatim can be skipped undecoded. An id
// that a JSON number, literal, object or array would also produce (stringValue accepts those) gets
// no needle, and mayDuplicate still decodes every line with a \u or \/ escape in it. Every action
// dedupeAction accepts is such a value.
func dedupeSessionNeedle(sessionID string) []byte {
	if sessionID == "" || sessionID == "true" || sessionID == "false" || sessionID == "null" {
		return nil
	}
	if sessionID[0] == '{' || sessionID[0] == '[' || sessionID[0] == '-' || (sessionID[0] >= '0' && sessionID[0] <= '9') {
		return nil
	}
	for i := 0; i < len(sessionID); i++ {
		if c := sessionID[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return nil
		}
	}
	return []byte(sessionID)
}

// mayDuplicate reports whether line could describe action in the session, by its bytes alone.
func mayDuplicate(line, session []byte, action string) bool {
	if bytes.Contains(line, []byte(`\u`)) || bytes.Contains(line, []byte(`\/`)) {
		return true
	}
	// The action is compared after strings.ToLower, which takes U+0130 to i and U+212A (the
	// Kelvin sign) to k: an action spelled with either matches in bytes no fold of ours finds.
	if bytes.Contains(line, []byte("\u0130")) || bytes.Contains(line, []byte("\u212a")) {
		return true
	}
	return bytes.Contains(line, session) && containsFold(line, action)
}

// containsFold reports whether s holds needle, which is lower case ASCII, in any case.
func containsFold(s []byte, needle string) bool {
	if needle == "" {
		return true
	}
	first, upper := needle[0], needle[0]
	if first >= 'a' && first <= 'z' {
		upper = first - 'a' + 'A'
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		if (s[i] == first || s[i] == upper) && bytes.EqualFold(s[i:i+len(needle)], []byte(needle)) {
			return true
		}
	}
	return false
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
		return canonicalLiteralTarget("command", nestedString(event, "command", "command"), nestedString(event, "tool", "command"))
	case "file.read", "file.modified":
		// The path alone, deliberately. file.operation is the one part of a file
		// event the two capture paths describe differently for the same action:
		// a Claude Code Write reaches the hook through diffFields, which records
		// "modify", and reaches the collector as "create". Including it in the
		// target let one action fail to match itself, so every Write stayed in
		// the log twice however well the two events were identified otherwise.
		//
		// Nothing is lost by dropping it. The action already separates a read
		// from a write, and within file.modified the operation only ever varies
		// between two words for the same event -- so it discriminated nothing
		// and disagreed often. The sandbox checker's mirror of this key has
		// always used the path alone.
		return canonicalLiteralTarget("file", nestedString(event, "file", "path"))
	case "tool.completed":
		return canonicalTarget("tool.completed", stringValue(event["model"]), stringValue(event["message"]))
	case "tool.invoked", "tool.failed":
		return canonicalTarget("tool", nestedString(event, "tool", "name"), nestedString(event, "tool", "path"), stringValue(event["message"]))
	default:
		return ""
	}
}

// canonicalTarget builds a dedupe key from values that name an MCP server or
// tool. Those arrive spelled differently from the two capture paths -- a Claude
// Code tool name reaches the hook as "mcp__server__tool" and the collector
// reports the server and tool separately -- so the prefix and "__" separator
// are folded away to let one call match itself.
func canonicalTarget(prefix string, values ...string) string {
	return joinCanonicalTarget(prefix, values, true)
}

// canonicalLiteralTarget builds a dedupe key from free-form text: a shell
// command line or a filesystem path. It folds case, because the two capture
// paths can spell the same command or directory differently, but leaves the
// text otherwise intact.
//
// The MCP alias rules canonicalTarget applies have no meaning here: "__" is an
// ordinary character sequence in a command argument or a path segment, and
// rewriting it to ":" makes the key lossy about input it was never meant to
// interpret.
func canonicalLiteralTarget(prefix string, values ...string) string {
	return joinCanonicalTarget(prefix, values, false)
}

func joinCanonicalTarget(prefix string, values []string, aliasMCPNames bool) string {
	var parts []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if aliasMCPNames {
			value = strings.TrimPrefix(value, "mcp:")
			value = strings.TrimPrefix(value, "mcp__")
			value = strings.ReplaceAll(value, "__", ":")
		}
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
