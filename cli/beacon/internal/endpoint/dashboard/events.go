package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	defaultEventLimit = 500
	maxEventLimit     = 2000
)

type EventRecord struct {
	ID         string          `json:"id"`
	Line       int             `json:"line"`
	Event      schema.Event    `json:"event"`
	Raw        json.RawMessage `json:"-"`
	Parsed     time.Time       `json:"parsed_timestamp,omitempty"`
	WazuhLevel int             `json:"wazuh_level,omitempty"`
}

type EventQuery struct {
	Limit      int
	NoLimit    bool
	Since      time.Time
	Until      time.Time
	Q          string
	Harness    string
	Model      string
	Action     string
	Severity   string
	Category   string
	Repository string
	Session    string
	Trace      string
	File       string
	Command    string
	MCP        string
	Approval   string
	Decision   string
	Policy     string
	Review     string
	WazuhLevel string
}

type EventResult struct {
	Events         []EventRecord     `json:"events"`
	TotalMatched   int               `json:"total_matched"`
	MalformedLines int               `json:"malformed_lines"`
	Limit          int               `json:"limit"`
	Query          string            `json:"query,omitempty"`
	Filters        map[string]string `json:"filters,omitempty"`
	Returned       int               `json:"returned"`
	Truncated      bool              `json:"truncated"`
}

func ReadEvents(path string, query EventQuery) (EventResult, error) {
	limit := normalizeLimit(query.Limit)
	result := EventResult{Limit: limit, Query: strings.TrimSpace(query.Q), Filters: activeFilters(query)}
	for _, source := range eventSources(path) {
		if err := readEventsFromSource(source, query, &result, limit); err != nil {
			return EventResult{}, err
		}
	}
	sort.SliceStable(result.Events, func(i, j int) bool {
		if result.Events[i].Parsed.Equal(result.Events[j].Parsed) {
			return result.Events[i].ID > result.Events[j].ID
		}
		return result.Events[i].Parsed.After(result.Events[j].Parsed)
	})
	result.Returned = len(result.Events)
	result.Truncated = result.TotalMatched > len(result.Events)
	return result, nil
}

// SortRecordsAppendOrder orders records oldest-first in log append order: by
// timestamp, then older archives before the live log, then by numeric line.
// Token aggregation needs this because runtime timestamps are second-resolution
// (a batch of cumulative datapoints shares a timestamp) and the cumulative
// dedup relies on append order; the newest-first ReadEvents sort breaks ties on
// lexicographic line IDs, which mis-orders line-9 vs line-10 within a second.
func SortRecordsAppendOrder(records []EventRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].Parsed.Equal(records[j].Parsed) {
			return records[i].Parsed.Before(records[j].Parsed)
		}
		ai, li, _ := parseRecordID(records[i].ID)
		aj, lj, _ := parseRecordID(records[j].ID)
		if ai != aj {
			// Higher archive index is an older rotation, so it comes first.
			return ai > aj
		}
		return li < lj
	})
}

func readEventsFromSource(source eventSource, query EventQuery, result *EventResult, limit int) error {
	file, err := os.Open(source.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			result.MalformedLines++
			continue
		}
		normalizeDashboardEvent(&event)
		parsed, _ := time.Parse(time.RFC3339, event.Timestamp)
		record := EventRecord{
			ID:         source.lineID(lineNo),
			Line:       lineNo,
			Event:      event,
			Raw:        append(json.RawMessage(nil), line...),
			Parsed:     parsed,
			WazuhLevel: WazuhLevel(event.Event.Action),
		}
		if !matchesQuery(record, query) {
			continue
		}
		result.TotalMatched++
		result.Events = append(result.Events, record)
		if !query.NoLimit {
			sort.SliceStable(result.Events, func(i, j int) bool {
				if result.Events[i].Parsed.Equal(result.Events[j].Parsed) {
					return result.Events[i].ID > result.Events[j].ID
				}
				return result.Events[i].Parsed.After(result.Events[j].Parsed)
			})
			if len(result.Events) > limit {
				result.Events = result.Events[:limit]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func FindEvent(path, id string) (EventRecord, bool, error) {
	archiveNum, lineNo, ok := parseRecordID(id)
	if !ok {
		return EventRecord{}, false, nil
	}
	source, found := findSource(eventSources(path), archiveNum)
	if !found {
		return EventRecord{}, false, nil
	}
	file, err := os.Open(source.path)
	if err != nil {
		if os.IsNotExist(err) {
			return EventRecord{}, false, nil
		}
		return EventRecord{}, false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	currentLine := 0
	for scanner.Scan() {
		currentLine++
		if currentLine != lineNo {
			continue
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return EventRecord{}, false, nil
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			return EventRecord{}, false, nil
		}
		normalizeDashboardEvent(&event)
		parsed, _ := time.Parse(time.RFC3339, event.Timestamp)
		return EventRecord{
			ID:         source.lineID(lineNo),
			Line:       lineNo,
			Event:      event,
			Raw:        append(json.RawMessage(nil), line...),
			Parsed:     parsed,
			WazuhLevel: WazuhLevel(event.Event.Action),
		}, true, nil
	}
	if err := scanner.Err(); err != nil {
		return EventRecord{}, false, err
	}
	return EventRecord{}, false, nil
}

type eventSource struct {
	path    string
	archive int
}

func (s eventSource) lineID(line int) string {
	if s.archive == 0 {
		return fmt.Sprintf("line-%d", line)
	}
	return fmt.Sprintf("archive-%d-line-%d", s.archive, line)
}

func eventSources(path string) []eventSource {
	sources := []eventSource{{path: path}}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return sources
	}
	prefix := base + "."
	var archives []eventSource
	for _, entry := range entries {
		name := entry.Name()
		suffix, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index <= 0 {
			continue
		}
		archives = append(archives, eventSource{path: filepath.Join(dir, name), archive: index})
	}
	sort.Slice(archives, func(i, j int) bool {
		return archives[i].archive < archives[j].archive
	})
	return append(sources, archives...)
}

func findSource(sources []eventSource, archive int) (eventSource, bool) {
	for _, s := range sources {
		if s.archive == archive {
			return s, true
		}
	}
	return eventSource{}, false
}

func normalizeDashboardEvent(event *schema.Event) {
	metricName := metricEventName(event)
	if event.Event.Category == "" && metricName != "" {
		event.Event.Category = "metric"
	}
	if event.Event.Category == "" {
		event.Event.Category = inferEventCategory(event.Event.Action)
	}
	metricName = metricEventName(event)
	if event.Event.Category == "metric" && (event.Event.Action == "" || event.Event.Action == "metric.observed") {
		event.Event.Action = metricName
		if event.Event.Action == "" {
			event.Event.Action = "metric.observed"
		}
	}
}

func metricEventName(event *schema.Event) string {
	if event == nil {
		return ""
	}
	if event.Raw != nil {
		if value, ok := event.Raw["metric_name"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if event.Event.Category == "metric" {
		return strings.TrimSpace(event.Message)
	}
	return ""
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultEventLimit
	}
	if limit > maxEventLimit {
		return maxEventLimit
	}
	return limit
}

func matchesQuery(record EventRecord, query EventQuery) bool {
	event := record.Event
	if !query.Since.IsZero() {
		if record.Parsed.IsZero() || record.Parsed.Before(query.Since) {
			return false
		}
	}
	if !query.Until.IsZero() {
		if record.Parsed.IsZero() || record.Parsed.After(query.Until) {
			return false
		}
	}
	if query.Harness != "" && !strings.EqualFold(event.Harness.Name, query.Harness) {
		return false
	}
	if query.Model != "" && !containsFold(event.Model, query.Model) {
		return false
	}
	if query.Action != "" && !strings.EqualFold(event.Event.Action, query.Action) {
		return false
	}
	if query.Severity != "" && !strings.EqualFold(string(event.Severity), query.Severity) {
		return false
	}
	if query.Category != "" && !strings.EqualFold(event.Event.Category, query.Category) {
		return false
	}
	if query.Repository != "" && !containsFold(event.Repository, query.Repository) {
		return false
	}
	if query.Session != "" {
		if event.Session == nil || !containsFold(event.Session.ID, query.Session) {
			return false
		}
	}
	if query.Trace != "" {
		if event.Trace == nil || (!containsFold(event.Trace.ID, query.Trace) && !containsFold(event.Trace.SpanID, query.Trace) && !containsFold(event.Trace.ParentSpanID, query.Trace)) {
			return false
		}
	}
	if query.File != "" {
		if event.File == nil || !containsFold(event.File.Path, query.File) {
			return false
		}
	}
	if query.Command != "" && !matchesCommand(event, query.Command) {
		return false
	}
	if query.MCP != "" {
		if event.MCP == nil || (!containsFold(event.MCP.Server, query.MCP) && !containsFold(event.MCP.Tool, query.MCP)) {
			return false
		}
	}
	if query.Approval != "" {
		if event.Approval == nil || (!containsFold(event.Approval.Decision, query.Approval) && !containsFold(event.Approval.Reason, query.Approval)) {
			return false
		}
	}
	if query.Decision != "" {
		if !matchesDecision(event, query.Decision) {
			return false
		}
	}
	if query.Policy != "" {
		if event.Policy == nil || (!containsFold(event.Policy.ID, query.Policy) && !containsFold(event.Policy.Name, query.Policy) && !containsFold(event.Policy.Decision, query.Policy) && !containsFold(event.Policy.Reason, query.Policy)) {
			return false
		}
	}
	if query.Review != "" && truthy(query.Review) && !isNeedsReview(record) {
		return false
	}
	if query.WazuhLevel != "" && !strings.EqualFold(strconv.Itoa(record.WazuhLevel), query.WazuhLevel) {
		return false
	}
	if query.Q != "" && !matchesFreeText(record, query.Q) {
		return false
	}
	return true
}

func containsFold(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(strings.TrimSpace(needle)))
}

func matchesDecision(event schema.Event, decision string) bool {
	if event.Approval != nil && containsFold(event.Approval.Decision, decision) {
		return true
	}
	if event.Policy != nil && containsFold(event.Policy.Decision, decision) {
		return true
	}
	return false
}

func matchesCommand(event schema.Event, command string) bool {
	if event.Command != nil && containsFold(event.Command.Command, command) {
		return true
	}
	if event.Tool != nil && (containsFold(event.Tool.Name, command) || containsFold(event.Tool.Command, command)) {
		return true
	}
	return false
}

func inferEventCategory(action string) string {
	switch {
	case strings.HasPrefix(action, "prompt."):
		return "prompt"
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	case strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return "approval"
	case strings.HasPrefix(action, "metric."):
		return "metric"
	case strings.HasPrefix(action, "tool."):
		return "tool"
	default:
		return ""
	}
}

func matchesFreeText(record EventRecord, query string) bool {
	haystack := strings.ToLower(strings.Join(searchFields(record), "\x00"))
	for _, term := range strings.Fields(strings.ToLower(strings.TrimSpace(query))) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func searchFields(record EventRecord) []string {
	event := record.Event
	fields := []string{
		record.ID,
		strconv.Itoa(record.Line),
		strconv.Itoa(record.WazuhLevel),
		event.Timestamp,
		event.Event.Kind,
		event.Event.Action,
		event.Event.Category,
		string(event.Severity),
		event.Endpoint.Hostname,
		event.Endpoint.OS,
		event.Endpoint.AgentVersion,
		event.User.Name,
		event.User.UID,
		event.Harness.Name,
		event.Harness.Version,
		event.Harness.ExecutablePath,
		event.Harness.ConfigPath,
		event.Model,
		event.Repository,
		event.Branch,
		event.Message,
	}
	if event.Session != nil {
		fields = append(fields, event.Session.ID, event.Session.WorkingDirectory)
	}
	if event.Trace != nil {
		fields = append(fields, event.Trace.ID, event.Trace.SpanID, event.Trace.ParentSpanID)
	}
	if event.Tool != nil {
		fields = append(fields, event.Tool.Name, event.Tool.Command, event.Tool.Path)
	}
	if event.File != nil {
		fields = append(fields, event.File.Path, event.File.Operation, event.File.Language, event.File.DiffHash, strconv.Itoa(event.File.DiffBytes))
	}
	if event.Command != nil {
		fields = append(fields, event.Command.Command, strconv.FormatInt(event.Command.DurationMS, 10))
		if event.Command.ExitCode != nil {
			fields = append(fields, strconv.Itoa(*event.Command.ExitCode))
		}
	}
	if event.MCP != nil {
		fields = append(fields, event.MCP.Server, event.MCP.Tool)
	}
	if event.Approval != nil {
		fields = append(fields, event.Approval.Decision, event.Approval.Reason)
	}
	if event.Policy != nil {
		fields = append(fields, event.Policy.ID, event.Policy.Name, event.Policy.Decision, event.Policy.Enforcement, event.Policy.Reason)
	}
	if event.Prompt != nil {
		fields = append(fields, event.Prompt.Text)
	}
	if event.Content != nil {
		fields = append(fields, event.Content.Retention)
		if event.Content.Included {
			fields = append(fields, "content included")
		}
		if event.Content.Redacted {
			fields = append(fields, "redacted")
		}
		if event.Content.Truncated {
			fields = append(fields, "truncated")
		}
	}
	if event.Destination != nil {
		fields = append(fields, event.Destination.Type, event.Destination.Mode, event.Destination.Status)
	}
	if event.Health != nil {
		fields = append(fields, event.Health.Component, event.Health.Status, event.Health.Reason)
	}
	if event.Truncated {
		fields = append(fields, "truncated")
	}
	return fields
}

func activeFilters(query EventQuery) map[string]string {
	filters := map[string]string{}
	add := func(key, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			filters[key] = value
		}
	}
	add("harness", query.Harness)
	add("model", query.Model)
	add("action", query.Action)
	add("severity", query.Severity)
	add("category", query.Category)
	add("repository", query.Repository)
	add("session", query.Session)
	add("trace", query.Trace)
	add("file", query.File)
	add("command", query.Command)
	add("mcp", query.MCP)
	add("approval", query.Approval)
	add("decision", query.Decision)
	add("policy", query.Policy)
	add("review", query.Review)
	add("wazuh_level", query.WazuhLevel)
	if !query.Since.IsZero() {
		filters["since"] = query.Since.Format(time.RFC3339)
	}
	if !query.Until.IsZero() {
		filters["until"] = query.Until.Format(time.RFC3339)
	}
	if len(filters) == 0 {
		return nil
	}
	return filters
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "review", "needs_review":
		return true
	default:
		return false
	}
}

func parseRecordID(id string) (int, int, bool) {
	if archiveID, rest, ok := strings.Cut(strings.TrimPrefix(id, "archive-"), "-line-"); ok && strings.HasPrefix(id, "archive-") {
		archive, err := strconv.Atoi(archiveID)
		if err != nil || archive <= 0 {
			return 0, 0, false
		}
		lineNo, err := strconv.Atoi(rest)
		if err != nil || lineNo <= 0 {
			return 0, 0, false
		}
		return archive, lineNo, true
	}
	line, ok := strings.CutPrefix(id, "line-")
	if !ok {
		return 0, 0, false
	}
	lineNo, err := strconv.Atoi(line)
	if err != nil || lineNo <= 0 {
		return 0, 0, false
	}
	return 0, lineNo, true
}

func WazuhLevel(action string) int {
	switch action {
	case "endpoint.tamper_detected", "endpoint.health_failed":
		return 12
	case "approval.denied", "policy.blocked":
		return 10
	case "tool.failed":
		return 9
	case "command.executed", "mcp.tool_invoked":
		return 7
	case "telemetry.disabled", "telemetry.misconfigured", "prompt.submitted", "tool.invoked", "tool.completed", "file.read", "file.modified":
		return 5
	case "":
		return 0
	default:
		return 3
	}
}
