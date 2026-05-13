package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
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
	ID     string          `json:"id"`
	Line   int             `json:"line"`
	Event  schema.Event    `json:"event"`
	Raw    json.RawMessage `json:"raw"`
	Parsed time.Time       `json:"parsed_timestamp,omitempty"`
}

type EventQuery struct {
	Limit      int
	Since      time.Time
	Harness    string
	Action     string
	Repository string
	Session    string
	File       string
	Command    string
}

type EventResult struct {
	Events         []EventRecord `json:"events"`
	TotalMatched   int           `json:"total_matched"`
	MalformedLines int           `json:"malformed_lines"`
	Limit          int           `json:"limit"`
}

func ReadEvents(path string, query EventQuery) (EventResult, error) {
	limit := normalizeLimit(query.Limit)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EventResult{Events: []EventRecord{}, Limit: limit}, nil
		}
		return EventResult{}, err
	}
	defer file.Close()

	result := EventResult{Limit: limit}
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
		parsed, _ := time.Parse(time.RFC3339, event.Timestamp)
		record := EventRecord{
			ID:     fmt.Sprintf("line-%d", lineNo),
			Line:   lineNo,
			Event:  event,
			Raw:    append(json.RawMessage(nil), line...),
			Parsed: parsed,
		}
		if !matchesQuery(record, query) {
			continue
		}
		result.TotalMatched++
		result.Events = append(result.Events, record)
		if len(result.Events) > limit {
			copy(result.Events, result.Events[1:])
			result.Events = result.Events[:limit]
		}
	}
	if err := scanner.Err(); err != nil {
		return EventResult{}, err
	}

	sort.SliceStable(result.Events, func(i, j int) bool {
		return result.Events[i].Line > result.Events[j].Line
	})
	return result, nil
}

func FindEvent(path, id string) (EventRecord, bool, error) {
	targetLine := 0
	if strings.HasPrefix(id, "line-") {
		n, err := strconv.Atoi(strings.TrimPrefix(id, "line-"))
		if err == nil && n > 0 {
			targetLine = n
		}
	}
	if targetLine == 0 {
		return EventRecord{}, false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EventRecord{}, false, nil
		}
		return EventRecord{}, false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo != targetLine {
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
		parsed, _ := time.Parse(time.RFC3339, event.Timestamp)
		record := EventRecord{
			ID:     id,
			Line:   lineNo,
			Event:  event,
			Raw:    append(json.RawMessage(nil), line...),
			Parsed: parsed,
		}
		return record, true, nil
	}
	if err := scanner.Err(); err != nil {
		return EventRecord{}, false, err
	}
	return EventRecord{}, false, nil
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
	if query.Harness != "" && !strings.EqualFold(event.Harness.Name, query.Harness) {
		return false
	}
	if query.Action != "" && !strings.EqualFold(event.Event.Action, query.Action) {
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
	if query.File != "" {
		if event.File == nil || !containsFold(event.File.Path, query.File) {
			return false
		}
	}
	if query.Command != "" {
		if event.Command != nil && containsFold(event.Command.Command, query.Command) {
			return true
		}
		if event.Tool != nil && (containsFold(event.Tool.Name, query.Command) || containsFold(event.Tool.Command, query.Command)) {
			return true
		}
		return false
	}
	return true
}

func containsFold(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}
