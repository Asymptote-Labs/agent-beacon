package dashboard

import (
	"encoding/json"
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
	data := LensDataV1{
		APIVersion: asymptoteobserve.LensAPIVersion,
		Trace:      TraceBundleFromShow(show, version.Version),
	}
	data.Truncated = fitLensEvents(&data.Trace, lensDataEventBudget)

	var raw []schema.Event
	err = StreamEvents(logPath, func(event schema.Event) error {
		if traceProjectionID(event) == traceID {
			raw = append(raw, event)
		}
		return nil
	})
	if err != nil {
		return LensDataV1{}, false, err
	}
	// Findings and coverage are each optional in the contract. A rule pack that fails to load
	// leaves Findings nil -- "not scanned" -- instead of failing the whole lens, since every other
	// part of the data is still right.
	if findings, err := lensFindings(raw, opts); err == nil {
		data.Findings = findings
	}
	data.TokenCoverage = lensTokenCoverage(raw, show.Trace.Harness.Name)
	return data, true, nil
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
func lensFindings(raw []schema.Event, opts LensDataOptions) (*asymptoteobserve.LensFindingsV1, error) {
	compiled, skipped, err := compileActiveRules(opts.UserMode, opts.RulesDir)
	if err != nil {
		return nil, err
	}
	events := append([]asymptoteobserve.Event(nil), raw...)
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
			// Trace events take their ID from the endpoint event ID, so evidence resolves against
			// trace.events[].id without copying the event.
			if id := strings.TrimSpace(event.Event.ID); id != "" {
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
