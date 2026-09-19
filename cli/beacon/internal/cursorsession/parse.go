package cursorsession

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func composerPreview(c composerData) string {
	for _, value := range []string{c.Name, c.Text, c.RichText} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "{") {
			if text := richTextPreview(value); text != "" {
				return truncateLine(text, 100)
			}
		}
		return truncateLine(value, 100)
	}
	return "(No title)"
}

func richTextPreview(value string) string {
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		return ""
	}
	var parts []string
	var walk func(interface{})
	walk = func(v interface{}) {
		switch typed := v.(type) {
		case map[string]interface{}:
			if text, ok := typed["text"].(string); ok {
				parts = append(parts, text)
			}
			for _, child := range []string{"content", "children"} {
				walk(typed[child])
			}
		case []interface{}:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(doc)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func transcriptPreview(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(No preview)"
	}
	for _, rec := range parseTranscriptRecords(data) {
		if text := userTextFromRecord(rec); text != "" {
			return truncateLine(stripUserQueryTags(text), 100)
		}
	}
	return "(No preview)"
}

func parseTranscriptRecords(data []byte) []transcriptRecord {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	var whole interface{}
	if err := json.Unmarshal(trimmed, &whole); err == nil {
		if records := coerceTranscriptCandidates(whole); records != nil {
			return records
		}
	}
	var out []transcriptRecord
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var v interface{}
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		if rec := asMap(v); rec != nil {
			out = append(out, rec)
		}
	}
	return out
}

func coerceTranscriptCandidates(v interface{}) []transcriptRecord {
	if list, ok := v.([]interface{}); ok {
		out := make([]transcriptRecord, 0, len(list))
		for _, item := range list {
			if rec := asMap(item); rec != nil {
				out = append(out, rec)
			}
		}
		return out
	}
	rec := asMap(v)
	if rec == nil {
		return nil
	}
	for _, key := range []string{"messages", "events", "entries"} {
		if list, ok := rec[key].([]interface{}); ok {
			out := make([]transcriptRecord, 0, len(list))
			for _, item := range list {
				if child := asMap(item); child != nil {
					out = append(out, child)
				}
			}
			return out
		}
	}
	return []transcriptRecord{rec}
}

func appendTranscriptMessage(out *[]Record, pending map[string]Record, msg map[string]interface{}, baseID string, ts int64, fallbackRole, fallbackModel string, order int) int {
	role := normalizeRole(firstNonEmpty(pickString(msg, "role"), fallbackRole))
	model := firstNonEmpty(pickString(msg, "modelId", "model"), fallbackModel)
	used, limit := contextFromAny(msg)
	if role == "user" {
		if text := messageText(msg); text != "" {
			*out = append(*out, Record{Order: order, NativeID: baseID, Type: "user_message", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, Content: stripUserQueryTags(text), Model: model})
			order++
		}
		return order
	}
	if role == "assistant" || role == "agent" {
		reasoning := reasoningTexts(msg)
		for i, thinking := range reasoning {
			if strings.TrimSpace(thinking) == "" {
				continue
			}
			*out = append(*out, Record{Order: order, NativeID: fmt.Sprintf("%s:thinking:%d", baseID, i), Type: "agent_thinking", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, Content: thinking, Model: model})
			order++
		}
		if text := messageText(msg); text != "" {
			*out = append(*out, Record{Order: order, NativeID: baseID, Type: "agent_text", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, Content: text, Model: model})
			order++
		}
		for _, call := range toolCallsFromMessage(msg) {
			r := transcriptToolCall(call, baseID, ts, order)
			r.ContextUsedTokens = used
			r.ContextLimitTokens = limit
			pending[r.CallID] = r
			*out = append(*out, r)
			order++
		}
		return order
	}
	if text := messageText(msg); text != "" {
		*out = append(*out, Record{Order: order, NativeID: baseID, Type: "agent_text", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, Content: text, Model: model})
		order++
	}
	return order
}

func reasoningTexts(msg map[string]interface{}) []string {
	var out []string
	if text := pickString(msg, "thinking", "reasoning"); text != "" {
		out = append(out, text)
	}
	for _, block := range normalizeBlocks(msg["content"]) {
		typ := strings.ToLower(firstNonEmpty(pickString(block, "type"), "text"))
		if typ == "thinking" || typ == "reasoning" {
			if text := firstNonEmpty(pickString(block, "thinking", "text"), stringFrom(block["content"])); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func messageText(msg map[string]interface{}) string {
	if text := firstNonEmpty(pickString(msg, "text"), stringFrom(msg["content"]), stringFrom(msg["message"])); text != "" {
		return text
	}
	var parts []string
	for _, block := range normalizeBlocks(msg["content"]) {
		typ := strings.ToLower(firstNonEmpty(pickString(block, "type"), "text"))
		if typ != "text" {
			continue
		}
		if text := firstNonEmpty(pickString(block, "text"), stringFrom(block["content"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func userTextFromRecord(rec transcriptRecord) string {
	typ := strings.ToLower(pickString(rec, "type"))
	role := normalizeRole(pickString(rec, "role"))
	if typ == "user_message" || role == "user" {
		return firstNonEmpty(messageText(rec), stringFrom(rec["message"]))
	}
	if msg := asMap(rec["message"]); msg != nil {
		if normalizeRole(pickString(msg, "role")) == "user" {
			return messageText(msg)
		}
	}
	return ""
}

func toolCallsFromMessage(msg map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	for _, key := range []string{"toolCalls", "tool_calls"} {
		if list, ok := msg[key].([]interface{}); ok {
			for _, item := range list {
				if call := asMap(item); call != nil {
					out = append(out, call)
				}
			}
		}
	}
	return out
}

func transcriptToolCall(call map[string]interface{}, baseID string, ts int64, order int) Record {
	callID := firstNonEmpty(pickString(call, "id", "toolCallId", "callId"), baseID+":call")
	name := firstNonEmpty(pickString(call, "name", "toolName", "tool"), "unknown_tool")
	args := objectArgs(firstNonNil(call["args"], call["arguments"], call["params"], call["input"]))
	used, limit := contextFromAny(call)
	return Record{Order: order, NativeID: baseID + ":tool_call", Type: "tool_call", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, CallID: callID, ToolName: name, Args: args}
}

func transcriptToolResult(result map[string]interface{}, baseID string, ts int64, order int, pending map[string]Record) Record {
	callID := firstNonEmpty(pickString(result, "toolCallId", "callId", "id"), baseID+":call")
	name := firstNonEmpty(pickString(result, "toolName", "name", "tool"), pending[callID].ToolName, "unknown_tool")
	args := pending[callID].Args
	output := firstNonEmpty(pickString(result, "output", "result", "content", "text"), stringify(firstNonNil(result["result"], result["data"])))
	status := strings.ToLower(firstNonEmpty(pickString(result, "status"), "success"))
	if status == "failure" || status == "failed" {
		status = "error"
	}
	used, limit := contextFromAny(result)
	if used == 0 {
		used = pending[callID].ContextUsedTokens
	}
	if limit == 0 {
		limit = pending[callID].ContextLimitTokens
	}
	return Record{Order: order, NativeID: baseID + ":tool_result", Type: "tool_result", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, CallID: callID, ToolName: name, Args: args, Output: output, Status: status}
}

func parseTranscriptTimestamp(rec map[string]interface{}, fallback int64) int64 {
	for _, key := range []string{"timestamp", "createdAt", "time"} {
		if ms := millisFrom(rec[key]); ms > 0 {
			return ms
		}
	}
	return fallback
}

func millisFrom(v interface{}) int64 {
	switch typed := v.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		n, _ := typed.Int64()
		return n
	case string:
		if typed == "" {
			return 0
		}
		if n, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return n
		}
		if t, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func contextFromAny(values ...interface{}) (used, limit int64) {
	for _, value := range values {
		u, l := contextFromValue(value, 0)
		if used == 0 {
			used = u
		}
		if limit == 0 {
			limit = l
		}
	}
	return used, limit
}

func contextFromValue(value interface{}, depth int) (used, limit int64) {
	if value == nil || depth > 4 {
		return 0, 0
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		used = firstIntKey(typed,
			"context_tokens",
			"contextTokens",
			"contextTokenCount",
			"context_usage_tokens",
			"contextUsageTokens",
			"context_used_tokens",
			"contextUsedTokens",
			"used_context_tokens",
			"usedContextTokens",
		)
		limit = firstIntKey(typed,
			"context_window_size",
			"contextWindowSize",
			"context_window_tokens",
			"contextWindowTokens",
			"context_token_budget",
			"contextTokenBudget",
			"max_context_tokens",
			"maxContextTokens",
			"context_limit",
			"contextLimit",
			"context_limit_tokens",
			"contextLimitTokens",
		)
		if used > 0 && limit > 0 {
			return used, limit
		}
		for key, child := range typed {
			if !strings.Contains(strings.ToLower(key), "context") && key != "data" && key != "message" {
				continue
			}
			u, l := contextFromValue(child, depth+1)
			if used == 0 {
				used = u
			}
			if limit == 0 {
				limit = l
			}
			if used > 0 && limit > 0 {
				return used, limit
			}
		}
	case []interface{}:
		for _, child := range typed {
			u, l := contextFromValue(child, depth+1)
			if used == 0 {
				used = u
			}
			if limit == 0 {
				limit = l
			}
			if used > 0 && limit > 0 {
				return used, limit
			}
		}
	default:
		data, err := json.Marshal(typed)
		if err != nil || len(data) == 0 || data[0] != '{' {
			return 0, 0
		}
		var asObject map[string]interface{}
		if err := json.Unmarshal(data, &asObject); err != nil {
			return 0, 0
		}
		return contextFromValue(asObject, depth+1)
	}
	return used, limit
}

func firstIntKey(m map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		if value := int64From(m[key]); value > 0 {
			return value
		}
	}
	return 0
}

func int64From(value interface{}) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return n
	default:
		return 0
	}
}

func formatToolFormerResult(data *toolFormerData) string {
	if data == nil {
		return ""
	}
	if strings.TrimSpace(data.Result) != "" {
		var pretty interface{}
		if err := json.Unmarshal([]byte(data.Result), &pretty); err == nil {
			if encoded, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				return string(encoded)
			}
		}
		return data.Result
	}
	if data.AdditionalData != nil && isCursorGrepTool(data.Name) {
		return cursorGrepSummary(data.AdditionalData)
	}
	return ""
}

func cursorGrepSummary(data map[string]interface{}) string {
	var lines []string
	if v, ok := data["totalMatches"].(float64); ok {
		lines = append(lines, fmt.Sprintf("Matches: %.0f", v))
	}
	if v, ok := data["totalFiles"].(float64); ok {
		lines = append(lines, fmt.Sprintf("Files: %.0f", v))
	}
	if pattern, ok := data["pattern"].(string); ok && strings.TrimSpace(pattern) != "" {
		lines = append(lines, "Pattern: "+pattern)
	}
	if path, ok := data["path"].(string); ok && strings.TrimSpace(path) != "" {
		lines = append(lines, "Path: "+path)
	}
	return strings.Join(lines, "\n")
}

func isCursorGrepTool(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(name))
	return strings.Contains(normalized, "grep") || strings.Contains(normalized, "ripgrep") || (strings.Contains(normalized, "search") && strings.Contains(normalized, "code"))
}

func decodeArgs(params string) map[string]interface{} {
	if strings.TrimSpace(params) == "" {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(params), &out); err != nil {
		return map[string]interface{}{"raw": params}
	}
	return out
}

func objectArgs(v interface{}) map[string]interface{} {
	switch typed := v.(type) {
	case map[string]interface{}:
		return typed
	case string:
		return decodeArgs(typed)
	default:
		if typed == nil {
			return nil
		}
		return map[string]interface{}{"value": typed}
	}
}

func (s *Store) enrichEditFileV2Args(db *sql.DB, args map[string]interface{}, result string) map[string]interface{} {
	if args == nil {
		args = map[string]interface{}{}
	}
	var resultObj map[string]interface{}
	if err := json.Unmarshal([]byte(result), &resultObj); err != nil {
		return args
	}
	beforeID, _ := resultObj["beforeContentId"].(string)
	afterID, _ := resultObj["afterContentId"].(string)
	before := readComposerContent(db, beforeID)
	after := readComposerContent(db, afterID)
	if before == "" || after == "" {
		return args
	}
	if _, ok := args["path"]; !ok {
		if path, ok := args["relativeWorkspacePath"].(string); ok {
			args["path"] = path
		}
	}
	args["old_string"] = truncateForArg(before)
	args["new_string"] = truncateForArg(after)
	return args
}

func readComposerContent(db *sql.DB, id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	key := id
	if !strings.HasPrefix(key, contentPrefix) {
		key = contentPrefix + key
	}
	var raw []byte
	if err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key = ?`, key).Scan(&raw); err != nil {
		return ""
	}
	return string(raw)
}

func truncateForArg(value string) string {
	const limit = 100000
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func compactionSummary(c composerData) map[string]interface{} {
	if c.LatestConversationSummary == nil || c.LatestConversationSummary.Summary == nil {
		return nil
	}
	summary := c.LatestConversationSummary.Summary
	if strings.TrimSpace(summary.TruncationLastBubbleIDInclusive) == "" {
		return nil
	}
	out := map[string]interface{}{
		"compacted_through_id": summary.TruncationLastBubbleIDInclusive,
	}
	if v := strings.TrimSpace(c.LatestConversationSummary.LastBubbleID); v != "" {
		out["boundary_message_id"] = v
	}
	if v := strings.TrimSpace(summary.PreviousConversationSummaryBubbleID); v != "" {
		out["previous_compaction_id"] = v
	}
	if v := strings.TrimSpace(summary.ClientShouldStartSendingFromInclusive); v != "" {
		out["retained_from_id"] = v
	}
	if v := strings.TrimSpace(summary.Summary); v != "" {
		out["summary"] = v
	}
	if summary.IncludesToolResults != nil {
		out["summary_includes_tool_results"] = *summary.IncludesToolResults
	}
	return out
}

func (s *Store) resolveComposerWorkspace(db queryRower, id string, composer composerData) string {
	if path := directoryFromContext(composer.Context); path != "" {
		return path
	}
	for i, header := range composer.FullConversationHeadersOnly {
		if i >= 8 {
			break
		}
		bubble, ok := readBubbleFrom(db, id, header.BubbleID)
		if !ok {
			continue
		}
		if path := directoryFromURIs(bubble.WorkspaceURIs); path != "" {
			return path
		}
		if path := directoryFromContext(bubble.Context); path != "" {
			return path
		}
		if path := directoryFromTool(bubble.ToolFormerData); path != "" {
			return path
		}
	}
	return ""
}

type queryRower interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

func readBubbleFrom(db queryRower, composerID, bubbleID string) (bubbleData, bool) {
	var raw []byte
	err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key = ?`, bubblePrefix+composerID+":"+bubbleID).Scan(&raw)
	if err != nil {
		return bubbleData{}, false
	}
	var bubble bubbleData
	if err := json.Unmarshal(raw, &bubble); err != nil {
		return bubbleData{}, false
	}
	return bubble, true
}

func directoryFromContext(ctx map[string]interface{}) string {
	if ctx == nil {
		return ""
	}
	var candidates []interface{}
	for _, key := range []string{"uri", "path"} {
		if ctx[key] != nil {
			candidates = append(candidates, ctx[key])
		}
	}
	for _, key := range []string{"folderSelections", "fileSelections", "terminalFiles"} {
		if list, ok := ctx[key].([]interface{}); ok {
			candidates = append(candidates, list...)
		}
	}
	for _, candidate := range candidates {
		if path := directoryCandidate(candidate); path != "" {
			return path
		}
	}
	return ""
}

func directoryFromURIs(values []string) string {
	for _, value := range values {
		if path := normalizeDirectoryCandidate(value); path != "" {
			return path
		}
	}
	return ""
}

func directoryFromTool(tool *toolFormerData) string {
	if tool == nil || strings.TrimSpace(tool.Params) == "" {
		return ""
	}
	args := decodeArgs(tool.Params)
	for _, key := range []string{"targetFile", "targetDirectory", "filePath", "file_path", "path", "directoryPath", "directory_path", "absolutePath"} {
		if value, ok := args[key].(string); ok && filepath.IsAbs(value) {
			return filepath.Dir(value)
		}
	}
	return ""
}

func directoryCandidate(v interface{}) string {
	if s, ok := v.(string); ok {
		return normalizeDirectoryCandidate(s)
	}
	m := asMap(v)
	if m == nil {
		return ""
	}
	for _, key := range []string{"uri", "path", "filePath"} {
		if value, ok := m[key].(string); ok {
			if path := normalizeDirectoryCandidate(value); path != "" {
				return path
			}
		}
	}
	return ""
}

func normalizeDirectoryCandidate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "file://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "file" {
			return ""
		}
		value = u.Path
	}
	if !filepath.IsAbs(value) {
		return ""
	}
	if info, err := os.Stat(value); err == nil && !info.IsDir() {
		return filepath.Dir(value)
	}
	return value
}

func resolveDashEncodedProjectDirectory(name string) string {
	parts := strings.FieldsFunc(strings.TrimLeft(name, "-"), func(r rune) bool { return r == '-' })
	if len(parts) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var walk func(root string, idx int) string
	walk = func(root string, idx int) string {
		key := root + "\x00" + strconv.Itoa(idx)
		if seen[key] {
			return ""
		}
		seen[key] = true
		for end := len(parts); end > idx; end-- {
			component := strings.Join(parts[idx:end], "-")
			candidate := filepath.Join(root, component)
			info, err := os.Stat(candidate)
			if err != nil || !info.IsDir() {
				continue
			}
			if end == len(parts) {
				return candidate
			}
			if nested := walk(candidate, end); nested != "" {
				return nested
			}
		}
		return ""
	}
	roots := []string{string(filepath.Separator)}
	if runtime.GOOS == "windows" && len(parts) > 0 && len(parts[0]) == 1 {
		roots = append(roots, strings.ToUpper(parts[0])+`:\`)
	}
	for _, root := range roots {
		start := 0
		if root != string(filepath.Separator) {
			start = 1
		}
		if resolved := walk(root, start); resolved != "" {
			return resolved
		}
	}
	return name
}

func stripUserQueryTags(s string) string {
	s = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`).ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

func isTranscriptFilename(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jsonl") || filepath.Ext(name) == ""
}

func traceIDFromFilename(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".jsonl"):
		return name[:len(name)-6]
	case strings.HasSuffix(lower, ".json"):
		return name[:len(name)-5]
	default:
		return name
	}
}

func normalizeTraceID(id string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(id), "transcript:"), "transcript%3A"), "transcript_3A")
}

func birthUnixMS(path string, fallback int64) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return fallback
	}
	if !info.ModTime().IsZero() {
		return info.ModTime().UnixMilli()
	}
	return fallback
}

func asMap(v interface{}) map[string]interface{} {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	if rec, ok := v.(transcriptRecord); ok {
		return map[string]interface{}(rec)
	}
	return nil
}

func normalizeBlocks(v interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	if list, ok := v.([]interface{}); ok {
		for _, item := range list {
			if m := asMap(item); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func pickString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringFrom(m[key]); value != "" {
			return value
		}
	}
	return ""
}

func pickNestedString(m map[string]interface{}, first, second string) string {
	if child := asMap(m[first]); child != nil {
		return pickString(child, second)
	}
	return ""
}

func stringFrom(v interface{}) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func stringify(v interface{}) string {
	if v == nil {
		return ""
	}
	if s := stringFrom(v); s != "" {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "human", "user":
		return "user"
	case "assistant", "agent", "ai":
		return "assistant"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func truncateLine(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func stringValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func outPtr(out *[]Record) *[]Record { return out }
