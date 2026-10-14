package dashboard

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const TraceSchemaVersion = asymptoteobserve.TraceSchemaVersion

type TraceBundleV1 = asymptoteobserve.TraceBundleV1
type TraceProvenanceV1 = asymptoteobserve.TraceProvenanceV1
type TraceSummaryV1 = asymptoteobserve.TraceSummaryV1
type TraceHarnessV1 = asymptoteobserve.TraceHarnessV1
type TraceSessionV1 = asymptoteobserve.TraceSessionV1
type TraceIdentityV1 = asymptoteobserve.TraceIdentityV1
type TraceRepositoryV1 = asymptoteobserve.TraceRepositoryV1
type TraceNamespaceV1 = asymptoteobserve.TraceNamespaceV1
type TraceModelV1 = asymptoteobserve.TraceModelV1
type TraceRemoteV1 = asymptoteobserve.TraceRemoteV1
type TraceSharingV1 = asymptoteobserve.TraceSharingV1
type TraceActorV1 = asymptoteobserve.TraceActorV1
type TraceContentSummaryV1 = asymptoteobserve.TraceContentSummaryV1
type TraceEventV1 = asymptoteobserve.TraceEventV1
type TraceEventTraceV1 = asymptoteobserve.TraceEventTraceV1
type TraceContentV1 = asymptoteobserve.TraceContentV1
type TraceToolV1 = asymptoteobserve.TraceToolV1
type TraceCommandV1 = asymptoteobserve.TraceCommandV1
type TraceFileV1 = asymptoteobserve.TraceFileV1
type TraceMCPV1 = asymptoteobserve.TraceMCPV1
type TraceApprovalV1 = asymptoteobserve.TraceApprovalV1
type TracePolicyV1 = asymptoteobserve.TracePolicyV1
type TraceErrorV1 = asymptoteobserve.TraceErrorV1
type TraceUsageV1 = asymptoteobserve.TraceUsageV1
type TraceSpanV1 = asymptoteobserve.TraceSpanV1
type TraceRangeV1 = asymptoteobserve.TraceRangeV1
type TraceListResultV1 = asymptoteobserve.TraceListResultV1
type TraceSearchResultV1 = asymptoteobserve.TraceSearchResultV1
type TraceEventMatchV1 = asymptoteobserve.TraceEventMatchV1
type TraceShowResultV1 = asymptoteobserve.TraceShowResultV1

type TraceQuery struct {
	EventQuery
	Limit       int
	Page        int
	State       string
	Visibility  string
	EventTypes  []string
	ResultLevel string
	Offset      int
	AroundEvent int
	Before      int
	After       int
}

// traceAggregate is one projected trace as the JSONL scan answers from it: its finished summary
// and its events, built by asymptoteobserve.TraceBuilder.
type traceAggregate struct {
	summary TraceSummaryV1
	events  []TraceEventV1
}

// traceFold is a trace being folded: asymptoteobserve.TraceBuilder, which projects the summary
// every reader shares, and the local view's sharing staleness, which only local views show. The
// JSONL scan and the history fold both go through it, so they answer alike.
type traceFold struct {
	builder *asymptoteobserve.TraceBuilder
	stale   traceStaleness
}

func newTraceFold(id string, first schema.Event) *traceFold {
	return &traceFold{builder: asymptoteobserve.NewTraceBuilder(id, first)}
}

// add numbers and keeps te, and folds event into the summary and the staleness.
func (f *traceFold) add(event schema.Event, te TraceEventV1) {
	f.builder.Add(event, te)
	f.stale.observe(event)
}

// fold folds event without keeping its projection, for the history, which stores events itself.
func (f *traceFold) fold(event schema.Event, te TraceEventV1) {
	f.builder.Fold(event, te)
	f.stale.observe(event)
}

// summary is the finished summary with the sharing staleness applied. Neither changes the fold.
func (f *traceFold) summary() TraceSummaryV1 {
	summary := f.builder.Summary()
	f.stale.apply(&summary)
	return summary
}

// traceAggregateState is what the history stores of a fold between catch-ups: the builder's state
// and the staleness, in one JSON object.
type traceAggregateState struct {
	asymptoteobserve.TraceBuilderState
	traceStaleness
}

func (f *traceFold) state() traceAggregateState {
	return traceAggregateState{TraceBuilderState: f.builder.State(), traceStaleness: f.stale}
}

// traceStaleness tells whether a shared trace is behind its session: lastActivity is the newest
// timestamp of an event the session itself produced, and lastShareRecord the newest point a
// session.shared record covered (its shared_through, else its timestamp); a shared trace whose
// session has activity after that is stale. share tells the share's own events (a share made from
// inside the session) from activity.
type traceStaleness struct {
	LastActivity    string        `json:"last_activity,omitempty"`
	LastShareRecord string        `json:"last_share_record,omitempty"`
	Share           shareActivity `json:"share,omitempty"`
}

func (s *traceStaleness) observe(event schema.Event) {
	if event.Event.Action == "session.shared" {
		// What the upload covered (shared_through, when the session was read for it), or the
		// record's own time for a record from before that field.
		s.LastShareRecord = laterTimestamp(s.LastShareRecord, firstNonEmpty(rawString(event.Raw, "shared_through"), event.Timestamp))
	}
	if s.Share.counts(event) {
		s.LastActivity = laterTimestamp(s.LastActivity, event.Timestamp)
	}
}

// apply marks a shared summary stale when the session has activity after what the share covered,
// and gives it the refresh command a reader can copy.
func (s traceStaleness) apply(summary *TraceSummaryV1) {
	if summary.Sharing.URL == "" {
		return
	}
	if s.LastShareRecord != "" && s.LastActivity != "" && laterTimestamp(s.LastShareRecord, s.LastActivity) != s.LastShareRecord {
		summary.Sharing.State = "stale"
	}
	if summary.Session != nil {
		summary.Sharing.RefreshCommand = asymptoteobserve.ShareRefreshCommand(summary.Harness.Name, summary.Session.ID)
	}
}

// shareBookkeeping is an event Beacon writes into a session's timeline about the session, rather
// than activity of the session: it never makes a shared trace stale.
func shareBookkeeping(action string) bool {
	return action == "session.shared" || action == "session.share_requested" || action == "session.commit_linked"
}

func laterTimestamp(a, b string) string {
	if b == "" {
		return a
	}
	if a == "" {
		return b
	}
	ta, errA := schema.ParseTimestamp(a)
	tb, errB := schema.ParseTimestamp(b)
	if errA != nil {
		return b
	}
	if errB != nil || !tb.After(ta) {
		return a
	}
	return b
}

// The local history, once the user has opted in to it, answers the same
// question the JSONL scan below answers, over a longer window: it keeps what
// rotation deletes. Any error -- no history, a query it does not support, an
// unreadable database -- falls back to the scan rather than failing the read.
// The two paths share one free-text matcher (matchesAllTerms), one pair of
// haystacks and one event projection so the fallback cannot change the answer.
func ReadTraceList(path string, query TraceQuery) (TraceListResultV1, error) {
	if result, err := historyList(path, query); err == nil {
		return result, nil
	}
	traces, err := readTraceAggregates(path, withoutFreeText(query.EventQuery))
	if err != nil {
		return TraceListResultV1{}, err
	}
	filtered := filterTraceSummaries(traces, query)
	if needle := strings.TrimSpace(query.Q); needle != "" {
		matched := make([]TraceSummaryV1, 0, len(filtered))
		for _, summary := range filtered {
			if traceAggregateMatches(traces[summary.ID], needle) {
				matched = append(matched, summary)
			}
		}
		filtered = matched
	}
	sortTraceSummaries(filtered)
	limit := normalizeLimit(query.Limit)
	page := query.Page
	if page <= 0 {
		page = 1
	}
	// Clamp before multiplying: a valid positive page can overflow an int.
	start := len(filtered)
	if page-1 <= len(filtered)/limit {
		start = (page - 1) * limit
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

// SearchTraces prefers the local store and falls back to scanning JSONL; see
// ReadTraceList on why a store error is not an error here.
func SearchTraces(path string, query TraceQuery) (TraceSearchResultV1, error) {
	if result, err := historySearch(path, query); err == nil {
		return result, nil
	}
	if query.ResultLevel == "" {
		query.ResultLevel = "trace"
	}
	limit := normalizeLimit(query.Limit)
	traces, err := readTraceAggregates(path, withoutFreeText(query.EventQuery))
	if err != nil {
		return TraceSearchResultV1{}, err
	}
	filtered := filterTraceSummaries(traces, query)
	sortTraceSummaries(filtered)
	needle := strings.TrimSpace(query.Q)
	resp := TraceSearchResultV1{ResultLevel: query.ResultLevel, Limit: limit, Filters: activeTraceFilters(query)}
	switch query.ResultLevel {
	case "event":
		for _, summary := range filtered {
			agg := traces[summary.ID]
			events := filterTraceEvents(agg.events, query.EventTypes)
			for _, event := range events {
				if needle != "" && !traceEventMatches(event, needle) {
					continue
				}
				resp.TotalMatched++
				if len(resp.Events) < limit {
					resp.Events = append(resp.Events, TraceEventMatchV1{
						Trace:   agg.summary,
						Event:   event,
						Snippet: traceSnippet(event, needle),
						Score:   1,
					})
				}
			}
		}
	default:
		resp.ResultLevel = "trace"
		matched := make([]TraceSummaryV1, 0, len(filtered))
		for _, summary := range filtered {
			if needle == "" || traceSummaryMatches(summary, needle) {
				matched = append(matched, summary)
			}
		}
		sortTraceSummaries(matched)
		resp.TotalMatched = len(matched)
		if len(matched) > limit {
			resp.Traces = matched[:limit]
		} else {
			resp.Traces = matched
		}
	}
	if resp.ResultLevel == "event" {
		resp.Returned = len(resp.Events)
	} else {
		resp.Returned = len(resp.Traces)
	}
	return resp, nil
}

// ShowTrace prefers the local store and falls back to scanning JSONL; see
// ReadTraceList on why a store error is not an error here.
func ShowTrace(path, id string, query TraceQuery) (TraceShowResultV1, bool, error) {
	if result, ok, err := historyShow(path, id, query); err == nil {
		return result, ok, nil
	}
	traces, err := readTraceAggregates(path, withoutFreeText(query.EventQuery))
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	agg := traces[id]
	if agg == nil {
		return TraceShowResultV1{}, false, nil
	}
	events := filterTraceEvents(agg.events, query.EventTypes)
	total := len(events)
	offset, limit := traceShowRange(query, total, func(i int) int { return events[i].Number })
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	if offset-1 > end {
		end = offset - 1
	}
	resp := TraceShowResultV1{
		Trace:  agg.summary,
		Events: append([]TraceEventV1(nil), events[offset-1:end]...),
		Spans:  asymptoteobserve.TraceSpans(events),
		Range: TraceRangeV1{
			TotalEvents:    total,
			ReturnedEvents: end - (offset - 1),
			Offset:         offset,
			Limit:          limit,
			AroundEvent:    query.AroundEvent,
		},
	}
	return resp, true, nil
}

func TraceBundleFromShow(show TraceShowResultV1, beaconVersion string) TraceBundleV1 {
	return TraceBundleV1{
		SchemaVersion: TraceSchemaVersion,
		ID:            show.Trace.ID,
		Summary:       show.Trace,
		Events:        show.Events,
		Spans:         show.Spans,
		Range:         &show.Range,
		Provenance: TraceProvenanceV1{
			Source:        "runtime_jsonl",
			GeneratedAt:   asymptoteobserve.FormatTimestamp(time.Now()),
			BeaconVersion: beaconVersion,
		},
	}
}

func readTraceAggregates(path string, query EventQuery) (map[string]*traceAggregate, error) {
	query.NoLimit = true
	result, err := ReadEvents(path, query)
	if err != nil {
		return nil, err
	}
	SortRecordsAppendOrder(result.Events)
	folds := map[string]*traceFold{}
	for _, record := range result.Events {
		event := record.Event
		id := traceProjectionID(event)
		fold := folds[id]
		if fold == nil {
			fold = newTraceFold(id, event)
			folds[id] = fold
		}
		fold.add(event, traceEventFromRecord(record, 0))
	}
	traces := make(map[string]*traceAggregate, len(folds))
	for id, fold := range folds {
		traces[id] = &traceAggregate{summary: fold.summary(), events: fold.builder.Events()}
	}
	return traces, nil
}

// traceEventFromRecord is the trace projection of one log record; the line's ID names an event
// that carries no event.id.
func traceEventFromRecord(record EventRecord, number int) TraceEventV1 {
	return asymptoteobserve.TraceEventFromEvent(record.Event, record.ID, number)
}

func traceProjectionID(event schema.Event) string {
	return asymptoteobserve.TraceProjectionID(event)
}

// RetainedOutputText is asymptoteobserve.RetainedOutputText, for readers that reach it through
// the dashboard, such as the handoff brief.
func RetainedOutputText(genAI *schema.GenAIInfo, partType string) string {
	return asymptoteobserve.RetainedOutputText(genAI, partType)
}

// sharingStateMatches is the --state filter. "stale" is a kind of "shared" (published, with newer
// local activity), so asking for shared sessions includes stale ones and "stale" narrows to them.
// The history query (sharingCondition) applies the same rule.
func sharingStateMatches(state, want string) bool {
	if strings.EqualFold(want, "shared") {
		return strings.EqualFold(state, "shared") || strings.EqualFold(state, "stale")
	}
	return strings.EqualFold(state, want)
}

func filterTraceSummaries(traces map[string]*traceAggregate, query TraceQuery) []TraceSummaryV1 {
	out := make([]TraceSummaryV1, 0, len(traces))
	for _, agg := range traces {
		if query.State != "" && !sharingStateMatches(agg.summary.Sharing.State, query.State) {
			continue
		}
		if query.Visibility != "" && !strings.EqualFold(agg.summary.Sharing.Visibility, query.Visibility) {
			continue
		}
		out = append(out, agg.summary)
	}
	return out
}

// traceAggregateMatches keeps a trace whose summary or any of its events match
// the free-text query. Matching the summary alone would hide a trace whose only
// mention of the term is in a command, a path, or a message -- the events the
// list is built from but does not show.
func traceAggregateMatches(agg *traceAggregate, needle string) bool {
	if agg == nil {
		return false
	}
	if traceSummaryMatches(agg.summary, needle) {
		return true
	}
	for _, event := range agg.events {
		if traceEventMatches(event, needle) {
			return true
		}
	}
	return false
}

func sortTraceSummaries(traces []TraceSummaryV1) {
	sort.SliceStable(traces, func(i, j int) bool {
		it, _ := schema.ParseTimestamp(traces[i].UpdatedAt)
		jt, _ := schema.ParseTimestamp(traces[j].UpdatedAt)
		if !it.Equal(jt) {
			return it.After(jt)
		}
		return traces[i].ID < traces[j].ID
	})
}

func filterTraceEvents(events []TraceEventV1, eventTypes []string) []TraceEventV1 {
	if len(eventTypes) == 0 {
		return events
	}
	allowed := traceEventTypeSet(eventTypes)
	out := make([]TraceEventV1, 0, len(events))
	for _, event := range events {
		if allowed[strings.ToLower(event.Type)] || allowed[traceEventTypeAlias(event.Type)] {
			out = append(out, event)
		}
	}
	return out
}

// traceShowRange keeps pagination in filtered coordinates, but around-event names
// an original event number. If that event is filtered out, its insertion point
// separates the matching events before and after it without adding an anchor slot.
func traceShowRange(query TraceQuery, total int, numberAt func(int) int) (int, int) {
	if query.AroundEvent <= 0 || len(query.EventTypes) == 0 {
		return traceRange(query, total)
	}
	before, after := aroundContext(query)
	anchor := sort.Search(total, func(i int) bool { return numberAt(i) >= query.AroundEvent })
	start := anchor - before
	if start < 0 {
		start = 0
	}
	end := anchor
	if anchor < total && numberAt(anchor) == query.AroundEvent {
		end++
	}
	if after > total-end {
		after = total - end
	}
	end += after
	return start + 1, end - start
}

// aroundContext is how many events --around-event shows on each side, 3 unless set.
func aroundContext(query TraceQuery) (before, after int) {
	before, after = query.Before, query.After
	if before <= 0 {
		before = 3
	}
	if after <= 0 {
		after = 3
	}
	return before, after
}

func traceRange(query TraceQuery, total int) (int, int) {
	limit := normalizeLimit(query.Limit)
	offset := query.Offset
	if offset <= 0 {
		offset = 1
	}
	if query.NoLimit {
		// Lens data needs the whole trace in one read; the size budget is applied by its caller.
		limit = total
	}
	if query.AroundEvent > 0 {
		before, after := aroundContext(query)
		offset = query.AroundEvent - before
		if offset < 1 {
			offset = 1
		}
		limit = min(before, total) + 1 + min(after, total)
	}
	if total == 0 {
		return 1, 0
	}
	if offset > total {
		offset = total + 1
		return offset, 0
	}
	if limit > total-offset+1 {
		limit = total - offset + 1
	}
	return offset, limit
}

// traceSummaryHaystack is the text a free-text query is matched against for one
// trace. The local store indexes this exact string, so the store and the JSONL
// fallback select the same traces from the same query.
//
// Fields are joined with a newline rather than concatenated: strings.Fields
// splits the query on whitespace, so no term can contain one, and no term can
// straddle two fields.
func traceSummaryHaystack(summary TraceSummaryV1) string {
	return strings.Join([]string{
		summary.ID,
		summary.Title,
		summary.Preview,
		summary.Harness.Name,
		summary.Harness.Version,
		valueOrEmpty(summary.Session, func(s *TraceSessionV1) string { return s.ID + " " + s.WorkingDirectory }),
		valueOrEmpty(summary.Repository, func(r *TraceRepositoryV1) string { return r.RemoteURL + " " + r.Branch + " " + r.Ref + " " + r.Path }),
		valueOrEmpty(summary.Namespace, func(n *TraceNamespaceV1) string { return n.ID + " " + n.Slug + " " + n.Name }),
		summary.Sharing.State,
		summary.Sharing.Visibility,
		summary.Sharing.URL,
	}, "\n")
}

func traceSummaryMatches(summary TraceSummaryV1, query string) bool {
	return matchesAllTerms(traceSummaryHaystack(summary), query)
}

// traceEventHaystack is the per-event counterpart to traceSummaryHaystack, and
// is indexed by the local store for the same reason.
func traceEventHaystack(event TraceEventV1) string {
	return strings.Join([]string{
		event.ID,
		event.Type,
		event.Action,
		event.Category,
		event.Actor,
		event.Title,
		event.Summary,
		event.Model,
		valueOrEmpty(event.Content, func(c *TraceContentV1) string { return c.Text + " " + c.Hash }),
		valueOrEmpty(event.Tool, func(t *TraceToolV1) string {
			return t.Name + " " + t.Command + " " + t.Path + " " + fmt.Sprint(t.Arguments) + " " + fmt.Sprint(t.Result)
		}),
		valueOrEmpty(event.Command, func(c *TraceCommandV1) string {
			return c.Command + " " + valueOrEmpty(c.Output, func(o *TraceContentV1) string { return o.Text })
		}),
		valueOrEmpty(event.File, func(f *TraceFileV1) string {
			return f.Path + " " + f.Operation + " " + f.Language + " " + f.DiffHash + " " + valueOrEmpty(f.Diff, func(d *TraceContentV1) string { return d.Text })
		}),
		valueOrEmpty(event.MCP, func(m *TraceMCPV1) string { return m.Server + " " + m.Tool + " " + m.Method + " " + m.ResourceURI }),
		valueOrEmpty(event.Approval, func(a *TraceApprovalV1) string { return a.Decision + " " + a.Reason }),
		valueOrEmpty(event.Policy, func(p *TracePolicyV1) string {
			return p.ID + " " + p.Name + " " + p.Decision + " " + p.Enforcement + " " + p.Reason
		}),
		valueOrEmpty(event.Error, func(e *TraceErrorV1) string { return e.Type }),
	}, "\n")
}

func traceEventMatches(event TraceEventV1, query string) bool {
	return matchesAllTerms(traceEventHaystack(event), query)
}

// matchesAllTerms reports whether every whitespace-separated term in query is a
// case-insensitive substring of haystack. This is the one free-text matcher
// Beacon's trace queries use; the store must not reinterpret a query as
// anything else, or the same search would return different hits depending on
// whether the index happened to be readable.
func matchesAllTerms(haystack, query string) bool {
	haystack = strings.ToLower(haystack)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func traceSnippet(event TraceEventV1, query string) string {
	candidates := []string{event.Summary, event.Title}
	if event.Content != nil {
		candidates = append([]string{event.Content.Text}, candidates...)
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(candidate), strings.ToLower(strings.Fields(query)[0])) {
			return asymptoteobserve.TruncateUTF8(strings.TrimSpace(candidate), 240)
		}
	}
	return ""
}

func activeTraceFilters(query TraceQuery) map[string]string {
	filters := activeFilters(query.EventQuery)
	if filters == nil {
		filters = map[string]string{}
	}
	add := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			filters[key] = strings.TrimSpace(value)
		}
	}
	add("state", query.State)
	add("visibility", query.Visibility)
	add("result_level", query.ResultLevel)
	if len(query.EventTypes) > 0 {
		filters["event_type"] = strings.Join(query.EventTypes, ",")
	}
	if len(filters) == 0 {
		return nil
	}
	return filters
}

func ParseTraceQuery(r *http.Request, fallbackLimit int) TraceQuery {
	q := r.URL.Query()
	query := TraceQuery{
		EventQuery:  parseQuery(r, fallbackLimit),
		State:       q.Get("state"),
		Visibility:  q.Get("visibility"),
		ResultLevel: q.Get("result_level"),
		EventTypes:  splitCSV(q.Get("event_type")),
	}
	query.Limit = query.EventQuery.Limit
	query.Page, _ = strconv.Atoi(q.Get("page"))
	query.Offset, _ = strconv.Atoi(q.Get("offset"))
	query.AroundEvent, _ = strconv.Atoi(q.Get("around_event"))
	query.Before, _ = strconv.Atoi(q.Get("before"))
	query.After, _ = strconv.Atoi(q.Get("after"))
	return query
}

func withoutFreeText(query EventQuery) EventQuery {
	query.Q = ""
	return query
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func valueOrEmpty[T any](value *T, render func(*T) string) string {
	if value == nil {
		return ""
	}
	return render(value)
}
