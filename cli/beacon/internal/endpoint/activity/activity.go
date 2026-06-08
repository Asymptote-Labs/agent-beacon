package activity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
)

const DefaultLimit = 100

type Query struct {
	LogPath    string
	Limit      int
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
	File       string
	Command    string
	MCP        string
	Approval   string
	Decision   string
	Policy     string
	Review     string
	WazuhLevel string
}

type ResultMeta struct {
	TotalMatched   int               `json:"total_matched"`
	MalformedLines int               `json:"malformed_lines"`
	Limit          int               `json:"limit"`
	Returned       int               `json:"returned"`
	Truncated      bool              `json:"truncated"`
	Query          string            `json:"query,omitempty"`
	Filters        map[string]string `json:"filters,omitempty"`
	Caveats        []string          `json:"caveats,omitempty"`
}

type SearchResult struct {
	Meta   ResultMeta     `json:"meta"`
	Events []EventSummary `json:"events"`
}

type SummaryResult struct {
	Meta    ResultMeta `json:"meta"`
	Summary Summary    `json:"summary"`
}

type EventResult struct {
	Found bool          `json:"found"`
	Event *EventSummary `json:"event,omitempty"`
}

type FilterValuesResult struct {
	Meta    ResultMeta              `json:"meta"`
	Filters map[string][]ValueCount `json:"filters"`
}

type ValueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Summary struct {
	TotalEvents              int            `json:"total_events"`
	LastEventTime            string         `json:"last_event_time,omitempty"`
	ActiveSessions           int            `json:"active_sessions"`
	MalformedLines           int            `json:"malformed_lines"`
	CountsByAction           map[string]int `json:"counts_by_action"`
	CountsByHarness          map[string]int `json:"counts_by_harness"`
	CountsBySeverity         map[string]int `json:"counts_by_severity"`
	CountsByCategory         map[string]int `json:"counts_by_category"`
	CountsByApprovalDecision map[string]int `json:"counts_by_approval_decision"`
	CountsByMCPServer        map[string]int `json:"counts_by_mcp_server"`
	CountsByRepository       map[string]int `json:"counts_by_repository"`
	CountsByModel            map[string]int `json:"counts_by_model"`
	PromptEvents             int            `json:"prompt_events"`
	ToolEvents               int            `json:"tool_events"`
	CommandEvents            int            `json:"command_events"`
	FileEvents               int            `json:"file_events"`
	MCPEvents                int            `json:"mcp_events"`
	ApprovalEvents           int            `json:"approval_events"`
	HighSeverityEvents       int            `json:"high_severity_events"`
	CriticalSeverityEvents   int            `json:"critical_severity_events"`
	NeedsReviewEvents        int            `json:"needs_review_events"`
	FailedToolEvents         int            `json:"failed_tool_events"`
	DeniedApprovalEvents     int            `json:"denied_approval_events"`
	PolicyBlockedEvents      int            `json:"policy_blocked_events"`
	TopActions               []Count        `json:"top_actions"`
	TopHarnesses             []Count        `json:"top_harnesses"`
	TopModels                []Count        `json:"top_models"`
	TopRepositories          []Count        `json:"top_repositories"`
	TopMCPServers            []Count        `json:"top_mcp_servers"`
}

type EventSummary struct {
	ID         string           `json:"id"`
	Timestamp  string           `json:"timestamp"`
	Harness    string           `json:"harness,omitempty"`
	Action     string           `json:"action,omitempty"`
	Category   string           `json:"category,omitempty"`
	Severity   string           `json:"severity,omitempty"`
	Message    string           `json:"message,omitempty"`
	Session    *SessionSummary  `json:"session,omitempty"`
	Tool       *ToolSummary     `json:"tool,omitempty"`
	File       *FileSummary     `json:"file,omitempty"`
	Command    *CommandSummary  `json:"command,omitempty"`
	MCP        *MCPSummary      `json:"mcp,omitempty"`
	Approval   *ApprovalSummary `json:"approval,omitempty"`
	Policy     *PolicySummary   `json:"policy,omitempty"`
	Content    *ContentSummary  `json:"content,omitempty"`
	Model      string           `json:"model,omitempty"`
	Repository string           `json:"repository,omitempty"`
	Branch     string           `json:"branch,omitempty"`
	WazuhLevel int              `json:"wazuh_level,omitempty"`
	Caveats    []string         `json:"caveats,omitempty"`
}

type SessionSummary struct {
	ID               string `json:"id,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type ToolSummary struct {
	Name    string `json:"name,omitempty"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
}

type FileSummary struct {
	Path      string `json:"path,omitempty"`
	Operation string `json:"operation,omitempty"`
	Language  string `json:"language,omitempty"`
	DiffHash  string `json:"diff_hash,omitempty"`
	DiffBytes int    `json:"diff_bytes,omitempty"`
}

type CommandSummary struct {
	Command    string `json:"command,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type MCPSummary struct {
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
}

type ApprovalSummary struct {
	Required bool   `json:"required,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type PolicySummary struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Decision    string `json:"decision,omitempty"`
	Enforcement string `json:"enforcement,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ContentSummary struct {
	Retention      string `json:"retention,omitempty"`
	Included       bool   `json:"included"`
	Redacted       bool   `json:"redacted,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	FieldTruncated bool   `json:"field_truncated,omitempty"`
}

func Search(query Query) (SearchResult, error) {
	result, err := read(query)
	if err != nil {
		return SearchResult{}, err
	}
	events := make([]EventSummary, 0, len(result.Events))
	for _, record := range result.Events {
		events = append(events, summarizeRecord(record))
	}
	return SearchResult{Meta: metaFromResult(result), Events: events}, nil
}

func Summarize(query Query) (SummaryResult, error) {
	result, err := read(query)
	if err != nil {
		return SummaryResult{}, err
	}
	return SummaryResult{Meta: metaFromResult(result), Summary: activitySummary(dashboard.BuildSummary(result))}, nil
}

func GetEvent(logPath, id string) (EventResult, error) {
	if logPath == "" {
		return EventResult{}, fmt.Errorf("runtime log path is required")
	}
	record, ok, err := dashboard.FindEvent(logPath, id)
	if err != nil {
		return EventResult{}, err
	}
	if !ok {
		return EventResult{Found: false}, nil
	}
	event := summarizeRecord(record)
	return EventResult{Found: true, Event: &event}, nil
}

func ListFilters(query Query) (FilterValuesResult, error) {
	result, err := read(query)
	if err != nil {
		return FilterValuesResult{}, err
	}
	counts := map[string]map[string]int{
		"actions":      {},
		"categories":   {},
		"harnesses":    {},
		"models":       {},
		"repositories": {},
		"sessions":     {},
		"mcp_servers":  {},
		"mcp_tools":    {},
		"commands":     {},
		"files":        {},
	}
	for _, record := range result.Events {
		event := record.Event
		add(counts["actions"], event.Event.Action)
		add(counts["categories"], event.Event.Category)
		add(counts["harnesses"], event.Harness.Name)
		add(counts["models"], event.Model)
		add(counts["repositories"], event.Repository)
		if event.Session != nil {
			add(counts["sessions"], event.Session.ID)
		}
		if event.MCP != nil {
			add(counts["mcp_servers"], event.MCP.Server)
			add(counts["mcp_tools"], event.MCP.Tool)
		}
		if event.Command != nil {
			add(counts["commands"], event.Command.Command)
		}
		if event.Tool != nil {
			add(counts["commands"], event.Tool.Command)
		}
		if event.File != nil {
			add(counts["files"], event.File.Path)
		}
	}
	filters := make(map[string][]ValueCount, len(counts))
	for name, values := range counts {
		filters[name] = topValues(values, 20)
	}
	meta := metaFromResult(result)
	if result.Truncated {
		meta.Caveats = append(meta.Caveats, "filter values are based on the returned event window")
	}
	return FilterValuesResult{Meta: meta, Filters: filters}, nil
}

func InspectLog(logPath string) (sampledEvents, malformedLines int, archives []string, err error) {
	result, err := read(Query{LogPath: logPath, Limit: 25})
	if err != nil {
		return 0, 0, nil, err
	}
	dir := filepath.Dir(logPath)
	base := filepath.Base(logPath)
	entries, readErr := os.ReadDir(dir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return 0, 0, nil, readErr
	}
	prefix := base + "."
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			archives = append(archives, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(archives)
	return len(result.Events), result.MalformedLines, archives, nil
}

func read(query Query) (dashboard.EventResult, error) {
	if query.LogPath == "" {
		return dashboard.EventResult{}, fmt.Errorf("runtime log path is required")
	}
	return dashboard.ReadEvents(query.LogPath, dashboard.EventQuery{
		Limit:      limitOrDefault(query.Limit),
		Since:      query.Since,
		Until:      query.Until,
		Q:          query.Q,
		Harness:    query.Harness,
		Model:      query.Model,
		Action:     query.Action,
		Severity:   query.Severity,
		Category:   query.Category,
		Repository: query.Repository,
		Session:    query.Session,
		File:       query.File,
		Command:    query.Command,
		MCP:        query.MCP,
		Approval:   query.Approval,
		Decision:   query.Decision,
		Policy:     query.Policy,
		Review:     query.Review,
		WazuhLevel: query.WazuhLevel,
	})
}

func limitOrDefault(limit int) int {
	if limit == 0 {
		return DefaultLimit
	}
	return limit
}

func metaFromResult(result dashboard.EventResult) ResultMeta {
	meta := ResultMeta{
		TotalMatched:   result.TotalMatched,
		MalformedLines: result.MalformedLines,
		Limit:          result.Limit,
		Returned:       result.Returned,
		Truncated:      result.Truncated,
		Query:          result.Query,
		Filters:        result.Filters,
	}
	if result.Truncated {
		meta.Caveats = append(meta.Caveats, "result set truncated by limit")
	}
	if result.MalformedLines > 0 {
		meta.Caveats = append(meta.Caveats, "one or more malformed log lines were skipped")
	}
	return meta
}

func summarizeRecord(record dashboard.EventRecord) EventSummary {
	event := record.Event
	summary := EventSummary{
		ID:         record.ID,
		Timestamp:  event.Timestamp,
		Harness:    event.Harness.Name,
		Action:     event.Event.Action,
		Category:   event.Event.Category,
		Severity:   string(event.Severity),
		Message:    event.Message,
		Model:      event.Model,
		Repository: event.Repository,
		Branch:     event.Branch,
		WazuhLevel: record.WazuhLevel,
	}
	if event.Session != nil {
		summary.Session = &SessionSummary{ID: event.Session.ID, WorkingDirectory: event.Session.WorkingDirectory}
	}
	if event.Tool != nil {
		summary.Tool = &ToolSummary{Name: event.Tool.Name, Command: event.Tool.Command, Path: event.Tool.Path}
	}
	if event.File != nil {
		summary.File = &FileSummary{Path: event.File.Path, Operation: event.File.Operation, Language: event.File.Language, DiffHash: event.File.DiffHash, DiffBytes: event.File.DiffBytes}
	}
	if event.Command != nil {
		summary.Command = &CommandSummary{Command: event.Command.Command, ExitCode: event.Command.ExitCode, DurationMS: event.Command.DurationMS}
	}
	if event.MCP != nil {
		summary.MCP = &MCPSummary{Server: event.MCP.Server, Tool: event.MCP.Tool}
	}
	if event.Approval != nil {
		summary.Approval = &ApprovalSummary{Required: event.Approval.Required, Decision: event.Approval.Decision, Reason: event.Approval.Reason}
	}
	if event.Policy != nil {
		summary.Policy = &PolicySummary{ID: event.Policy.ID, Name: event.Policy.Name, Decision: event.Policy.Decision, Enforcement: event.Policy.Enforcement, Reason: event.Policy.Reason}
	}
	if event.Truncated {
		summary.Content = contentSummary(event.Truncated)
		summary.Caveats = append(summary.Caveats, contentCaveats(summary.Content)...)
	}
	return summary
}

func contentSummary(fieldTruncated bool) *ContentSummary {
	summary := &ContentSummary{FieldTruncated: fieldTruncated}
	return summary
}

func contentCaveats(content *ContentSummary) []string {
	if content == nil {
		return nil
	}
	var caveats []string
	if content.Redacted {
		caveats = append(caveats, "content was redacted")
	}
	if content.Truncated || content.FieldTruncated {
		caveats = append(caveats, "content was truncated")
	}
	return caveats
}

func activitySummary(summary dashboard.Summary) Summary {
	return Summary{
		TotalEvents:              summary.TotalEvents,
		LastEventTime:            summary.LastEventTime,
		ActiveSessions:           summary.ActiveSessions,
		MalformedLines:           summary.MalformedLines,
		CountsByAction:           summary.CountsByAction,
		CountsByHarness:          summary.CountsByHarness,
		CountsBySeverity:         summary.CountsBySeverity,
		CountsByCategory:         summary.CountsByCategory,
		CountsByApprovalDecision: summary.CountsByApprovalDecision,
		CountsByMCPServer:        summary.CountsByMCPServer,
		CountsByRepository:       summary.CountsByRepository,
		CountsByModel:            summary.CountsByModel,
		PromptEvents:             summary.PromptEvents,
		ToolEvents:               summary.ToolEvents,
		CommandEvents:            summary.CommandEvents,
		FileEvents:               summary.FileEvents,
		MCPEvents:                summary.MCPEvents,
		ApprovalEvents:           summary.ApprovalEvents,
		HighSeverityEvents:       summary.HighSeverityEvents,
		CriticalSeverityEvents:   summary.CriticalSeverityEvents,
		NeedsReviewEvents:        summary.NeedsReviewEvents,
		FailedToolEvents:         summary.FailedToolEvents,
		DeniedApprovalEvents:     summary.DeniedApprovalEvents,
		PolicyBlockedEvents:      summary.PolicyBlockedEvents,
		TopActions:               activityCounts(summary.TopActions),
		TopHarnesses:             activityCounts(summary.TopHarnesses),
		TopModels:                activityCounts(summary.TopModels),
		TopRepositories:          activityCounts(summary.TopRepositories),
		TopMCPServers:            activityCounts(summary.TopMCPServers),
	}
}

func activityCounts(counts []dashboard.Count) []Count {
	out := make([]Count, 0, len(counts))
	for _, count := range counts {
		out = append(out, Count{Name: count.Name, Count: count.Count})
	}
	return out
}

func add(counts map[string]int, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		counts[value]++
	}
}

func topValues(counts map[string]int, limit int) []ValueCount {
	values := make([]ValueCount, 0, len(counts))
	for value, count := range counts {
		values = append(values, ValueCount{Value: value, Count: count})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Count == values[j].Count {
			return values[i].Value < values[j].Value
		}
		return values[i].Count > values[j].Count
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}
