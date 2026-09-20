package dashboard

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

type SessionRecord struct {
	ID             string `json:"id"`
	FirstEventAt   string `json:"first_event_at,omitempty"`
	LastEventAt    string `json:"last_event_at,omitempty"`
	EventCount     int    `json:"event_count"`
	ReviewCount    int    `json:"review_count"`
	MaxSeverity    string `json:"max_severity,omitempty"`
	Harness        string `json:"harness,omitempty"`
	Model          string `json:"model,omitempty"`
	Repository     string `json:"repository,omitempty"`
	Branch         string `json:"branch,omitempty"`
	WorkingDir     string `json:"working_directory,omitempty"`
	OriginalPrompt string `json:"original_prompt,omitempty"`
	LastAction     string `json:"last_action,omitempty"`
	LastMessage    string `json:"last_message,omitempty"`
	LastArtifact   string `json:"last_artifact,omitempty"`
	CommandCount   int    `json:"command_count"`
	FileCount      int    `json:"file_count"`
	MCPCount       int    `json:"mcp_count"`
	ApprovalCount  int    `json:"approval_count"`
	PromptCount    int    `json:"prompt_count"`
	ToolCount      int    `json:"tool_count"`
}

type SessionResult struct {
	Sessions       []SessionRecord   `json:"sessions"`
	EventsMatched  int               `json:"events_matched"`
	TotalSessions  int               `json:"total_sessions"`
	MalformedLines int               `json:"malformed_lines"`
	Limit          int               `json:"limit"`
	Query          string            `json:"query,omitempty"`
	Filters        map[string]string `json:"filters,omitempty"`
	Returned       int               `json:"returned"`
	Truncated      bool              `json:"truncated"`
}

type SessionEventRecord struct {
	ID         string          `json:"id"`
	Line       int             `json:"line"`
	Event      schema.Event    `json:"event"`
	RawEvent   json.RawMessage `json:"raw_event,omitempty"`
	Parsed     time.Time       `json:"parsed_timestamp,omitempty"`
	WazuhLevel int             `json:"wazuh_level,omitempty"`
}

type SessionDetail struct {
	Session SessionRecord        `json:"session"`
	Events  []SessionEventRecord `json:"events"`
}

func ReadSessions(path string, query EventQuery) (SessionResult, error) {
	limit := normalizeLimit(query.Limit)
	query.NoLimit = true
	events, err := ReadEvents(path, query)
	if err != nil {
		return SessionResult{}, err
	}
	SortRecordsAppendOrder(events.Events)
	byID := map[string]*sessionAccumulator{}
	for _, record := range events.Events {
		id := sessionID(record.Event)
		if id == "" {
			continue
		}
		acc := byID[id]
		if acc == nil {
			acc = &sessionAccumulator{record: SessionRecord{ID: id}}
			byID[id] = acc
		}
		acc.add(record)
	}
	sessions := make([]SessionRecord, 0, len(byID))
	for _, acc := range byID {
		sessions = append(sessions, acc.record)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		li := parseSessionTime(sessions[i].LastEventAt)
		lj := parseSessionTime(sessions[j].LastEventAt)
		if !li.Equal(lj) {
			return li.After(lj)
		}
		return sessions[i].ID > sessions[j].ID
	})
	total := len(sessions)
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return SessionResult{
		Sessions:       sessions,
		EventsMatched:  events.TotalMatched,
		TotalSessions:  total,
		MalformedLines: events.MalformedLines,
		Limit:          limit,
		Query:          events.Query,
		Filters:        events.Filters,
		Returned:       len(sessions),
		Truncated:      total > len(sessions),
	}, nil
}

func ReadSessionDetail(path, id string) (SessionDetail, bool, error) {
	result, err := ReadEvents(path, EventQuery{Session: id, NoLimit: true})
	if err != nil {
		return SessionDetail{}, false, err
	}
	records := make([]EventRecord, 0, len(result.Events))
	for _, record := range result.Events {
		if sessionID(record.Event) == id {
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		return SessionDetail{}, false, nil
	}
	SortRecordsAppendOrder(records)
	acc := &sessionAccumulator{record: SessionRecord{ID: id}}
	events := make([]SessionEventRecord, 0, len(records))
	for _, record := range records {
		acc.add(record)
		events = append(events, SessionEventRecord{
			ID:         record.ID,
			Line:       record.Line,
			Event:      record.Event,
			RawEvent:   append(json.RawMessage(nil), record.Raw...),
			Parsed:     record.Parsed,
			WazuhLevel: record.WazuhLevel,
		})
	}
	return SessionDetail{Session: acc.record, Events: events}, true, nil
}

type sessionAccumulator struct {
	record SessionRecord
	first  time.Time
	last   time.Time
}

func (acc *sessionAccumulator) add(record EventRecord) {
	event := record.Event
	acc.record.EventCount++
	if isNeedsReview(record) {
		acc.record.ReviewCount++
	}
	acc.record.MaxSeverity = maxSeverity(acc.record.MaxSeverity, string(event.Severity))
	if event.Harness.Name != "" && acc.record.Harness == "" {
		acc.record.Harness = event.Harness.Name
	}
	if event.Model != "" && acc.record.Model == "" {
		acc.record.Model = event.Model
	}
	if event.Repository != "" && acc.record.Repository == "" {
		acc.record.Repository = event.Repository
	}
	if event.Branch != "" && acc.record.Branch == "" {
		acc.record.Branch = event.Branch
	}
	if event.Session != nil && event.Session.WorkingDirectory != "" && acc.record.WorkingDir == "" {
		acc.record.WorkingDir = event.Session.WorkingDirectory
	}
	if acc.record.OriginalPrompt == "" && isPromptEvent(event) {
		acc.record.OriginalPrompt = promptText(event)
	}
	switch event.Event.Category {
	case "prompt":
		acc.record.PromptCount++
	case "tool":
		acc.record.ToolCount++
	case "command":
		acc.record.CommandCount++
	case "file":
		acc.record.FileCount++
	case "mcp":
		acc.record.MCPCount++
	case "approval":
		acc.record.ApprovalCount++
	}
	if record.Parsed.IsZero() {
		return
	}
	if acc.first.IsZero() || record.Parsed.Before(acc.first) {
		acc.first = record.Parsed
		acc.record.FirstEventAt = event.Timestamp
	}
	if acc.last.IsZero() || record.Parsed.After(acc.last) || record.Parsed.Equal(acc.last) {
		acc.last = record.Parsed
		acc.record.LastEventAt = event.Timestamp
		acc.record.LastAction = event.Event.Action
		acc.record.LastMessage = event.Message
		acc.record.LastArtifact = sessionArtifact(event)
	}
}

func sessionID(event schema.Event) string {
	if event.Session == nil {
		return ""
	}
	return event.Session.ID
}

func sessionArtifact(event schema.Event) string {
	if event.Prompt != nil && event.Prompt.Text != "" {
		return event.Prompt.Text
	}
	if event.Command != nil && event.Command.Command != "" {
		return event.Command.Command
	}
	if event.File != nil && event.File.Path != "" {
		return event.File.Path
	}
	if event.MCP != nil && (event.MCP.Server != "" || event.MCP.Tool != "") {
		if event.MCP.Server == "" {
			return event.MCP.Tool
		}
		if event.MCP.Tool == "" {
			return event.MCP.Server
		}
		return event.MCP.Server + " / " + event.MCP.Tool
	}
	if event.Tool != nil && event.Tool.Name != "" {
		return event.Tool.Name
	}
	return ""
}

func promptText(event schema.Event) string {
	if event.Prompt != nil && event.Prompt.Text != "" {
		return event.Prompt.Text
	}
	if text := rawString(event.Raw, "first_prompt"); text != "" {
		return text
	}
	if event.Event.Action == "prompt.submitted" {
		return event.Message
	}
	return ""
}

func maxSeverity(a, b string) string {
	if severityRank(b) > severityRank(a) {
		return b
	}
	return a
}

func severityRank(value string) int {
	switch value {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func parseSessionTime(value string) time.Time {
	parsed, _ := schema.ParseTimestamp(value)
	return parsed
}
