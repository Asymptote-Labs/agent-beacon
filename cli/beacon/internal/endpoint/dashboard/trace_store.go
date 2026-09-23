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

// traceStoreSchemaVersion is written to the database's user_version. The store
// is a rebuildable index over the runtime log, never a source of truth, so a
// version bump drops the tables and reindexes rather than migrating them.
const traceStoreSchemaVersion = 4

// Search rows are an acceleration structure, not retained evidence. Keep them
// small and fall back to the JSONL scan for free-text queries when truncation
// could hide a match.
const maxTraceStoreSearchBodyBytes = 16 * 1024

type traceStore struct {
	dbPath string
	// logPath is the log to read; sourceKey is the same path normalized, and is
	// what every row is scoped by. Sibling logs in one directory share one
	// traces.db, so two sources can hold the same trace or event id.
	logPath   string
	sourceKey string
}

type traceStoreStatus struct {
	Path      string `json:"path"`
	Traces    int    `json:"traces"`
	Events    int    `json:"events"`
	IndexRows int    `json:"index_rows"`
	SizeBytes int64  `json:"size_bytes"`
	WALBytes  int64  `json:"wal_bytes,omitempty"`
	IndexedAt string `json:"indexed_at,omitempty"`
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
	return &traceStore{
		dbPath:    defaultTraceStorePath(logPath),
		logPath:   logPath,
		sourceKey: traceSourceKey(logPath),
	}
}

// traceSourceKey normalizes a log path so the same log reached by a relative
// and an absolute path indexes once rather than twice.
func traceSourceKey(logPath string) string {
	abs, err := filepath.Abs(logPath)
	if err != nil {
		return logPath
	}
	return filepath.Clean(abs)
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
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	// Any version but the current one is dropped, 0 included: a database this
	// build has never stamped either is empty, where the drops are no-ops, or
	// predates the stamp and holds tables CREATE TABLE IF NOT EXISTS would
	// leave in their old shape.
	if version != traceStoreSchemaVersion {
		for _, table := range []string{"trace_search", "trace_events", "traces", "trace_index_state"} {
			if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
				return err
			}
		}
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trace_index_state (
			source_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			indexed_at TEXT NOT NULL
		)`,
		// Trace ids are unique within one source, not across a directory of
		// them, so the key carries the source.
		`CREATE TABLE IF NOT EXISTS traces (
			source_key TEXT NOT NULL,
			id TEXT NOT NULL,
			summary_json TEXT NOT NULL,
			updated_at TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (source_key, id)
		)`,
		`CREATE INDEX IF NOT EXISTS traces_by_source_updated ON traces (source_key, updated_at DESC)`,
		// event_number is the event's ordinal within its trace, which is what
		// makes a row unique. event_id is the runtime's own name for the call
		// and is deliberately not part of the key: two events can share one --
		// a repeated log line, or a hook and an OTLP capture of the same call,
		// whose ids are derived to match -- and the JSONL path keeps both.
		`CREATE TABLE IF NOT EXISTS trace_events (
			source_key TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			event_number INTEGER NOT NULL,
			event_id TEXT NOT NULL,
			event_type TEXT,
			tool_name TEXT,
			event_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (source_key, trace_id, event_number)
		)`,
		`CREATE INDEX IF NOT EXISTS trace_events_by_event_id ON trace_events (source_key, trace_id, event_id)`,
		`CREATE TABLE IF NOT EXISTS trace_search (
			source_key TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			event_id TEXT,
			source TEXT NOT NULL,
			event_type TEXT,
			tool_name TEXT,
			event_number INTEGER,
			body TEXT NOT NULL,
			body_truncated INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS trace_search_by_source_trace ON trace_search (source_key, source, trace_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if version != traceStoreSchemaVersion {
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, traceStoreSchemaVersion)); err != nil {
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
	err = db.QueryRow(`SELECT fingerprint FROM trace_index_state WHERE source_key = ?`, s.sourceKey).Scan(&current)
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
	if err := purgeMissingTraceSources(tx); err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM trace_search WHERE source_key = ?`,
		`DELETE FROM trace_events WHERE source_key = ?`,
		`DELETE FROM traces WHERE source_key = ?`,
	} {
		if _, err := tx.Exec(stmt, s.sourceKey); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, agg := range traces {
		if err := insertTraceAggregate(tx, s.sourceKey, agg, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO trace_index_state (source_key, fingerprint, indexed_at) VALUES (?, ?, ?)
		ON CONFLICT(source_key) DO UPDATE SET fingerprint = excluded.fingerprint, indexed_at = excluded.indexed_at`,
		s.sourceKey, fingerprint, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return checkpointTraceStore(db)
}

func insertTraceAggregate(tx *sql.Tx, sourceKey string, agg *traceAggregate, now string) error {
	summaryJSON, err := json.Marshal(agg.summary)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO traces (source_key, id, summary_json, updated_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		sourceKey, agg.summary.ID, string(summaryJSON), agg.summary.UpdatedAt, now); err != nil {
		return err
	}
	if err := insertTraceSearchRow(tx, sourceKey, agg.summary.ID, "", "trace", "", "", 0, traceSummaryHaystack(agg.summary)); err != nil {
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
		if _, err := tx.Exec(`INSERT INTO trace_events (source_key, trace_id, event_number, event_id, event_type, tool_name, event_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sourceKey, agg.summary.ID, event.Number, event.ID, event.Type, toolName, string(eventJSON), now); err != nil {
			return err
		}
		if err := insertTraceSearchRow(tx, sourceKey, agg.summary.ID, event.ID, "event", event.Type, toolName, event.Number, traceEventHaystack(event)); err != nil {
			return err
		}
	}
	return nil
}

func insertTraceSearchRow(tx *sql.Tx, sourceKey, traceID, eventID, source, eventType, toolName string, eventNumber int, body string) error {
	body, truncated := truncateTraceSearchBody(body)
	truncatedInt := 0
	if truncated {
		truncatedInt = 1
	}
	_, err := tx.Exec(`INSERT INTO trace_search (source_key, trace_id, event_id, source, event_type, tool_name, event_number, body, body_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sourceKey, traceID, eventID, source, eventType, toolName, eventNumber, body, truncatedInt)
	return err
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
	status := traceStoreStatus{Path: s.dbPath, SizeBytes: fileSize(s.dbPath), WALBytes: fileSize(s.dbPath + "-wal")}
	_ = db.QueryRow(`SELECT COUNT(*) FROM traces WHERE source_key = ?`, s.sourceKey).Scan(&status.Traces)
	_ = db.QueryRow(`SELECT COUNT(*) FROM trace_events WHERE source_key = ?`, s.sourceKey).Scan(&status.Events)
	_ = db.QueryRow(`SELECT COUNT(*) FROM trace_search WHERE source_key = ?`, s.sourceKey).Scan(&status.IndexRows)
	_ = db.QueryRow(`SELECT indexed_at FROM trace_index_state WHERE source_key = ?`, s.sourceKey).Scan(&status.IndexedAt)
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
	// traceAggregateMatches keeps a trace whose summary or any of its events
	// match, so the store searches both kinds of row.
	matchingIDs, err := s.matchingTraceIDs(query.Q, "trace", "event")
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

func (s *traceStore) Search(query TraceQuery) (TraceSearchResultV1, error) {
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
		events, err := s.searchEvents(query, limit)
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
		// Unlike List, trace-level search matches the summary alone -- the
		// JSONL path uses traceSummaryMatches here, not traceAggregateMatches.
		matching, err := s.matchingTraceIDs(query.Q, "trace")
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
	err = db.QueryRow(`SELECT summary_json FROM traces WHERE source_key = ? AND id = ?`, s.sourceKey, id).Scan(&summaryJSON)
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
	rows, err := db.Query(`SELECT summary_json FROM traces WHERE source_key = ?`, s.sourceKey)
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

// matchingTraceIDs returns the traces whose indexed text matches the free-text
// query, looking only at the named row kinds ("trace" for summaries, "event"
// for events).
func (s *traceStore) matchingTraceIDs(q string, kinds ...string) (map[string]bool, error) {
	out := map[string]bool{}
	needle := strings.TrimSpace(q)
	if needle == "" || len(kinds) == 0 {
		return out, nil
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if truncated, err := s.hasTruncatedSearchRows(db, kinds...); err != nil {
		return nil, err
	} else if truncated {
		return nil, errTraceStoreUnsupported
	}
	args := []any{s.sourceKey}
	for _, kind := range kinds {
		args = append(args, kind)
	}
	rows, err := db.Query(`SELECT trace_id, body FROM trace_search WHERE source_key = ? AND source IN (`+placeholders(len(kinds))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		if matchesAllTerms(body, needle) {
			out[id] = true
		}
	}
	return out, rows.Err()
}

type traceEventSearchRows struct {
	returned []TraceEventMatchV1
	total    int
}

func (s *traceStore) searchEvents(query TraceQuery, limit int) (traceEventSearchRows, error) {
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
	needle := strings.TrimSpace(query.Q)
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
			if len(allowedTypes) > 0 && !allowedTypes[traceEventTypeAlias(event.Type)] && !allowedTypes[strings.ToLower(event.Type)] {
				continue
			}
			if needle != "" && !traceEventMatches(event, needle) {
				continue
			}
			result.total++
			if len(result.returned) < limit {
				result.returned = append(result.returned, TraceEventMatchV1{
					Trace:   summary,
					Event:   event,
					Snippet: traceSnippet(event, needle),
					Score:   1,
				})
			}
		}
	}
	return result, nil
}

func (s *traceStore) loadTraceEvents(db *sql.DB, traceID string, eventTypes []string) ([]TraceEventV1, error) {
	rows, err := db.Query(`SELECT event_json FROM trace_events WHERE source_key = ? AND trace_id = ? ORDER BY event_number`, s.sourceKey, traceID)
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

func purgeMissingTraceSources(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT source_key FROM trace_index_state`)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			rows.Close()
			return err
		}
		if _, err := os.Stat(source); os.IsNotExist(err) {
			missing = append(missing, source)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, source := range missing {
		for _, stmt := range []string{
			`DELETE FROM trace_search WHERE source_key = ?`,
			`DELETE FROM trace_events WHERE source_key = ?`,
			`DELETE FROM traces WHERE source_key = ?`,
			`DELETE FROM trace_index_state WHERE source_key = ?`,
		} {
			if _, err := tx.Exec(stmt, source); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkpointTraceStore(db *sql.DB) error {
	_, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func truncateTraceSearchBody(body string) (string, bool) {
	if len(body) <= maxTraceStoreSearchBodyBytes {
		return body, false
	}
	used := 0
	for i, r := range body {
		next := used + len(string(r))
		if next > maxTraceStoreSearchBodyBytes {
			return body[:i], true
		}
		used = next
	}
	return body, false
}

func (s *traceStore) hasTruncatedSearchRows(db *sql.DB, kinds ...string) (bool, error) {
	if len(kinds) == 0 {
		return false, nil
	}
	args := []any{s.sourceKey}
	for _, kind := range kinds {
		args = append(args, kind)
	}
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM trace_search WHERE source_key = ? AND source IN (`+placeholders(len(kinds))+`) AND body_truncated = 1`, args...).Scan(&count)
	return count > 0, err
}

// placeholders builds an n-wide SQL "?,?,..." list.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
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
