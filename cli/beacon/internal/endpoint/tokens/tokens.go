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
	ByHarness       []Group            `json:"by_harness,omitempty"`
	ByRepository    []Group            `json:"by_repository,omitempty"`
	ByRun           []Group            `json:"by_run,omitempty"`
	Utilization     []ModelUtilization `json:"utilization,omitempty"`
	Series          []TimeBucket       `json:"series,omitempty"`
	SessionDetail   *SessionDetail     `json:"session_detail,omitempty"`
	EventsWithUsage int                `json:"events_with_usage"`
	TotalEvents     int                `json:"total_events"`
}

// usageEvent is one usage-bearing event with its usage normalized to a delta
// contribution and its attribution keys extracted.
type usageEvent struct {
	ts           time.Time
	order        int
	action       string
	name         string
	harness      string
	session      string
	model        string
	repository   string
	run          string
	traceID      string
	spanID       string
	parentSpanID string
	usage        Usage
	cumulative   bool
	metricName   string
	seriesField  string
	seriesValue  float64
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
	if opts.NearLimitRatio <= 0 {
		opts.NearLimitRatio = defaultNearLimitRatio
	}
	report := Report{TotalEvents: len(events)}
	usageEvents := collectUsageEvents(events)
	resolveCumulativeSeries(usageEvents)

	byModel := map[string]*Usage{}
	bySession := map[string]*Usage{}
	byHarness := map[string]*Usage{}
	byRepository := map[string]*Usage{}
	byRun := map[string]*Usage{}
	buckets := map[time.Time]*Usage{}
	for _, ue := range usageEvents {
		report.Totals.add(ue.usage)
		addGroup(byModel, ue.model, ue.usage)
		addGroup(bySession, ue.session, ue.usage)
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
	}
	report.EventsWithUsage = len(usageEvents)
	report.ByModel = sortedGroups(byModel, opts.TopLimit)
	report.BySession = sortedGroups(bySession, opts.TopLimit)
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

func collectUsageEvents(events []schema.Event) []*usageEvent {
	var out []*usageEvent
	for i, event := range events {
		if event.GenAI == nil || event.GenAI.Usage == nil {
			continue
		}
		usage := event.GenAI.Usage
		ue := &usageEvent{
			order:      i,
			action:     event.Event.Action,
			name:       event.Message,
			harness:    event.Harness.Name,
			model:      event.Model,
			repository: event.Repository,
			usage:      Usage{Events: 1},
		}
		if ts, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
			ue.ts = ts
		}
		if event.Session != nil {
			ue.session = event.Session.ID
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
		if ue.usage.TotalTokens() == 0 && ue.usage.ReasoningOutputTokens == 0 && ue.usage.CostUSD == 0 {
			continue
		}
		if event.Raw != nil {
			if temporality, _ := event.Raw["metric_temporality"].(string); strings.EqualFold(temporality, "cumulative") {
				ue.cumulative = true
			}
			ue.metricName, _ = event.Raw["metric_name"].(string)
		}
		out = append(out, ue)
	}
	return out
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
	for _, ue := range events {
		if ue.model == "" {
			continue
		}
		inputTotal := ue.usage.InputTokens + ue.usage.CacheReadInputTokens + ue.usage.CacheCreationInputTokens
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
		if ue.session != sessionID {
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
			step.Timestamp = ue.ts.UTC().Format(time.RFC3339)
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
