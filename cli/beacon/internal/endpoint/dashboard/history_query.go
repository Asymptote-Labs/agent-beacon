package dashboard

import (
	"database/sql"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// historyList, historySearch and historyShow answer a trace query from the local history, or
// return an error that sends the caller to the JSONL scan: the store is not set up, the query uses
// a filter the store does not index, or the store could not be read.
func historyList(logPath string, query TraceQuery) (TraceListResultV1, error) {
	if !traceStoreSupportsQuery(query) {
		return TraceListResultV1{}, errTraceStoreUnsupported
	}
	store, sourceID, err := openCaughtUpHistory(logPath)
	if err != nil {
		return TraceListResultV1{}, err
	}
	defer store.close()
	return store.list(sourceID, query)
}

func historySearch(logPath string, query TraceQuery) (TraceSearchResultV1, error) {
	if !traceStoreSupportsQuery(query) {
		return TraceSearchResultV1{}, errTraceStoreUnsupported
	}
	store, sourceID, err := openCaughtUpHistory(logPath)
	if err != nil {
		return TraceSearchResultV1{}, err
	}
	defer store.close()
	return store.search(sourceID, query)
}

func historyShow(logPath, id string, query TraceQuery) (TraceShowResultV1, bool, error) {
	if !traceStoreSupportsQuery(query) {
		return TraceShowResultV1{}, false, errTraceStoreUnsupported
	}
	store, sourceID, err := openCaughtUpHistory(logPath)
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	defer store.close()
	return store.show(sourceID, id, query)
}

func openCaughtUpHistory(logPath string) (*historyStore, int64, error) {
	store, err := openHistoryStore(false)
	if err != nil {
		return nil, 0, err
	}
	sourceID, err := store.catchUp(logPath, nil, false)
	if err != nil {
		store.close()
		return nil, 0, err
	}
	return store, sourceID, nil
}

// traceStoreSupportsQuery reports whether the store can answer a query. The store indexes free
// text, sharing state and visibility; any other event filter is answered by the JSONL scan.
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

// textMatch turns a free-text query into SQL that selects exactly what matchesAllTerms selects.
// Terms of three or more characters go to the trigram index, where a quoted term matches the rows
// that contain it as a substring. Shorter terms have no trigram, so they are checked with instr()
// on the stored haystack, which the index text is also built from.
type textMatch struct {
	long  []string
	short []string
}

func newTextMatch(q string) textMatch {
	var m textMatch
	for _, term := range strings.Fields(strings.ToLower(q)) {
		if utf8.RuneCountInString(term) >= 3 {
			m.long = append(m.long, term)
		} else {
			m.short = append(m.short, term)
		}
	}
	return m
}

func (m textMatch) empty() bool {
	return len(m.long) == 0 && len(m.short) == 0
}

func ftsPhrases(terms []string) string {
	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " ")
}

// condition returns a WHERE fragment over alias (a row of table, whose FTS table is fts).
func (m textMatch) condition(alias, fts string) (string, []any) {
	var parts []string
	var args []any
	if len(m.long) > 0 {
		parts = append(parts, alias+".id IN (SELECT rowid FROM "+fts+" WHERE "+fts+" MATCH ?)")
		args = append(args, ftsPhrases(m.long))
	}
	for _, term := range m.short {
		parts = append(parts, "instr("+alias+".hay, ?) > 0")
		args = append(args, term)
	}
	if len(parts) == 0 {
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

func sharingCondition(alias string, query TraceQuery) (string, []any) {
	var parts []string
	var args []any
	if query.State != "" {
		parts = append(parts, alias+".state = ? COLLATE NOCASE")
		args = append(args, query.State)
	}
	if query.Visibility != "" {
		parts = append(parts, alias+".visibility = ? COLLATE NOCASE")
		args = append(args, query.Visibility)
	}
	if len(parts) == 0 {
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

// list returns one page of traces, newest first. With free text it keeps a trace whose summary or
// any of its events match, as traceAggregateMatches does.
func (s *historyStore) list(sourceID int64, query TraceQuery) (TraceListResultV1, error) {
	match := newTextMatch(query.Q)
	sharing, sharingArgs := sharingCondition("t", query)
	where := "t.source_id = ? AND " + sharing
	args := append([]any{sourceID}, sharingArgs...)
	if !match.empty() {
		summaryCond, summaryArgs := match.condition("m", "trace_fts")
		eventCond, eventArgs := match.condition("e", "event_fts")
		where += ` AND t.trace_key IN (
			SELECT m.trace_key FROM traces m WHERE m.source_id = ? AND ` + summaryCond + `
			UNION
			SELECT e.trace_key FROM events e WHERE e.source_id = ? AND ` + eventCond + `)`
		args = append(args, sourceID)
		args = append(args, summaryArgs...)
		args = append(args, sourceID)
		args = append(args, eventArgs...)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM traces t WHERE `+where, args...).Scan(&total); err != nil {
		return TraceListResultV1{}, err
	}
	limit := normalizeLimit(query.Limit)
	page := query.Page
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	summaries, err := s.summaries(`SELECT t.aggregate FROM traces t WHERE `+where+` ORDER BY t.updated_ns DESC, t.trace_key ASC LIMIT ? OFFSET ?`,
		append(args, limit, start)...)
	if err != nil {
		return TraceListResultV1{}, err
	}
	end := start + len(summaries)
	return TraceListResultV1{
		Traces:       summaries,
		TotalMatched: total,
		Returned:     len(summaries),
		Limit:        limit,
		Page:         page,
		Truncated:    end < total,
		Filters:      activeTraceFilters(query),
	}, nil
}

func (s *historyStore) summaries(query string, args ...any) ([]TraceSummaryV1, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceSummaryV1{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		summary, err := finishedSummaryFromState(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

func (s *historyStore) search(sourceID int64, query TraceQuery) (TraceSearchResultV1, error) {
	if query.ResultLevel == "" {
		query.ResultLevel = "trace"
	}
	limit := normalizeLimit(query.Limit)
	resp := TraceSearchResultV1{ResultLevel: query.ResultLevel, Limit: limit, Filters: activeTraceFilters(query)}
	match := newTextMatch(query.Q)
	sharing, sharingArgs := sharingCondition("t", query)
	if query.ResultLevel == "event" {
		return s.searchEvents(sourceID, query, match, sharing, sharingArgs, resp)
	}
	resp.ResultLevel = "trace"
	// Trace-level search matches the summary alone, as the JSONL path does with
	// traceSummaryMatches.
	cond, condArgs := match.condition("t", "trace_fts")
	where := "t.source_id = ? AND " + sharing + " AND " + cond
	args := append(append([]any{sourceID}, sharingArgs...), condArgs...)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM traces t WHERE `+where, args...).Scan(&resp.TotalMatched); err != nil {
		return TraceSearchResultV1{}, err
	}
	traces, err := s.summaries(`SELECT t.aggregate FROM traces t WHERE `+where+` ORDER BY t.updated_ns DESC, t.trace_key ASC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return TraceSearchResultV1{}, err
	}
	if len(traces) > 0 {
		resp.Traces = traces
	}
	resp.Returned = len(traces)
	return resp, nil
}

type historyEventRef struct {
	id        int64
	traceRow  int64
	seq       int
	eventType string
}

// searchEvents returns matching events across traces, newest trace first and in event order
// within a trace, counting every match and returning the first limit of them.
func (s *historyStore) searchEvents(sourceID int64, query TraceQuery, match textMatch, sharing string, sharingArgs []any, resp TraceSearchResultV1) (TraceSearchResultV1, error) {
	cond, condArgs := match.condition("e", "event_fts")
	args := append(append([]any{sourceID}, sharingArgs...), condArgs...)
	rows, err := s.db.Query(`SELECT e.id, t.id, e.seq, e.event_type
		FROM events e JOIN traces t ON t.source_id = e.source_id AND t.trace_key = e.trace_key
		WHERE e.source_id = ? AND `+sharing+` AND `+cond+`
		ORDER BY t.updated_ns DESC, t.trace_key ASC, e.seq ASC`, args...)
	if err != nil {
		return TraceSearchResultV1{}, err
	}
	allowed := traceEventTypeSet(query.EventTypes)
	var picked []historyEventRef
	for rows.Next() {
		var ref historyEventRef
		if err := rows.Scan(&ref.id, &ref.traceRow, &ref.seq, &ref.eventType); err != nil {
			rows.Close()
			return TraceSearchResultV1{}, err
		}
		if len(allowed) > 0 && !allowed[traceEventTypeAlias(ref.eventType)] && !allowed[strings.ToLower(ref.eventType)] {
			continue
		}
		resp.TotalMatched++
		if len(picked) < resp.Limit {
			picked = append(picked, ref)
		}
	}
	if err := rows.Close(); err != nil {
		return TraceSearchResultV1{}, err
	}
	summaries := map[int64]TraceSummaryV1{}
	needle := strings.TrimSpace(query.Q)
	for _, ref := range picked {
		summary, ok := summaries[ref.traceRow]
		if !ok {
			var raw string
			if err := s.db.QueryRow(`SELECT aggregate FROM traces WHERE id = ?`, ref.traceRow).Scan(&raw); err != nil {
				return TraceSearchResultV1{}, err
			}
			if summary, err = finishedSummaryFromState(raw); err != nil {
				return TraceSearchResultV1{}, err
			}
			summaries[ref.traceRow] = summary
		}
		event, err := s.loadTraceEvent(ref.id, ref.seq)
		if err != nil {
			return TraceSearchResultV1{}, err
		}
		resp.Events = append(resp.Events, TraceEventMatchV1{
			Trace:   summary,
			Event:   event,
			Snippet: traceSnippet(event, needle),
			Score:   1,
		})
	}
	resp.Returned = len(resp.Events)
	return resp, nil
}

// loadTraceEvent projects one stored event, numbered as it was when stored.
func (s *historyStore) loadTraceEvent(id int64, seq int) (TraceEventV1, error) {
	var recordID string
	var line []byte
	if err := s.db.QueryRow(`SELECT record_id, line FROM events WHERE id = ?`, id).Scan(&recordID, &line); err != nil {
		return TraceEventV1{}, err
	}
	return projectStoredEvent(recordID, line, seq)
}

func projectStoredEvent(recordID string, compressed []byte, seq int) (TraceEventV1, error) {
	raw, err := decompressLine(compressed)
	if err != nil {
		return TraceEventV1{}, err
	}
	var event schema.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return TraceEventV1{}, err
	}
	normalizeDashboardEvent(&event)
	return traceEventFromRecord(EventRecord{ID: recordID, Event: event, Raw: raw}, seq), nil
}

type historyShowRow struct {
	id           int64
	seq          int
	eventType    string
	eventID      string
	spanID       string
	parentSpanID string
	otelTraceID  string
	spanName     string
}

func (s *historyStore) show(sourceID int64, id string, query TraceQuery) (TraceShowResultV1, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT aggregate FROM traces WHERE source_id = ? AND trace_key = ?`, sourceID, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return TraceShowResultV1{}, false, nil
	}
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	summary, err := finishedSummaryFromState(raw)
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	rows, err := s.db.Query(`SELECT id, seq, event_type, event_id, span_id, parent_span_id, otel_trace_id, span_name
		FROM events WHERE source_id = ? AND trace_key = ? ORDER BY seq`, sourceID, id)
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	allowed := traceEventTypeSet(query.EventTypes)
	var filtered []historyShowRow
	for rows.Next() {
		var row historyShowRow
		if err := rows.Scan(&row.id, &row.seq, &row.eventType, &row.eventID, &row.spanID, &row.parentSpanID, &row.otelTraceID, &row.spanName); err != nil {
			rows.Close()
			return TraceShowResultV1{}, false, err
		}
		if len(allowed) > 0 && !allowed[strings.ToLower(row.eventType)] && !allowed[traceEventTypeAlias(row.eventType)] {
			continue
		}
		filtered = append(filtered, row)
	}
	if err := rows.Close(); err != nil {
		return TraceShowResultV1{}, false, err
	}
	total := len(filtered)
	offset, limit := traceRange(query, total)
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	if offset-1 > end {
		end = offset - 1
	}
	// nil rather than empty when the range is past the end, which is how both other paths encode it.
	var events []TraceEventV1
	for _, row := range filtered[offset-1 : end] {
		event, err := s.loadTraceEvent(row.id, row.seq)
		if err != nil {
			return TraceShowResultV1{}, false, err
		}
		events = append(events, event)
	}
	return TraceShowResultV1{
		Trace:  summary,
		Events: events,
		Spans:  historySpans(filtered),
		Range: TraceRangeV1{
			TotalEvents:    total,
			ReturnedEvents: end - (offset - 1),
			Offset:         offset,
			Limit:          limit,
			AroundEvent:    query.AroundEvent,
		},
	}, true, nil
}

// historySpans is spansFromEvents over stored rows, so show does not have to project every event
// of a long trace to list its spans.
func historySpans(rows []historyShowRow) []TraceSpanV1 {
	spans := map[string]*TraceSpanV1{}
	for _, row := range rows {
		if row.spanID == "" {
			continue
		}
		span := spans[row.spanID]
		if span == nil {
			span = &TraceSpanV1{ID: row.spanID, ParentSpanID: row.parentSpanID, TraceID: row.otelTraceID}
			spans[row.spanID] = span
		}
		if span.Name == "" {
			span.Name = row.spanName
		}
		span.EventIDs = append(span.EventIDs, row.eventID)
	}
	return traceSpanList(spans)
}

// spansFromEvents lists the spans of the given events, in the order traceSpanList sorts them. Both
// paths build show's spans from the events the query selected, so a type filter narrows the spans
// the same way whether the store or the JSONL scan answers.
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
