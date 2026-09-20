package dashboard

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const traceStoreFile = "traces.db"

type traceStore struct {
	dbPath  string
	logPath string
}

type traceStoreStatus struct {
	Path      string `json:"path"`
	Traces    int    `json:"traces"`
	Events    int    `json:"events"`
	IndexRows int    `json:"index_rows"`
	IndexedAt string `json:"indexed_at,omitempty"`
}

type traceSearchOptions struct {
	Literal            bool
	CaseSensitive      bool
	Sources            []string
	Tool               string
	ScanTraces         int
	ScanEventsPerTrace int
}

func TraceStorePath(logPath string) string {
	return defaultTraceStorePath(logPath)
}

func TraceStoreStatus(logPath string) (traceStoreStatus, error) {
	return openTraceStore(logPath).Status()
}

func ReindexTraceStore(logPath string) error {
	return openTraceStore(logPath).Reindex()
}

func defaultTraceStorePath(logPath string) string {
	if strings.TrimSpace(logPath) == "" {
		return traceStoreFile
	}
	dir := filepath.Dir(logPath)
	if filepath.Base(dir) == "logs" {
		return filepath.Join(filepath.Dir(dir), traceStoreFile)
	}
	return filepath.Join(dir, traceStoreFile)
}

func openTraceStore(logPath string) *traceStore {
	return &traceStore{dbPath: defaultTraceStorePath(logPath), logPath: logPath}
}

func (s *traceStore) db() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(s.dbPath))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureTraceStoreSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func sqliteURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(5000)"
}

func ensureTraceStoreSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trace_index_state (
			source_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			indexed_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS traces (
			id TEXT NOT NULL,
			source_key TEXT NOT NULL,
			summary_json TEXT NOT NULL,
			updated_at TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (source_key, id)
		)`,
		`CREATE INDEX IF NOT EXISTS traces_by_source_updated ON traces (source_key, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS trace_events (
			trace_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			event_number INTEGER NOT NULL,
			event_type TEXT,
			tool_name TEXT,
			event_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (trace_id, event_id)
		)`,
		`CREATE INDEX IF NOT EXISTS trace_events_by_trace_number ON trace_events (trace_id, event_number)`,
		`CREATE TABLE IF NOT EXISTS trace_search (
			trace_id TEXT NOT NULL,
			event_id TEXT,
			source TEXT NOT NULL,
			event_type TEXT,
			tool_name TEXT,
			event_number INTEGER,
			body TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS trace_search_by_trace_source ON trace_search (trace_id, source)`,
		`CREATE INDEX IF NOT EXISTS trace_search_by_event_type ON trace_search (event_type)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *traceStore) ensureCurrent() error {
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	fingerprint := traceLogFingerprint(s.logPath)
	var current string
	err = db.QueryRow(`SELECT fingerprint FROM trace_index_state WHERE source_key = ?`, s.logPath).Scan(&current)
	if err == nil && current == fingerprint {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return s.reindexDB(db, fingerprint)
}

func (s *traceStore) Reindex() error {
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	return s.reindexDB(db, traceLogFingerprint(s.logPath))
}

func (s *traceStore) reindexDB(db *sql.DB, fingerprint string) error {
	traces, err := readTraceAggregates(s.logPath, EventQuery{})
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM trace_search WHERE trace_id IN (SELECT id FROM traces WHERE source_key = ?)`,
		`DELETE FROM trace_events WHERE trace_id IN (SELECT id FROM traces WHERE source_key = ?)`,
		`DELETE FROM traces WHERE source_key = ?`,
	} {
		if _, err := tx.Exec(stmt, s.logPath); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, agg := range traces {
		if err := insertTraceAggregate(tx, s.logPath, agg, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO trace_index_state (source_key, fingerprint, indexed_at) VALUES (?, ?, ?)
		ON CONFLICT(source_key) DO UPDATE SET fingerprint = excluded.fingerprint, indexed_at = excluded.indexed_at`,
		s.logPath, fingerprint, now); err != nil {
		return err
	}
	return tx.Commit()
}

func insertTraceAggregate(tx *sql.Tx, sourceKey string, agg *traceAggregate, now string) error {
	summaryJSON, err := json.Marshal(agg.summary)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO traces (id, source_key, summary_json, updated_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		agg.summary.ID, sourceKey, string(summaryJSON), agg.summary.UpdatedAt, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO trace_search (trace_id, source, body) VALUES (?, 'trace', ?)`,
		agg.summary.ID, traceSummarySearchBody(agg.summary)); err != nil {
		return err
	}
	for _, event := range agg.events {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return err
		}
		toolName := ""
		if event.Tool != nil {
			toolName = event.Tool.Name
		}
		if event.Command != nil && toolName == "" {
			toolName = "command"
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO trace_events (trace_id, event_id, event_number, event_type, tool_name, event_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			agg.summary.ID, event.ID, event.Number, event.Type, toolName, string(eventJSON), now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO trace_search (trace_id, event_id, source, event_type, tool_name, event_number, body)
			VALUES (?, ?, 'event', ?, ?, ?, ?)`,
			agg.summary.ID, event.ID, event.Type, toolName, event.Number, traceEventSearchBody(event)); err != nil {
			return err
		}
	}
	return nil
}

func (s *traceStore) Status() (traceStoreStatus, error) {
	if err := s.ensureCurrent(); err != nil {
		return traceStoreStatus{}, err
	}
	db, err := s.db()
	if err != nil {
		return traceStoreStatus{}, err
	}
	defer db.Close()
	status := traceStoreStatus{Path: s.dbPath}
	_ = db.QueryRow(`SELECT COUNT(*) FROM traces WHERE source_key = ?`, s.logPath).Scan(&status.Traces)
	_ = db.QueryRow(`SELECT COUNT(*) FROM trace_events WHERE trace_id IN (SELECT id FROM traces WHERE source_key = ?)`, s.logPath).Scan(&status.Events)
	_ = db.QueryRow(`SELECT COUNT(*) FROM trace_search WHERE trace_id IN (SELECT id FROM traces WHERE source_key = ?)`, s.logPath).Scan(&status.IndexRows)
	_ = db.QueryRow(`SELECT indexed_at FROM trace_index_state WHERE source_key = ?`, s.logPath).Scan(&status.IndexedAt)
	return status, nil
}

func (s *traceStore) List(query TraceQuery) (TraceListResultV1, error) {
	if !traceStoreSupportsQuery(query) {
		return TraceListResultV1{}, errTraceStoreUnsupported
	}
	if err := s.ensureCurrent(); err != nil {
		return TraceListResultV1{}, err
	}
	summaries, err := s.loadSummaries()
	if err != nil {
		return TraceListResultV1{}, err
	}
	filtered := make([]TraceSummaryV1, 0, len(summaries))
	matchingIDs, err := s.matchingTraceIDs(query, traceSearchOptions{})
	if err != nil {
		return TraceListResultV1{}, err
	}
	for _, summary := range summaries {
		if query.State != "" && !strings.EqualFold(summary.Sharing.State, query.State) {
			continue
		}
		if query.Visibility != "" && !strings.EqualFold(summary.Sharing.Visibility, query.Visibility) {
			continue
		}
		if query.Q != "" && !matchingIDs[summary.ID] {
			continue
		}
		filtered = append(filtered, summary)
	}
	sortTraceSummaries(filtered)
	limit := normalizeLimit(query.Limit)
	page := query.Page
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * limit
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return TraceListResultV1{
		Traces:       filtered[start:end],
		TotalMatched: len(filtered),
		Returned:     end - start,
		Limit:        limit,
		Page:         page,
		Truncated:    end < len(filtered),
		Filters:      activeTraceFilters(query),
	}, nil
}

func (s *traceStore) Search(query TraceQuery, opts traceSearchOptions) (TraceSearchResultV1, error) {
	if !traceStoreSupportsQuery(query) {
		return TraceSearchResultV1{}, errTraceStoreUnsupported
	}
	if query.ResultLevel == "" {
		query.ResultLevel = "trace"
	}
	if err := s.ensureCurrent(); err != nil {
		return TraceSearchResultV1{}, err
	}
	limit := normalizeLimit(query.Limit)
	resp := TraceSearchResultV1{ResultLevel: query.ResultLevel, Limit: limit, Filters: activeTraceFilters(query)}
	switch query.ResultLevel {
	case "event":
		events, err := s.searchEvents(query, opts, limit)
		if err != nil {
			return TraceSearchResultV1{}, err
		}
		resp.Events = events.returned
		resp.TotalMatched = events.total
		resp.Returned = len(resp.Events)
	default:
		resp.ResultLevel = "trace"
		summaries, err := s.loadSummaries()
		if err != nil {
			return TraceSearchResultV1{}, err
		}
		matching, err := s.matchingTraceIDs(query, opts)
		if err != nil {
			return TraceSearchResultV1{}, err
		}
		for _, summary := range summaries {
			if query.State != "" && !strings.EqualFold(summary.Sharing.State, query.State) {
				continue
			}
			if query.Visibility != "" && !strings.EqualFold(summary.Sharing.Visibility, query.Visibility) {
				continue
			}
			if query.Q != "" && !matching[summary.ID] {
				continue
			}
			resp.Traces = append(resp.Traces, summary)
		}
		sortTraceSummaries(resp.Traces)
		resp.TotalMatched = len(resp.Traces)
		if len(resp.Traces) > limit {
			resp.Traces = resp.Traces[:limit]
		}
		resp.Returned = len(resp.Traces)
	}
	return resp, nil
}

func (s *traceStore) Show(id string, query TraceQuery) (TraceShowResultV1, bool, error) {
	if !traceStoreSupportsQuery(query) {
		return TraceShowResultV1{}, false, errTraceStoreUnsupported
	}
	if err := s.ensureCurrent(); err != nil {
		return TraceShowResultV1{}, false, err
	}
	db, err := s.db()
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	defer db.Close()
	var summaryJSON string
	err = db.QueryRow(`SELECT summary_json FROM traces WHERE source_key = ? AND id = ?`, s.logPath, id).Scan(&summaryJSON)
	if err == sql.ErrNoRows {
		return TraceShowResultV1{}, false, nil
	}
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	var summary TraceSummaryV1
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		return TraceShowResultV1{}, false, err
	}
	events, err := s.loadTraceEvents(db, id, query.EventTypes)
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	total := len(events)
	offset, limit := traceRange(query, total)
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	if offset-1 > end {
		end = offset - 1
	}
	return TraceShowResultV1{
		Trace:  summary,
		Events: append([]TraceEventV1(nil), events[offset-1:end]...),
		Spans:  spansFromEvents(events),
		Range: TraceRangeV1{
			TotalEvents:    total,
			ReturnedEvents: end - (offset - 1),
			Offset:         offset,
			Limit:          limit,
			AroundEvent:    query.AroundEvent,
		},
	}, true, nil
}

var errTraceStoreUnsupported = fmt.Errorf("trace store does not support query")

func traceStoreSupportsQuery(query TraceQuery) bool {
	eq := query.EventQuery
	return eq.Since.IsZero() &&
		eq.Until.IsZero() &&
		eq.Harness == "" &&
		eq.Model == "" &&
		eq.Action == "" &&
		eq.Severity == "" &&
		eq.Category == "" &&
		eq.Repository == "" &&
		eq.Session == "" &&
		eq.Trace == "" &&
		eq.File == "" &&
		eq.Command == "" &&
		eq.MCP == "" &&
		eq.Approval == "" &&
		eq.Decision == "" &&
		eq.Policy == "" &&
		eq.Review == "" &&
		eq.WazuhLevel == "" &&
		eq.SessionState == ""
}

func (s *traceStore) loadSummaries() ([]TraceSummaryV1, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT summary_json FROM traces WHERE source_key = ?`, s.logPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []TraceSummaryV1
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var summary TraceSummaryV1
		if err := json.Unmarshal([]byte(raw), &summary); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (s *traceStore) matchingTraceIDs(query TraceQuery, opts traceSearchOptions) (map[string]bool, error) {
	out := map[string]bool{}
	needle := strings.TrimSpace(query.Q)
	if needle == "" {
		return out, nil
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT trace_id, body FROM trace_search WHERE trace_id IN (SELECT id FROM traces WHERE source_key = ?) AND source IN (`+sourcePlaceholders(opts.Sources)+`)`, append([]interface{}{s.logPath}, sourcesOrDefault(opts.Sources)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		if textMatches(body, needle, opts.Literal, opts.CaseSensitive) {
			out[id] = true
		}
	}
	return out, rows.Err()
}

type traceEventSearchRows struct {
	returned []TraceEventMatchV1
	total    int
}

func (s *traceStore) searchEvents(query TraceQuery, opts traceSearchOptions, limit int) (traceEventSearchRows, error) {
	db, err := s.db()
	if err != nil {
		return traceEventSearchRows{}, err
	}
	defer db.Close()
	summaries, err := s.loadSummaries()
	if err != nil {
		return traceEventSearchRows{}, err
	}
	sortTraceSummaries(summaries)
	result := traceEventSearchRows{}
	allowedTypes := traceEventTypeSet(query.EventTypes)
	for _, summary := range summaries {
		if query.State != "" && !strings.EqualFold(summary.Sharing.State, query.State) {
			continue
		}
		if query.Visibility != "" && !strings.EqualFold(summary.Sharing.Visibility, query.Visibility) {
			continue
		}
		events, err := s.loadTraceEvents(db, summary.ID, nil)
		if err != nil {
			return traceEventSearchRows{}, err
		}
		for _, event := range events {
			if query.Q != "" && !textMatches(traceEventSearchBody(event), query.Q, opts.Literal, opts.CaseSensitive) {
				continue
			}
			if len(allowedTypes) > 0 && !allowedTypes[traceEventTypeAlias(event.Type)] && !allowedTypes[strings.ToLower(event.Type)] {
				continue
			}
			if opts.Tool != "" && (event.Tool == nil || !strings.EqualFold(event.Tool.Name, opts.Tool)) {
				continue
			}
			result.total++
			if len(result.returned) < limit {
				result.returned = append(result.returned, TraceEventMatchV1{
					Trace:   summary,
					Event:   event,
					Snippet: traceSnippet(event, query.Q),
					Score:   1,
				})
			}
		}
	}
	return result, nil
}

func (s *traceStore) loadTraceEvents(db *sql.DB, traceID string, eventTypes []string) ([]TraceEventV1, error) {
	rows, err := db.Query(`SELECT event_json FROM trace_events WHERE trace_id = ? ORDER BY event_number`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := traceEventTypeSet(eventTypes)
	var events []TraceEventV1
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event TraceEventV1
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		if len(allowed) == 0 || allowed[strings.ToLower(event.Type)] || allowed[traceEventTypeAlias(event.Type)] {
			events = append(events, event)
		}
	}
	return events, rows.Err()
}

func (s *traceStore) loadOneEvent(db *sql.DB, traceID, eventID string) (TraceEventV1, bool, error) {
	var raw string
	err := db.QueryRow(`SELECT event_json FROM trace_events WHERE trace_id = ? AND event_id = ?`, traceID, eventID).Scan(&raw)
	if err == sql.ErrNoRows {
		return TraceEventV1{}, false, nil
	}
	if err != nil {
		return TraceEventV1{}, false, err
	}
	var event TraceEventV1
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return TraceEventV1{}, false, err
	}
	return event, true, nil
}

func spansFromEvents(events []TraceEventV1) []TraceSpanV1 {
	spans := map[string]*TraceSpanV1{}
	for _, event := range events {
		if event.Trace == nil || event.Trace.SpanID == "" {
			continue
		}
		span := spans[event.Trace.SpanID]
		if span == nil {
			span = &TraceSpanV1{ID: event.Trace.SpanID, ParentSpanID: event.Trace.ParentSpanID, TraceID: event.Trace.ID}
			spans[event.Trace.SpanID] = span
		}
		if span.Name == "" {
			span.Name = firstNonEmpty(event.Summary, event.Title, event.Action)
		}
		span.EventIDs = append(span.EventIDs, event.ID)
	}
	return traceSpanList(spans)
}

func traceLogFingerprint(logPath string) string {
	parts := []string{}
	for _, source := range eventSources(logPath) {
		info, err := os.Stat(source.path)
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", source.path, info.Size(), info.ModTime().UnixNano()))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func traceSummarySearchBody(summary TraceSummaryV1) string {
	fields := []string{
		summary.ID,
		summary.Title,
		summary.Preview,
		summary.Harness.Name,
		summary.Harness.Version,
		summary.Sharing.State,
		summary.Sharing.Visibility,
		summary.Sharing.URL,
	}
	if summary.Session != nil {
		fields = append(fields, summary.Session.ID, summary.Session.WorkingDirectory)
	}
	if summary.Repository != nil {
		fields = append(fields, summary.Repository.RemoteURL, summary.Repository.Branch, summary.Repository.Ref, summary.Repository.Path)
	}
	if summary.Namespace != nil {
		fields = append(fields, summary.Namespace.ID, summary.Namespace.Slug, summary.Namespace.Name)
	}
	if summary.Trace != nil {
		fields = append(fields, summary.Trace.ID, summary.Trace.RootSpanID)
	}
	return strings.Join(fields, " ")
}

func traceEventSearchBody(event TraceEventV1) string {
	fields := []string{event.ID, event.Type, event.Action, event.Category, event.Actor, event.Title, event.Summary, event.Model}
	if event.Content != nil {
		fields = append(fields, event.Content.Text, event.Content.Hash)
	}
	if event.Tool != nil {
		fields = append(fields, event.Tool.Name, event.Tool.Command, event.Tool.Path, fmt.Sprint(event.Tool.Arguments), fmt.Sprint(event.Tool.Result))
	}
	if event.Command != nil {
		fields = append(fields, event.Command.Command)
		if event.Command.Output != nil {
			fields = append(fields, event.Command.Output.Text)
		}
	}
	if event.File != nil {
		fields = append(fields, event.File.Path, event.File.Operation, event.File.Language, event.File.DiffHash)
		if event.File.Diff != nil {
			fields = append(fields, event.File.Diff.Text)
		}
	}
	if event.MCP != nil {
		fields = append(fields, event.MCP.Server, event.MCP.Tool, event.MCP.Method, event.MCP.ResourceURI)
	}
	if event.Approval != nil {
		fields = append(fields, event.Approval.Decision, event.Approval.Reason)
	}
	return strings.Join(fields, " ")
}

func textMatches(body, pattern string, literal, caseSensitive bool) bool {
	body = strings.TrimSpace(body)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	if literal {
		if caseSensitive {
			return strings.Contains(body, pattern)
		}
		return strings.Contains(strings.ToLower(body), strings.ToLower(pattern))
	}
	if caseSensitive {
		for _, term := range strings.Fields(pattern) {
			if !strings.Contains(body, term) {
				return false
			}
		}
		return true
	}
	lowerBody := strings.ToLower(body)
	for _, term := range strings.Fields(strings.ToLower(pattern)) {
		if !strings.Contains(lowerBody, term) {
			return false
		}
	}
	return true
}

func sourcePlaceholders(sources []string) string {
	values := sourcesOrDefault(sources)
	out := make([]string, len(values))
	for i := range out {
		out[i] = "?"
	}
	return strings.Join(out, ",")
}

func sourcesOrDefault(sources []string) []interface{} {
	if len(sources) == 0 {
		return []interface{}{"trace", "event"}
	}
	out := make([]interface{}, 0, len(sources))
	for _, source := range sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "trace" || source == "event" {
			out = append(out, source)
		}
	}
	if len(out) == 0 {
		return []interface{}{"trace", "event"}
	}
	return out
}

func traceEventTypeSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out[value] = true
		switch value {
		case "agent_text":
			out["agent_message"] = true
		case "agent_thinking":
			out["agent_reasoning"] = true
		case "agent_message":
			out["agent_text"] = true
		case "agent_reasoning":
			out["agent_thinking"] = true
		}
	}
	return out
}

func traceEventTypeAlias(value string) string {
	switch strings.ToLower(value) {
	case "agent_message":
		return "agent_text"
	case "agent_reasoning":
		return "agent_thinking"
	default:
		return strings.ToLower(value)
	}
}
