package dashboard

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

type LensDataV1 = asymptoteobserve.LensDataV1

// lensDataEventBudget bounds the encoded size of the events a lens receives. The data crosses into
// the frame in one structured-clone message and a lens builds its view from all of it at once, so
// a trace past this size is cut to a prefix and marked truncated rather than handed over whole.
const lensDataEventBudget = 12 << 20

// LensDataOptions selects the rules the findings are computed with, the same way `beacon scan`
// does. The zero value scans with the user's active rules.
type LensDataOptions struct {
	UserMode bool
	RulesDir string
}

// BuildLensData assembles what window.beacon.getTrace() resolves with for one trace: the trace
// bundle the trace view reads, the threat-rule findings over the trace's own events, and whether
// its runtime reports token usage at all. ok is false when no trace has that ID.
//
// The bundle comes from ShowTrace, so it answers from the opted-in history exactly when the trace
// view does. Findings and token coverage are computed from the trace's events in the live JSONL,
// because the rules engine evaluates whole endpoint events and the history keeps only projected
// ones; a trace whose lines were rotated away keeps its bundle and loses only those two.
func BuildLensData(logPath, traceID string, opts LensDataOptions) (LensDataV1, bool, error) {
	show, ok, err := ShowTrace(logPath, traceID, TraceQuery{EventQuery: EventQuery{NoLimit: true}})
	if err != nil || !ok {
		return LensDataV1{}, ok, err
	}
	var records []EventRecord
	err = streamEventRecords(logPath, func(record EventRecord) error {
		if traceProjectionID(record.Event) == traceID {
			records = append(records, record)
		}
		return nil
	})
	if err != nil {
		return LensDataV1{}, false, err
	}
	return lensDataFromShow(show, records, opts), true, nil
}

// BuildSessionLensData is BuildLensData for the dashboard's session page, which shows every event
// whose session ID is exactly session. A session is not always one trace: a runtime's hook
// events project to its session while its OTLP spans project to their own trace IDs. So this
// projects the same events the session page lists, with the trace projection code, into one bundle
// identified like a session trace. It reads the live JSONL, as the session page does.
func BuildSessionLensData(logPath, session string, opts LensDataOptions) (LensDataV1, bool, error) {
	result, err := ReadEvents(logPath, EventQuery{Session: session, NoLimit: true})
	if err != nil {
		return LensDataV1{}, false, err
	}
	records := make([]EventRecord, 0, len(result.Events))
	for _, record := range result.Events {
		if sessionID(record.Event) == session {
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		return LensDataV1{}, false, nil
	}
	SortRecordsAppendOrder(records)

	first := records[0].Event
	harness := asymptoteobserve.NormalizeHarnessName(first.Harness.Name)
	if harness == "" {
		harness = strings.TrimSpace(first.Harness.Name)
	}
	fold := newTraceFold("session:"+harness+":"+session, first)
	for _, record := range records {
		fold.add(record.Event, traceEventFromRecord(record, 0))
	}
	events := fold.builder.Events()
	total := len(events)
	show := TraceShowResultV1{
		Trace:  fold.summary(),
		Events: events,
		Spans:  asymptoteobserve.TraceSpans(events),
		Range:  TraceRangeV1{TotalEvents: total, ReturnedEvents: total, Offset: 1, Limit: total},
	}
	return lensDataFromShow(show, records, opts), true, nil
}

// lensDataFromShow finishes lens data from a projected trace and the endpoint events behind it.
func lensDataFromShow(show TraceShowResultV1, records []EventRecord, opts LensDataOptions) LensDataV1 {
	data := LensDataV1{
		APIVersion: asymptoteobserve.LensAPIVersion,
		Trace:      TraceBundleFromShow(show, version.Version),
	}
	data.Truncated = fitLensEvents(&data.Trace, lensDataEventBudget)
	// Findings and coverage are each optional in the contract. A rule pack that fails to load
	// leaves Findings nil -- "not scanned" -- instead of failing the whole lens, since every other
	// part of the data is still right.
	if findings, err := lensFindings(records, opts); err == nil {
		data.Findings = findings
	}
	raw := make([]schema.Event, 0, len(records))
	for _, record := range records {
		raw = append(raw, record.Event)
	}
	data.TokenCoverage = lensTokenCoverage(raw, show.Trace.Harness.Name)
	data.TokenUsage = lensTokenUsage(raw)
	return data
}

// lensTokenUsage counts the trace's usage with the same aggregation `beacon token-usage` and the
// dashboard's token view use, so a lens's total matches theirs. Summing each event's own usage
// would not: a runtime that reports one turn on two channels, or as a cumulative counter, would
// be counted twice. raw is in log append order, which the cumulative handling relies on.
func lensTokenUsage(raw []schema.Event) *asymptoteobserve.LensTokenUsageV1 {
	if len(raw) == 0 {
		return nil
	}
	report := tokens.Aggregate(raw, tokens.Options{})
	out := &asymptoteobserve.LensTokenUsageV1{
		Totals:          lensUsage(report.Totals),
		EventsWithUsage: report.EventsWithUsage,
	}
	for _, group := range report.ByModel {
		out.ByModel = append(out.ByModel, asymptoteobserve.LensTokenModelV1{
			Model:  group.Key,
			Usage:  lensUsage(group.Usage),
			Events: group.Usage.Events,
		})
	}
	return out
}

func lensUsage(u tokens.Usage) TraceUsageV1 {
	return TraceUsageV1{
		InputTokens:                u.InputTokens,
		OutputTokens:               u.OutputTokens,
		CacheReadInputTokens:       u.CacheReadInputTokens,
		CacheCreationInputTokens:   u.CacheCreationInputTokens,
		CacheCreation1hInputTokens: u.CacheCreation1hInputTokens,
		ReasoningOutputTokens:      u.ReasoningOutputTokens,
		CostUSD:                    u.CostUSD,
	}
}

// fitLensEvents keeps the longest prefix of the bundle's events that encodes within budget, and
// reports whether it dropped any. Spans that only cover dropped events are kept: they are small,
// and a lens resolves span event IDs leniently because truncation already allows a miss.
func fitLensEvents(bundle *asymptoteobserve.TraceBundleV1, budget int) bool {
	used := 0
	for i, event := range bundle.Events {
		encoded, err := json.Marshal(event)
		if err != nil {
			continue
		}
		used += len(encoded) + 1
		if used > budget {
			bundle.Events = bundle.Events[:i]
			if bundle.Range != nil {
				bundle.Range.ReturnedEvents = i
				bundle.Range.Limit = i
			}
			return true
		}
	}
	return false
}

// lensFindings runs the active rules over the trace's events. Any error, including no active rules
// (*detect.NoRulesError), leaves the caller's Findings nil: "not scanned".
func lensFindings(records []EventRecord, opts LensDataOptions) (*asymptoteobserve.LensFindingsV1, error) {
	if len(records) == 0 {
		// The trace is known only to the history: its lines have rotated out of the live log.
		// Scanning nothing would report a clean scan of a trace no rule ever saw.
		return nil, errors.New("no live events to scan")
	}
	compiled, skipped, err := compileActiveRules(opts.UserMode, opts.RulesDir)
	if err != nil {
		return nil, err
	}
	events := make([]asymptoteobserve.Event, 0, len(records))
	for _, record := range records {
		event := record.Event
		// Only scan copies receive fallback IDs; token aggregation and source logs keep raw IDs.
		if event.Event.ID == "" {
			event.Event.ID = record.ID
		}
		events = append(events, event)
	}
	// Same reason as RunScan: the log is in append order, not the order things happened, and
	// ordered correlation rules miss without this.
	threatrules.SortEvents(events)
	found, err := threatrules.ScanEvents(compiled, events)
	if err != nil {
		return nil, err
	}
	threatrules.SortFindings(found)

	taxonomy := make(map[string]map[string]string, len(compiled))
	for _, rule := range compiled {
		taxonomy[rule.Rule().ID] = rule.Rule().Taxonomy
	}
	items := make([]asymptoteobserve.LensFindingV1, 0, len(found))
	for _, f := range found {
		ids := make([]string, 0, len(f.Events))
		for _, event := range f.Events {
			// The scan copy uses the same explicit ID or record fallback as trace.events[].id.
			if id := event.Event.ID; id != "" {
				ids = append(ids, id)
			}
		}
		items = append(items, asymptoteobserve.LensFindingV1{
			RuleID:   f.RuleID,
			Title:    f.Title,
			Severity: f.Severity,
			Posture:  string(f.Posture),
			Reason:   f.Reason,
			Taxonomy: taxonomy[f.RuleID],
			EventIDs: ids,
		})
	}
	return &asymptoteobserve.LensFindingsV1{RulesEvaluated: len(compiled), Items: items, Skipped: skipped}, nil
}

// lensTokenCoverage is the trace's own line from the token coverage join, computed over just its
// events. The question a lens asks is about this run -- did it report usage, and should it have --
// so the installed-runtime half of the join, which answers "was the runtime used", is left out.
func lensTokenCoverage(raw []schema.Event, harness string) *asymptoteobserve.LensTokenCoverageV1 {
	want := asymptoteobserve.NormalizeHarnessName(strings.TrimSpace(harness))
	if want == "" || len(raw) == 0 {
		return nil
	}
	for _, line := range tokens.Coverage(raw, nil).Runtimes {
		if line.Harness == want {
			return &asymptoteobserve.LensTokenCoverageV1{
				Harness:     line.Harness,
				Status:      line.Status,
				Expectation: line.Expectation,
				Reason:      line.Reason,
			}
		}
	}
	return nil
}
