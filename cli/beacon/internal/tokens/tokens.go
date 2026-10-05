// Package tokens aggregates token usage and runtime-reported cost from
// Beacon endpoint JSONL events into attribution rollups: totals, per-model,
// per-session, per-harness, per-repository, per-CI-run, time series,
// context-window utilization, and per-step session detail.
package tokens

import (
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const defaultNearLimitRatio = 0.8

// Usage sums the canonical gen_ai.usage fields across events. Field names
// match the event schema so report consumers see one vocabulary.
type Usage struct {
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	ReasoningOutputTokens    int64   `json:"reasoning_output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
	Events                   int     `json:"events"`
}

func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

func (u *Usage) add(delta Usage) {
	u.InputTokens += delta.InputTokens
	u.OutputTokens += delta.OutputTokens
	u.CacheReadInputTokens += delta.CacheReadInputTokens
	u.CacheCreationInputTokens += delta.CacheCreationInputTokens
	u.ReasoningOutputTokens += delta.ReasoningOutputTokens
	u.CostUSD += delta.CostUSD
	u.Events += delta.Events
}

type Group struct {
	Key   string `json:"key"`
	Usage Usage  `json:"usage"`
}

// Step is one usage-bearing event inside a session. Steps with span identity
// nest under their parent span; steps without span identity stay flat in
// time order.
type Step struct {
	Timestamp    string  `json:"timestamp,omitempty"`
	Action       string  `json:"action,omitempty"`
	Name         string  `json:"name,omitempty"`
	Model        string  `json:"model,omitempty"`
	TraceID      string  `json:"trace_id,omitempty"`
	SpanID       string  `json:"span_id,omitempty"`
	ParentSpanID string  `json:"parent_span_id,omitempty"`
	Usage        Usage   `json:"usage"`
	Children     []*Step `json:"children,omitempty"`
}

type SessionDetail struct {
	SessionID string  `json:"session_id"`
	Usage     Usage   `json:"usage"`
	Steps     []*Step `json:"steps,omitempty"`
}

// ModelUtilization summarizes how close a model's calls run to its context
// window. Utilization samples sum input, cache read, and cache creation
// tokens per (harness, session, model, timestamp) group so Claude Code's
// per-type metric datapoints recombine into one per-interval context size.
type ModelUtilization struct {
	Model          string  `json:"model"`
	ContextWindow  int64   `json:"context_window,omitempty"`
	Calls          int     `json:"calls"`
	MaxInputTokens int64   `json:"max_input_tokens"`
	MaxRatio       float64 `json:"max_ratio,omitempty"`
	P95Ratio       float64 `json:"p95_ratio,omitempty"`
	NearLimitCalls int     `json:"near_limit_calls,omitempty"`
}

type TimeBucket struct {
	Start string `json:"start"`
	Usage Usage  `json:"usage"`
}

type Options struct {
	// BucketSize controls the time-series granularity. Zero disables the series.
	BucketSize time.Duration
	// NearLimitRatio marks utilization samples at or above this fraction of the
	// model context window. Defaults to 0.8.
	NearLimitRatio float64
	// SessionID selects one session for per-step detail.
	SessionID string
	// TopLimit caps each group list (0 keeps all groups).
	TopLimit int
}

type Report struct {
	Totals          Usage              `json:"totals"`
	ByModel         []Group            `json:"by_model,omitempty"`
	BySession       []Group            `json:"by_session,omitempty"`
	ByUser          []Group            `json:"by_user,omitempty"`
	ByHarness       []Group            `json:"by_harness,omitempty"`
	ByRepository    []Group            `json:"by_repository,omitempty"`
	ByRun           []Group            `json:"by_run,omitempty"`
	Utilization     []ModelUtilization `json:"utilization,omitempty"`
	Series          []TimeBucket       `json:"series,omitempty"`
	SessionDetail   *SessionDetail     `json:"session_detail,omitempty"`
	EventsWithUsage int                `json:"events_with_usage"`
	TotalEvents     int                `json:"total_events"`
	// SuppressedPollEvents counts poll-backfill usage events (collection_method=poll) dropped
	// because a live channel had already reported the same session's usage from that point on.
	// They are left out of every total and of EventsWithUsage; the count is here so a reader can
	// see the overlap was found rather than wonder where a backfill's rows went.
	SuppressedPollEvents int `json:"suppressed_poll_events,omitempty"`
}

// usageEvent is one usage-bearing event with its usage normalized to a delta
// contribution and its attribution keys extracted.
type usageEvent struct {
	ts           time.Time
	order        int
	action       string
	name         string
	endpoint     string
	harness      string
	session      string
	user         string
	model        string
	repository   string
	run          string
	traceID      string
	spanID       string
	parentSpanID string
	usage        Usage
	// contextOnly marks an event that reached the collector only because it reported context
	// occupancy. It feeds the utilization report and nothing else: it is not spend, so it must
	// not appear in a total, a group, a time bucket, or the count of usage-bearing events.
	contextOnly  bool
	contextUsed  int64
	contextLimit int64
	cumulative   bool
	metricName   string
	rawSource    string
	sourceStart  time.Time
	seriesField  string
	seriesValue  float64

	// collectionMethod is harness.collection_method, lowercased: whether this usage came from
	// live capture or from a poll of the runtime's own session store.
	collectionMethod string
}

// Aggregate builds a token usage report from endpoint events. Events from
// cumulative metric series are converted to per-interval deltas (grouped by
// harness, session, model, metric name, and usage field; counter resets fall
// back to the raw value) so totals never double-count. Delta metrics and
// span-level usage sum directly.
//
// Events should be supplied in chronological (log append) order. Runtime
// timestamps are second-resolution, so a batch of cumulative datapoints from
// one export commonly shares a timestamp; cumulative deduping then relies on
// slice order to recover the emission sequence. Passing events newest-first
// makes each cumulative step-down look like a counter reset and inflates totals.
func Aggregate(events []schema.Event, opts Options) Report {
	return aggregate(events, opts, events)
}

// aggregate reports on events. contextEvents is the unfiltered set the session-level context is
// read from -- who the session's user is, and when each live channel started capturing it -- so a
// report narrowed by time or model still resolves both the way the whole log would.
func aggregate(events []schema.Event, opts Options, contextEvents []schema.Event) Report {
	if opts.NearLimitRatio <= 0 {
		opts.NearLimitRatio = defaultNearLimitRatio
	}
	report := Report{TotalEvents: len(events)}
	usageEvents, suppressed := resolveUsageEvents(events, sessionUserContexts(contextEvents), liveCaptureStarts(contextEvents, events))
	report.SuppressedPollEvents = suppressed

	byModel := map[string]*Usage{}
	bySession := map[string]*Usage{}
	byUser := map[string]*Usage{}
	byHarness := map[string]*Usage{}
	byRepository := map[string]*Usage{}
	byRun := map[string]*Usage{}
	buckets := map[time.Time]*Usage{}
	eventsWithUsage := 0
	for _, ue := range usageEvents {
		if ue.contextOnly {
			// Context occupancy is not spend. Counting these would put zero-token rows under
			// every grouping and report a runtime that spends nothing as usage-bearing -- which
			// the coverage report reads as "this runtime is reporting its spend".
			continue
		}
		report.Totals.add(ue.usage)
		addGroup(byModel, ue.model, ue.usage)
		addGroup(bySession, ue.session, ue.usage)
		addGroup(byUser, ue.user, ue.usage)
		addGroup(byHarness, ue.harness, ue.usage)
		addGroup(byRepository, ue.repository, ue.usage)
		addGroup(byRun, ue.run, ue.usage)
		if opts.BucketSize > 0 && !ue.ts.IsZero() {
			start := ue.ts.Truncate(opts.BucketSize)
			if buckets[start] == nil {
				buckets[start] = &Usage{}
			}
			buckets[start].add(ue.usage)
		}
		eventsWithUsage++
	}
	report.EventsWithUsage = eventsWithUsage
	report.ByModel = sortedGroups(byModel, opts.TopLimit)
	report.BySession = sortedGroups(bySession, opts.TopLimit)
	report.ByUser = sortedGroups(byUser, opts.TopLimit)
	report.ByHarness = sortedGroups(byHarness, opts.TopLimit)
	report.ByRepository = sortedGroups(byRepository, opts.TopLimit)
	report.ByRun = sortedGroups(byRun, opts.TopLimit)
	report.Utilization = buildUtilization(usageEvents, opts.NearLimitRatio)
	report.Series = sortedBuckets(buckets)
	if session := strings.TrimSpace(opts.SessionID); session != "" {
		report.SessionDetail = buildSessionDetail(usageEvents, session)
	}
	return report
}

// AggregateScoped narrows events to a single session and optional CI run before
// aggregating. The session filter uses opts.SessionID and matches exactly
// (case-insensitive) so totals and grouping stay aligned with the per-step
// drilldown, which keys on the exact session id; the broader substring match used
// by the event query is intentionally not reused here. When runID != "" only the
// matching run is kept, accepting either the bare run id or the composite
// provider/run_id key shown in the BY RUN rollup. Pass runID == "" to skip run
// filtering. Events should already be in chronological (append) order; see
// Aggregate.
func AggregateScoped(events []schema.Event, runID string, opts Options) Report {
	return AggregateScopedWithContexts(events, events, runID, opts)
}

// AggregateScopedWithContexts applies report filters to events while resolving
// user attribution from an unfiltered set of session context events. Callers
// that filter by time, model, or session before aggregation should use this
// form so a SessionStart outside the selected window can still identify usage.
func AggregateScopedWithContexts(events, contextEvents []schema.Event, runID string, opts Options) Report {
	session := strings.TrimSpace(opts.SessionID)
	runID = strings.TrimSpace(runID)
	if session == "" && runID == "" {
		return aggregate(events, opts, contextEvents)
	}
	filtered := make([]schema.Event, 0, len(events))
	for _, event := range events {
		if session != "" && (event.Session == nil || !strings.EqualFold(event.Session.ID, session)) {
			continue
		}
		if runID != "" {
			run := event.Run
			if run == nil || (run.RunID != runID && RunKey(run.Provider, run.RunID) != runID) {
				continue
			}
		}
		filtered = append(filtered, event)
	}
	return aggregate(filtered, opts, contextEvents)
}

// resolveUsageEvents is the one pipeline from raw events to the usage contributions every report
// sums, so the token report and the coverage report cannot disagree about what was counted. It
// returns the contributions and the number of poll events dropped as copies of live capture.
//
// Order matters. The Codex span preference and the live/poll preference each drop whole events
// before the log/metric dedupe, which reads what the surviving non-metric events report; a poll
// copy still present at that point would claim fields for the metric's whole session. All three
// run before cumulative resolution so a dropped event never seeds or breaks a counter series.
func resolveUsageEvents(events []schema.Event, sessionUsers sessionUserIndex, liveStarts liveCaptureIndex) ([]*usageEvent, int) {
	usageEvents := collectUsageEvents(events, sessionUsers)
	usageEvents = preferCodexTurnSpans(usageEvents)
	usageEvents, suppressed := preferLiveOverPoll(usageEvents, liveStarts)
	usageEvents = dedupeOverlappingChannels(usageEvents)
	resolveCumulativeSeries(usageEvents)
	return usageEvents, suppressed
}

type sessionContextKey struct {
	endpoint string
	harness  string
	session  string
}

type endpointContextKey struct {
	endpoint string
	harness  string
}

type endpointUserNameKey struct {
	endpoint string
	harness  string
	name     string
}

type sessionUserIndex struct {
	bySession      map[sessionContextKey]string
	byEndpoint     map[endpointContextKey]string
	byEndpointName map[endpointUserNameKey]string
}

func sessionUserContexts(events []schema.Event) sessionUserIndex {
	contexts := sessionUserIndex{
		bySession:      map[sessionContextKey]string{},
		byEndpoint:     map[endpointContextKey]string{},
		byEndpointName: map[endpointUserNameKey]string{},
	}
	ambiguousEndpoints := map[endpointContextKey]bool{}
	ambiguousNames := map[endpointUserNameKey]bool{}
	for _, event := range events {
		if event.Session == nil || strings.TrimSpace(event.Session.ID) == "" {
			continue
		}
		user := userKey(event.User)
		if user == "" {
			continue
		}
		key := sessionKey(event)
		if event.Event.Action == "session.context" || contexts.bySession[key] == "" {
			contexts.bySession[key] = user
		}
		if event.Event.Action != "session.context" {
			continue
		}
		endpointKey := endpointKey(event)
		if current := contexts.byEndpoint[endpointKey]; current == "" {
			if !ambiguousEndpoints[endpointKey] {
				contexts.byEndpoint[endpointKey] = user
			}
		} else if current != user {
			delete(contexts.byEndpoint, endpointKey)
			ambiguousEndpoints[endpointKey] = true
		}
		if name := strings.ToLower(strings.TrimSpace(event.User.Name)); name != "" {
			nameKey := endpointUserNameKey{endpoint: endpointKey.endpoint, harness: endpointKey.harness, name: name}
			if current := contexts.byEndpointName[nameKey]; current == "" {
				if !ambiguousNames[nameKey] {
					contexts.byEndpointName[nameKey] = user
				}
			} else if current != user {
				delete(contexts.byEndpointName, nameKey)
				ambiguousNames[nameKey] = true
			}
		}
	}
	return contexts
}

func endpointKey(event schema.Event) endpointContextKey {
	return endpointContextKey{
		endpoint: strings.ToLower(strings.TrimSpace(event.Endpoint.Hostname)),
		harness:  strings.ToLower(strings.TrimSpace(event.Harness.Name)),
	}
}

func sessionKey(event schema.Event) sessionContextKey {
	session := ""
	if event.Session != nil {
		session = event.Session.ID
	}
	endpoint := endpointKey(event)
	return sessionContextKey{
		endpoint: endpoint.endpoint,
		harness:  endpoint.harness,
		session:  strings.ToLower(strings.TrimSpace(session)),
	}
}

func userKey(user schema.UserInfo) string {
	name := strings.TrimSpace(user.Name)
	uid := strings.TrimSpace(user.UID)
	switch {
	case name != "" && uid != "":
		return name + " [" + uid + "]"
	case uid != "":
		return uid
	default:
		return name
	}
}

// modelDeclaration is one point at which a session named its model, kept with the position it was
// seen at so a later lookup can ask what was in force rather than what was in force last.
type modelDeclaration struct {
	order int
	model string
}

// sessionModelIndex records every model a session named, in event order. Qwen Code is why it
// exists: it declares its model on session start and never again, so its Stop payload -- the only
// event carrying gen_ai.context -- has a session id and no model. Utilization keys its rows on the
// model, so without this the occupancy Beacon just learned how to record is dropped before the
// report that wanted it.
type sessionModelIndex map[sessionContextKey][]modelDeclaration

func sessionModelDeclarations(events []schema.Event) sessionModelIndex {
	index := sessionModelIndex{}
	for i, event := range events {
		model := asymptoteobserve.NormalizeModelName(event.Model)
		if model == "" || event.Session == nil || strings.TrimSpace(event.Session.ID) == "" {
			continue
		}
		key := sessionKey(event)
		if declarations := index[key]; len(declarations) > 0 && declarations[len(declarations)-1].model == model {
			continue
		}
		index[key] = append(index[key], modelDeclaration{order: i, model: model})
	}
	return index
}

// modelAt returns the model the session had named by the given position. Scanning back to the last
// declaration at or before the event, rather than taking the session's newest one, keeps a session
// that switched models from having its earlier occupancy attributed to the later one.
func (index sessionModelIndex) modelAt(key sessionContextKey, order int) string {
	declarations := index[key]
	for i := len(declarations) - 1; i >= 0; i-- {
		if declarations[i].order <= order {
			return declarations[i].model
		}
	}
	return ""
}

func collectUsageEvents(events []schema.Event, sessionUsers sessionUserIndex) []*usageEvent {
	sessionModels := sessionModelDeclarations(events)
	var out []*usageEvent
	for i, event := range events {
		// Context-only events count too. A runtime can report how full the window was without
		// reporting spend -- Qwen Code does exactly that -- and those events carry the whole of
		// its context utilization. Gating on usage alone dropped them before they reached the
		// utilization report, which is the one place they belong.
		if event.GenAI == nil || (event.GenAI.Usage == nil && event.GenAI.Context == nil) {
			continue
		}
		usage := event.GenAI.Usage
		if usage == nil {
			usage = &schema.GenAIUsageInfo{}
		}
		ue := &usageEvent{
			order:    i,
			action:   event.Event.Action,
			name:     event.Message,
			endpoint: event.Endpoint.Hostname,
			harness:  event.Harness.Name,
			user:     userKey(event.User),
			// Canonicalized on the way in, so every consumer downstream -- the by-model rollup,
			// utilization, the coverage join, the Codex span preference that keys on model --
			// agrees about what one model is called, from one place.
			//
			// Every write path already does this (see normalizeEventModel in the hook logger and
			// SplitModelProvider in the collector exporter), so a log Beacon wrote recently is
			// already canonical and this is a no-op on it. It is here for the logs where that does
			// not hold: those written before that rule existed, which the runtime log retains and
			// a --since window spans, and any JSONL Beacon reads without having written it.
			//
			// Grouping only. The stored event is untouched, and the raw spelling a runtime used
			// stays in the log where an investigator can still see it.
			model:      asymptoteobserve.NormalizeModelName(event.Model),
			repository: event.Repository,
			usage:      Usage{Events: 1},
		}
		ue.collectionMethod = strings.ToLower(strings.TrimSpace(event.Harness.CollectionMethod))
		if ts, err := schema.ParseTimestamp(event.Timestamp); err == nil {
			ue.ts = ts
		}
		if event.Session != nil {
			ue.session = event.Session.ID
			if contextualUser := sessionUsers.bySession[sessionKey(event)]; contextualUser != "" {
				ue.user = contextualUser
			}
		}
		if ue.user == userKey(event.User) {
			endpoint := endpointKey(event)
			nameKey := endpointUserNameKey{
				endpoint: endpoint.endpoint,
				harness:  endpoint.harness,
				name:     strings.ToLower(strings.TrimSpace(event.User.Name)),
			}
			if namedUser := sessionUsers.byEndpointName[nameKey]; namedUser != "" {
				ue.user = namedUser
			} else if endpointUser := sessionUsers.byEndpoint[endpoint]; endpointUser != "" {
				ue.user = endpointUser
			}
		}
		if event.Run != nil {
			ue.run = RunKey(event.Run.Provider, event.Run.RunID)
		}
		if event.Trace != nil {
			ue.traceID = event.Trace.ID
			ue.spanID = event.Trace.SpanID
			ue.parentSpanID = event.Trace.ParentSpanID
		}
		if usage.InputTokens != nil {
			ue.usage.InputTokens = *usage.InputTokens
		}
		if usage.OutputTokens != nil {
			ue.usage.OutputTokens = *usage.OutputTokens
		}
		if usage.CacheRead != nil && usage.CacheRead.InputTokens != nil {
			ue.usage.CacheReadInputTokens = *usage.CacheRead.InputTokens
		}
		if usage.CacheCreation != nil && usage.CacheCreation.InputTokens != nil {
			ue.usage.CacheCreationInputTokens = *usage.CacheCreation.InputTokens
		}
		if usage.Reasoning != nil && usage.Reasoning.OutputTokens != nil {
			ue.usage.ReasoningOutputTokens = *usage.Reasoning.OutputTokens
		}
		if usage.CostUSD != nil {
			ue.usage.CostUSD = *usage.CostUSD
		}
		if context := event.GenAI.Context; context != nil {
			if context.UsedTokens != nil {
				ue.contextUsed = *context.UsedTokens
			}
			if context.LimitTokens != nil {
				ue.contextLimit = *context.LimitTokens
			}
		}
		if ue.usage.TotalTokens() == 0 && ue.usage.ReasoningOutputTokens == 0 && ue.usage.CostUSD == 0 {
			if ue.contextUsed == 0 {
				continue
			}
			// Kept for utilization, excluded from everything additive. A runtime that reports
			// both context and usage is not context-only and counts normally.
			//
			// Events is zeroed as well as the event being skipped, so the struct cannot be
			// added to a total by some later path that does not know to check the flag.
			ue.contextOnly = true
			ue.usage.Events = 0
			if ue.model == "" && event.Session != nil {
				// Attribution only, and only here. An event reporting real spend without a model
				// of its own stays unattributed as it always has: relabelling that would move
				// other runtimes' spend between models on the strength of a guess. Occupancy has
				// no such risk -- it is never summed -- and without a model it is not reportable
				// at all.
				ue.model = sessionModels.modelAt(sessionKey(event), i)
			}
		}
		if event.Raw != nil {
			if temporality, _ := event.Raw["metric_temporality"].(string); strings.EqualFold(temporality, "cumulative") {
				ue.cumulative = true
			}
			ue.metricName, _ = event.Raw["metric_name"].(string)
			ue.rawSource, _ = event.Raw["source"].(string)
			if rawStart, _ := event.Raw["turn_start_timestamp"].(string); rawStart != "" {
				ue.sourceStart, _ = schema.ParseTimestamp(rawStart)
			}
		}
		out = append(out, ue)
	}
	return out
}

// preferCodexTurnSpans makes the attributable turn trace authoritative from
// the first time it appears for an endpoint/model. Legacy aggregate metrics
// before that cutover remain reportable, while a manually configured
// trace+metric overlap after it cannot double-count the same turns.
func preferCodexTurnSpans(events []*usageEvent) []*usageEvent {
	type sourceKey struct {
		endpoint string
		harness  string
		model    string
	}
	keyFor := func(event *usageEvent) sourceKey {
		return sourceKey{
			endpoint: strings.ToLower(strings.TrimSpace(event.endpoint)),
			harness:  strings.ToLower(strings.TrimSpace(event.harness)),
			model:    strings.ToLower(strings.TrimSpace(event.model)),
		}
	}
	cutovers := map[sourceKey]time.Time{}
	for _, event := range events {
		if !strings.EqualFold(event.rawSource, "codex_turn_span") || event.ts.IsZero() {
			continue
		}
		key := keyFor(event)
		cutover := event.sourceStart
		if cutover.IsZero() {
			cutover = event.ts
		}
		if current, ok := cutovers[key]; !ok || cutover.Before(current) {
			cutovers[key] = cutover
		}
	}
	if len(cutovers) == 0 {
		return events
	}
	out := events[:0]
	for _, event := range events {
		cutover, ok := cutovers[keyFor(event)]
		if ok &&
			strings.EqualFold(event.metricName, "codex.turn.token_usage") &&
			!event.ts.IsZero() &&
			!event.ts.Before(cutover) {
			continue
		}
		out = append(out, event)
	}
	return out
}

// liveScope is one session on one endpoint, the unit within which a live report and a poll
// report of the same usage can be recognized as the same. The harness is normalized so the
// spellings a runtime's OTLP resource, hook adapter and session store use land on one key.
type liveScope struct {
	endpoint string
	harness  string
	session  string
}

// liveScopeFor returns the scope for an event, or false when the event names no session. Two
// session-less reports are not evidence of one turn -- they could be any two sessions on the
// machine -- so they never take part in the live/poll preference.
func liveScopeFor(endpoint, harness, session string) (liveScope, bool) {
	session = strings.ToLower(strings.TrimSpace(session))
	if session == "" {
		return liveScope{}, false
	}
	return liveScope{
		endpoint: strings.ToLower(strings.TrimSpace(endpoint)),
		harness:  asymptoteobserve.NormalizeHarnessName(harness),
		session:  session,
	}, true
}

// isLiveCollectionMethod reports whether a collection method observed the runtime as it worked.
// An unset method is not live: it is a log written before provenance existed, or an event Beacon
// emitted about itself, and neither is evidence that a live channel covered the session.
func isLiveCollectionMethod(method string) bool {
	switch method {
	case schema.CollectionMethodHook, schema.CollectionMethodOTLP, schema.CollectionMethodPlugin:
		return true
	}
	return false
}

// liveCaptureIndex records, per session scope and live collection method, the earliest timestamp
// any event arrived over that method -- whether or not the event carried usage.
type liveCaptureIndex map[liveScope]map[string]time.Time

// LiveCaptureKey names the (endpoint, harness, session, live collection method) an event marks the
// start of, and reports false for an event that marks none: one without a session, a parseable
// timestamp, or a live collection method. The live/poll cutover is the earliest such event per
// key, so a reader that hands the aggregator a reduced context set (see
// dashboard.ReadTokenEventsAppendOrder) needs to keep only the earliest event per key, not every
// live event in the log.
func LiveCaptureKey(event schema.Event) (string, time.Time, bool) {
	method := strings.ToLower(strings.TrimSpace(event.Harness.CollectionMethod))
	if !isLiveCollectionMethod(method) || event.Session == nil {
		return "", time.Time{}, false
	}
	scope, ok := liveScopeFor(event.Endpoint.Hostname, event.Harness.Name, event.Session.ID)
	if !ok {
		return "", time.Time{}, false
	}
	ts, err := schema.ParseTimestamp(event.Timestamp)
	if err != nil || ts.IsZero() {
		return "", time.Time{}, false
	}
	return strings.Join([]string{scope.endpoint, scope.harness, scope.session, method}, "\x00"), ts, true
}

// liveCaptureStarts reads the cutover inputs from every source it is given. aggregate passes both
// the report's own events and its context events: the context set alone is not enough, because
// a caller may hand over only session.context rows, and the matched events alone are not enough
// either, because a time window can start after a session's first live event.
func liveCaptureStarts(sources ...[]schema.Event) liveCaptureIndex {
	index := liveCaptureIndex{}
	for _, events := range sources {
		for _, event := range events {
			if _, ts, ok := LiveCaptureKey(event); ok {
				scope, _ := liveScopeFor(event.Endpoint.Hostname, event.Harness.Name, event.Session.ID)
				method := strings.ToLower(strings.TrimSpace(event.Harness.CollectionMethod))
				if index[scope] == nil {
					index[scope] = map[string]time.Time{}
				}
				if current, seen := index[scope][method]; !seen || ts.Before(current) {
					index[scope][method] = ts
				}
			}
		}
	}
	return index
}

// preferLiveOverPoll makes live capture authoritative over a poll backfill of the same session.
//
// Several runtimes are read twice: live, over OTLP or a hook or a Beacon-managed plugin, and
// after the fact by `beacon endpoint <runtime> sync`, which reads the session store the runtime
// commits to disk (collection_method=poll). Claude Code's api_request log and its transcript,
// Codex's turn span and its rollout file, and the Cline, OpenCode, OpenClaw and Pi plugins and
// their session stores each report one request's usage twice under one session id. Nothing at
// write time collapses the two -- a usage event carries no tool-call id, so each path's event.id
// is a digest of its own line -- so without this step every such session counts double.
//
// Per session scope where live events report usage, the cutover is the moment the live channel
// that reported it started capturing the session: the earliest event of any kind over that
// collection method (the unfiltered liveStarts), or the earliest live usage event or Codex turn
// start if that is sooner. It is deliberately not the first live usage timestamp. A live usage
// record is written when its request completes, while the session store stamps the same request
// when it began or as it streamed, so a cutover on the live record would leave every session's
// first request on both sides of it. The earliest same-channel event -- Claude's user_prompt log,
// a plugin's session start -- precedes the first request instead.
//
// Poll usage at or after the cutover gives up the fields the live channel reports in that scope
// and is dropped once nothing is left, the same field-wise rule the log/metric dedupe uses: a
// live channel that reports only cost (Claude Code's cost.usage metric without its token.usage
// sibling) leaves the backfill's tokens standing, and one that reports tokens without cost leaves
// the backfill's cost, rather than either being lost. Poll usage before the
// cutover is the only record of turns from before Beacon's live capture started and is kept, as
// is any poll event with no usable timestamp, which cannot be placed against the cutover.
//
// Context-only live events do not count: occupancy is not spend, so a live channel that reports
// only how full the window was is no authority on what the session cost.
func preferLiveOverPoll(events []*usageEvent, liveStarts liveCaptureIndex) ([]*usageEvent, int) {
	type liveCoverage struct {
		fields  usageFieldSet
		methods map[string]bool
		cutover time.Time
	}
	earlier := func(current, candidate time.Time) time.Time {
		if candidate.IsZero() || (!current.IsZero() && !candidate.Before(current)) {
			return current
		}
		return candidate
	}
	coverage := map[liveScope]*liveCoverage{}
	hasPoll := false
	for _, ue := range events {
		if ue.contextOnly {
			continue
		}
		if ue.collectionMethod == schema.CollectionMethodPoll {
			hasPoll = true
			continue
		}
		if !isLiveCollectionMethod(ue.collectionMethod) {
			continue
		}
		scope, ok := liveScopeFor(ue.endpoint, ue.harness, ue.session)
		if !ok {
			continue
		}
		c := coverage[scope]
		if c == nil {
			c = &liveCoverage{methods: map[string]bool{}}
			coverage[scope] = c
		}
		c.fields.mark(ue.usage)
		c.methods[ue.collectionMethod] = true
		c.cutover = earlier(c.cutover, ue.ts)
		c.cutover = earlier(c.cutover, ue.sourceStart)
	}
	if !hasPoll || len(coverage) == 0 {
		return events, 0
	}
	for scope, c := range coverage {
		for method := range c.methods {
			c.cutover = earlier(c.cutover, liveStarts[scope][method])
		}
	}
	suppressed := 0
	out := events[:0]
	for _, ue := range events {
		if ue.collectionMethod == schema.CollectionMethodPoll && !ue.contextOnly && !ue.ts.IsZero() {
			if scope, ok := liveScopeFor(ue.endpoint, ue.harness, ue.session); ok {
				if c := coverage[scope]; c != nil && c.fields.any() && !c.cutover.IsZero() && !ue.ts.Before(c.cutover) {
					if !c.fields.clear(&ue.usage) {
						suppressed++
						continue
					}
				}
			}
		}
		out = append(out, ue)
	}
	return out, suppressed
}

// dedupeOverlappingChannels removes double-counted usage when a runtime reports
// the same tokens through two OTel channels. Claude Code emits each request's
// usage on both a claude_code.api_request log record and the
// claude_code.token.usage metric, so ingesting both doubles every token field.
//
// The log/span channel (events without a metric_name) is the token source of
// truth: it carries full per-request usage under the base model name. For each
// (harness, session) scope, any usage field that channel reports is zeroed on
// the scope's metric-channel events. Fields the log channel never reports
// (Claude Code reports cost only on claude_code.cost.usage) survive on the
// metric channel, so cost still lands exactly once. Runtimes that emit only
// metrics have no log/span channel in scope and are left untouched.
//
// Poll events are not the log channel. A poll backfill has no metric_name either, but it is a
// copy of the session's history, not a third OTel channel, and preferLiveOverPoll has already
// removed the part of it a live channel covers. What survives is from before live capture began,
// so letting it claim fields here would zero the metric's tokens for the rest of the session.
func dedupeOverlappingChannels(events []*usageEvent) []*usageEvent {
	scopeKey := func(ue *usageEvent) string { return ue.harness + "\x00" + ue.session }
	logFields := map[string]*usageFieldSet{}
	for _, ue := range events {
		if ue.metricName != "" || ue.collectionMethod == schema.CollectionMethodPoll {
			continue
		}
		fs := logFields[scopeKey(ue)]
		if fs == nil {
			fs = &usageFieldSet{}
			logFields[scopeKey(ue)] = fs
		}
		fs.mark(ue.usage)
	}
	if len(logFields) == 0 {
		return events
	}
	out := events[:0]
	for _, ue := range events {
		if ue.metricName != "" {
			if fs := logFields[scopeKey(ue)]; fs != nil {
				// Drop a metric event left with no usage so it neither inflates
				// event counts nor seeds an empty cumulative series.
				if !fs.clear(&ue.usage) {
					continue
				}
			}
		}
		out = append(out, ue)
	}
	return out
}

// usageFieldSet records which usage fields a channel reported anywhere in a scope, so a second
// channel can give up exactly those fields and keep the ones only it reports.
type usageFieldSet struct {
	input, output, cacheRead, cacheCreation, reasoning, cost bool
}

func (fs *usageFieldSet) mark(u Usage) {
	fs.input = fs.input || u.InputTokens != 0
	fs.output = fs.output || u.OutputTokens != 0
	fs.cacheRead = fs.cacheRead || u.CacheReadInputTokens != 0
	fs.cacheCreation = fs.cacheCreation || u.CacheCreationInputTokens != 0
	fs.reasoning = fs.reasoning || u.ReasoningOutputTokens != 0
	fs.cost = fs.cost || u.CostUSD != 0
}

func (fs *usageFieldSet) any() bool {
	return fs.input || fs.output || fs.cacheRead || fs.cacheCreation || fs.reasoning || fs.cost
}

// clear zeroes the fields in the set on u and reports whether u still carries any usage.
func (fs *usageFieldSet) clear(u *Usage) bool {
	if fs.input {
		u.InputTokens = 0
	}
	if fs.output {
		u.OutputTokens = 0
	}
	if fs.cacheRead {
		u.CacheReadInputTokens = 0
	}
	if fs.cacheCreation {
		u.CacheCreationInputTokens = 0
	}
	if fs.reasoning {
		u.ReasoningOutputTokens = 0
	}
	if fs.cost {
		u.CostUSD = 0
	}
	return u.TotalTokens() != 0 || u.ReasoningOutputTokens != 0 || u.CostUSD != 0
}

// resolveCumulativeSeries rewrites cumulative metric contributions into
// per-interval deltas. Each cumulative series is identified by harness,
// session, model, metric name, and the single usage field the datapoint set.
func resolveCumulativeSeries(events []*usageEvent) {
	series := map[string][]*usageEvent{}
	for _, ue := range events {
		if !ue.cumulative {
			continue
		}
		field, value := dominantUsageField(ue.usage)
		if field == "" {
			continue
		}
		ue.seriesField = field
		ue.seriesValue = value
		key := strings.Join([]string{ue.harness, ue.session, ue.model, ue.metricName, field}, "\x00")
		series[key] = append(series[key], ue)
	}
	for _, points := range series {
		sort.SliceStable(points, func(i, j int) bool {
			if points[i].ts.Equal(points[j].ts) {
				return points[i].order < points[j].order
			}
			return points[i].ts.Before(points[j].ts)
		})
		previous := 0.0
		for _, point := range points {
			delta := point.seriesValue - previous
			if delta < 0 {
				// Counter reset: the raw value is the new interval's total.
				delta = point.seriesValue
			}
			previous = point.seriesValue
			setUsageField(&point.usage, point.seriesField, delta)
		}
	}
}

func dominantUsageField(u Usage) (string, float64) {
	switch {
	case u.CostUSD != 0:
		return "cost_usd", u.CostUSD
	case u.InputTokens != 0:
		return "input_tokens", float64(u.InputTokens)
	case u.OutputTokens != 0:
		return "output_tokens", float64(u.OutputTokens)
	case u.CacheReadInputTokens != 0:
		return "cache_read_input_tokens", float64(u.CacheReadInputTokens)
	case u.CacheCreationInputTokens != 0:
		return "cache_creation_input_tokens", float64(u.CacheCreationInputTokens)
	case u.ReasoningOutputTokens != 0:
		return "reasoning_output_tokens", float64(u.ReasoningOutputTokens)
	default:
		return "", 0
	}
}

func setUsageField(u *Usage, field string, value float64) {
	switch field {
	case "cost_usd":
		u.CostUSD = value
	case "input_tokens":
		u.InputTokens = int64(value)
	case "output_tokens":
		u.OutputTokens = int64(value)
	case "cache_read_input_tokens":
		u.CacheReadInputTokens = int64(value)
	case "cache_creation_input_tokens":
		u.CacheCreationInputTokens = int64(value)
	case "reasoning_output_tokens":
		u.ReasoningOutputTokens = int64(value)
	}
}

// RunKey builds the CI run grouping key from a run's provider and id, joined as
// "provider/run_id". It falls back to whichever part is set so an empty
// provider never yields a leading slash. The same key labels the BY RUN rollup
// and is accepted by the --run-id filter.
func RunKey(provider, runID string) string {
	switch {
	case provider != "" && runID != "":
		return provider + "/" + runID
	case provider != "":
		return provider
	default:
		return runID
	}
}

func addGroup(groups map[string]*Usage, key string, delta Usage) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	if groups[key] == nil {
		groups[key] = &Usage{}
	}
	groups[key].add(delta)
}

func sortedGroups(groups map[string]*Usage, limit int) []Group {
	out := make([]Group, 0, len(groups))
	for key, usage := range groups {
		out = append(out, Group{Key: key, Usage: *usage})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].Usage.TotalTokens(), out[j].Usage.TotalTokens()
		if left == right {
			return out[i].Key < out[j].Key
		}
		return left > right
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sortedBuckets(buckets map[time.Time]*Usage) []TimeBucket {
	starts := make([]time.Time, 0, len(buckets))
	for start := range buckets {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	out := make([]TimeBucket, 0, len(starts))
	for _, start := range starts {
		out = append(out, TimeBucket{Start: start.UTC().Format(time.RFC3339), Usage: *buckets[start]})
	}
	return out
}

func buildUtilization(events []*usageEvent, nearLimitRatio float64) []ModelUtilization {
	type sample struct{ inputTotal int64 }
	samples := map[string]map[string]*sample{}
	reportedWindow := map[string]int64{}
	for _, ue := range events {
		if ue.model == "" {
			continue
		}
		// A reported context size is the measurement; summing the usage fields only approximates
		// it. Prefer the runtime's own number when it gave one, and take its window with it -- a
		// reported limit reflects the tier actually in force, which a static model table cannot.
		inputTotal := ue.contextUsed
		if inputTotal <= 0 {
			inputTotal = ue.usage.InputTokens + ue.usage.CacheReadInputTokens + ue.usage.CacheCreationInputTokens
		} else if ue.contextLimit > 0 && ue.contextLimit > reportedWindow[ue.model] {
			reportedWindow[ue.model] = ue.contextLimit
		}
		if inputTotal <= 0 {
			continue
		}
		callKey := strings.Join([]string{ue.harness, ue.session, ue.ts.UTC().Format(time.RFC3339)}, "\x00")
		if samples[ue.model] == nil {
			samples[ue.model] = map[string]*sample{}
		}
		if samples[ue.model][callKey] == nil {
			samples[ue.model][callKey] = &sample{}
		}
		samples[ue.model][callKey].inputTotal += inputTotal
	}
	out := make([]ModelUtilization, 0, len(samples))
	for model, calls := range samples {
		utilization := ModelUtilization{Model: model, Calls: len(calls)}
		window, known := ContextWindow(model)
		// A window the runtime reported beats the static table, which is a best-effort snapshot
		// and cannot know which tier a call actually ran under.
		if reported := reportedWindow[model]; reported > 0 {
			window, known = reported, true
		}
		if known {
			utilization.ContextWindow = window
		}
		ratios := make([]float64, 0, len(calls))
		for _, call := range calls {
			if call.inputTotal > utilization.MaxInputTokens {
				utilization.MaxInputTokens = call.inputTotal
			}
			if !known {
				continue
			}
			ratio := float64(call.inputTotal) / float64(window)
			ratios = append(ratios, ratio)
			if ratio >= nearLimitRatio {
				utilization.NearLimitCalls++
			}
		}
		if len(ratios) > 0 {
			sort.Float64s(ratios)
			utilization.MaxRatio = ratios[len(ratios)-1]
			utilization.P95Ratio = percentile(ratios, 0.95)
		}
		out = append(out, utilization)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MaxRatio == out[j].MaxRatio {
			return out[i].Model < out[j].Model
		}
		return out[i].MaxRatio > out[j].MaxRatio
	})
	return out
}

// percentile reads the nearest-rank percentile from an ascending-sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(float64(len(sorted))*p+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func buildSessionDetail(events []*usageEvent, sessionID string) *SessionDetail {
	detail := &SessionDetail{SessionID: sessionID}
	var ordered []*usageEvent
	for _, ue := range events {
		// Match case-insensitively to stay consistent with the case-insensitive
		// session query the token callers use to select events.
		if !strings.EqualFold(ue.session, sessionID) || ue.contextOnly {
			continue
		}
		detail.Usage.add(ue.usage)
		ordered = append(ordered, ue)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ts.Equal(ordered[j].ts) {
			return ordered[i].order < ordered[j].order
		}
		return ordered[i].ts.Before(ordered[j].ts)
	})
	steps := make([]*Step, 0, len(ordered))
	bySpan := map[string]*Step{}
	for _, ue := range ordered {
		step := &Step{
			Action:       ue.action,
			Name:         ue.name,
			Model:        ue.model,
			TraceID:      ue.traceID,
			SpanID:       ue.spanID,
			ParentSpanID: ue.parentSpanID,
			Usage:        ue.usage,
		}
		if !ue.ts.IsZero() {
			step.Timestamp = schema.FormatTimestamp(ue.ts)
		}
		steps = append(steps, step)
		if step.SpanID != "" {
			bySpan[step.SpanID] = step
		}
	}
	for _, step := range steps {
		if step.ParentSpanID == "" {
			detail.Steps = append(detail.Steps, step)
			continue
		}
		parent, ok := bySpan[step.ParentSpanID]
		if !ok || parent == step {
			detail.Steps = append(detail.Steps, step)
			continue
		}
		parent.Children = append(parent.Children, step)
	}
	return detail
}
