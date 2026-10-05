package tokens

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
)

// The expected figures below are worked by hand from the embedded catalog's rates, which are
// quoted in each case (USD per million tokens). A catalog regeneration that moves one of these
// rates is a reviewable change to this file.

func withUsage(input, output, cacheRead, cacheCreation, reasoning int64, cost float64) func(*schema.Event) {
	return func(e *schema.Event) {
		u := e.GenAI.Usage
		if input != 0 {
			u.InputTokens = int64Ptr(input)
		}
		if output != 0 {
			u.OutputTokens = int64Ptr(output)
		}
		if cacheRead != 0 {
			u.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(cacheRead)}
		}
		if cacheCreation != 0 {
			u.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: int64Ptr(cacheCreation)}
		}
		if reasoning != 0 {
			u.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: int64Ptr(reasoning)}
		}
		if cost != 0 {
			u.CostUSD = float64Ptr(cost)
		}
	}
}

func chain(mutators ...func(*schema.Event)) func(*schema.Event) {
	return func(e *schema.Event) {
		for _, m := range mutators {
			m(e)
		}
	}
}

func metricRaw(name, temporality string) func(*schema.Event) {
	return func(e *schema.Event) {
		e.Raw = map[string]interface{}{"metric_name": name, "metric_temporality": temporality}
	}
}

func collected(method string) func(*schema.Event) {
	return func(e *schema.Event) { e.Harness.CollectionMethod = method }
}

func usd(micro int64) float64 { return pricing.Microdollars(micro).USD() }

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAggregateCostEstimatesPerRuntimeShape(t *testing.T) {
	type want struct {
		estimated     int64 // microdollars
		effective     float64
		reported      float64
		source        string
		unpricedEvent int
		unpricedTok   int64
		// model -> requests priced in a band
		bands map[string][]BandUse
		// model -> request-scoped event count
		scoped map[string]int
	}
	cases := []struct {
		name   string
		events []schema.Event
		want   want
	}{
		{
			// claude_code.api_request carries the tokens (one request), the token metric repeats
			// them (deduplicated away), and claude_code.cost.usage carries the cost. claude-opus-4-6:
			// $5 in, $25 out, $0.50 cache read, $6.25 cache write, no band.
			// 11x5 + 826x25 + 119393x0.5 + 15751x6.25 = 178845.25 -> 178845 microdollars.
			name: "claude code api_request log plus cost metric",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-4-6", chain(
					withUsage(11, 826, 119393, 15751, 0, 0), collected("otlp"),
					func(e *schema.Event) { e.Event.Action = "tool.invoked"; e.Message = "claude_code.api_request" })),
				usageEventFixture("2026-06-11T10:00:05Z", "claude_code", "s1", "claude-opus-4-6[1m]", chain(
					withUsage(11, 0, 0, 0, 0, 0), collected("otlp"), metricRaw("claude_code.token.usage", "Delta"))),
				usageEventFixture("2026-06-11T10:00:05Z", "claude_code", "s1", "claude-opus-4-6[1m]", chain(
					withUsage(0, 0, 0, 0, 0, 0.1788), collected("otlp"), metricRaw("claude_code.cost.usage", "Delta"))),
			},
			want: want{estimated: 178845, effective: 0.1788, reported: 0.1788, source: CostSourceReported,
				scoped: map[string]int{"claude-opus-4-6": 1}},
		},
		{
			// Metrics only: the tokens arrive as per-type datapoints, which are interval sums and
			// priced at base rates. claude-sonnet-4-5: $3 in, $15 out.
			// 250000x3 + 2000x15 = 780000 microdollars -- no band despite 250k, as the metric is not
			// one request. The reported cost still wins the effective figure.
			name: "claude code metric-only path",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
					withUsage(250000, 0, 0, 0, 0, 0), metricRaw("claude_code.token.usage", "Delta"))),
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
					withUsage(0, 2000, 0, 0, 0, 0), metricRaw("claude_code.token.usage", "Delta"))),
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
					withUsage(0, 0, 0, 0, 0, 0.9), metricRaw("claude_code.cost.usage", "Delta"))),
			},
			want: want{estimated: 780000, effective: 0.9, reported: 0.9, source: CostSourceReported,
				scoped: map[string]int{"claude-sonnet-4-5": 0}},
		},
		{
			// A Codex turn span sums the turn's requests, so even a 300k-token turn is priced at
			// base rates. gpt-5.5: $5 in, $30 out, $0.50 cache read (band above 272k: $10/$45/$1).
			// 100000x5 + 200000x0.5 + 1000x30 = 630000 microdollars.
			name: "codex turn span is aggregate",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "c1", "gpt-5.5", chain(
					withUsage(100000, 1000, 200000, 0, 400, 0), collected("otlp"),
					func(e *schema.Event) {
						e.Message = "codex.turn.token_usage"
						e.Raw = map[string]interface{}{"source": "codex_turn_span", "turn_id": "t1"}
					})),
			},
			want: want{estimated: 630000, effective: 0.63, source: CostSourceEstimated,
				scoped: map[string]int{"gpt-5.5": 0}},
		},
		{
			// The same tokens from a Codex session-file token_count are one completion, so the
			// 300k prompt is billed in the long-context band for every token:
			// 100000x10 + 200000x1 + 1000x45 = 1245000 microdollars. Reasoning (400) is inside
			// output and not billed again.
			name: "codex session token_count is one request and banded",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "c1", "gpt-5.5", chain(
					withUsage(100000, 1000, 200000, 0, 400, 0), collected("poll"),
					func(e *schema.Event) {
						e.Raw = map[string]interface{}{"codex_session": map[string]interface{}{"source": "codex_session_token_count"}}
					})),
			},
			want: want{estimated: 1245000, effective: 1.245, source: CostSourceEstimated,
				bands:  map[string][]BandUse{"gpt-5.5": {{AboveTokens: 272000, Requests: 1}}},
				scoped: map[string]int{"gpt-5.5": 1}},
		},
		{
			// Copilot's shutdown record is a session-total delta: base rates even above 272k.
			// gpt-5.4: $2.50 in, $15 out, $0.25 cache read. 280000x2.5 + 5000x15 = 775000.
			name: "copilot shutdown aggregate stays at base rates",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "copilot_cli", "p1", "gpt-5.4", chain(
					withUsage(280000, 5000, 0, 0, 0, 0), collected("poll"),
					func(e *schema.Event) {
						e.Raw = map[string]interface{}{"token_source": "session_shutdown_model_metrics_delta"}
					})),
			},
			want: want{estimated: 775000, effective: 0.775, source: CostSourceEstimated,
				scoped: map[string]int{"gpt-5.4": 0}},
		},
		{
			// A Pi message_end is one request and reports its own cost. The estimate is still
			// computed (in the 200k band for claude-sonnet-4-5: $6 in, $22.50 out):
			// 250000x6 + 1000x22.5 = 1522500; the effective figure is the reported $1.20.
			name: "pi request with reported cost",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "pi_cli", "pi-1", "claude-sonnet-4-5", chain(
					withUsage(250000, 1000, 0, 0, 0, 1.2), collected("plugin"))),
			},
			want: want{estimated: 1522500, effective: 1.2, reported: 1.2, source: CostSourceReported,
				bands:  map[string][]BandUse{"claude-sonnet-4-5": {{AboveTokens: 200000, Requests: 1}}},
				scoped: map[string]int{"claude-sonnet-4-5": 1}},
		},
		{
			// A model the catalog does not price is counted as missing, never as free.
			name: "unpriced model",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "gemini_cli", "g1", "acme-internal-7b", withUsage(400, 100, 0, 0, 0, 0)),
			},
			want: want{estimated: 0, effective: 0, source: "", unpricedEvent: 1, unpricedTok: 500},
		},
		{
			// Session-less events are judged one at a time: the first reported a cost and
			// contributes it; the second did not and contributes its estimate (gpt-5: $1.25 in,
			// $10 out: 1000x1.25 + 100x10 = 2250). Its own estimate counts in estimated too
			// (claude-sonnet-4-5: 1000x3 + 100x15 = 4500).
			name: "session-less events mix reported and estimated",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "", "claude-sonnet-4-5", withUsage(1000, 100, 0, 0, 0, 0.2)),
				usageEventFixture("2026-06-11T10:00:01Z", "codex_cli", "", "gpt-5", withUsage(1000, 100, 0, 0, 0, 0)),
			},
			want: want{estimated: 6750, effective: 0.2 + 0.00225, reported: 0.2, source: CostSourceMixed},
		},
		{
			// Cumulative datapoints are differenced before pricing: 1000 then 3000 is 3000
			// tokens, not 4000. claude-sonnet-4-5 $3 in: 3000x3 = 9000.
			name: "cumulative metric deltas are priced after resolution",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
					withUsage(1000, 0, 0, 0, 0, 0), metricRaw("claude_code.token.usage", "Cumulative"))),
				usageEventFixture("2026-06-11T10:01:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
					withUsage(3000, 0, 0, 0, 0, 0), metricRaw("claude_code.token.usage", "Cumulative"))),
			},
			want: want{estimated: 9000, effective: 0.009, source: CostSourceEstimated,
				scoped: map[string]int{"claude-sonnet-4-5": 0}},
		},
		{
			// A request below the threshold is not banded even when request-scoped.
			// claude-sonnet-4-5 via the Claude Code session file: 150000x3 + 50000x0.3 +
			// 1000x15 = 480000.
			name: "band needs the threshold",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
					withUsage(150000, 1000, 50000, 0, 0, 0), collected("poll"))),
			},
			want: want{estimated: 480000, effective: 0.48, source: CostSourceEstimated,
				scoped: map[string]int{"claude-sonnet-4-5": 1}},
		},
		{
			// DeepSeek publishes no cache-write price, so writes are priced as input.
			// deepseek-chat: $0.28 in, $0.42 out, $0.028 cache read.
			// 1000x0.28 + 2000x0.28 (write) + 10000x0.028 + 500x0.42 = 280+560+280+210 = 1330.
			name: "deepseek per response with cache write fallback",
			events: []schema.Event{
				usageEventFixture("2026-06-11T10:00:00Z", "deepseek_harness", "d1", "deepseek-chat", chain(
					withUsage(1000, 500, 10000, 2000, 0, 0), collected("poll"),
					func(e *schema.Event) { e.Raw = map[string]interface{}{"token_source": "assistant_message"} })),
			},
			want: want{estimated: 1330, effective: 0.00133, source: CostSourceEstimated,
				scoped: map[string]int{"deepseek-chat": 1}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Aggregate(tc.events, Options{BucketSize: 3600e9})
			got := report.Totals
			if got.estimated != pricing.Microdollars(tc.want.estimated) || got.EstimatedCostUSD != usd(tc.want.estimated) {
				t.Fatalf("estimated = %d (%v), want %d", got.estimated, got.EstimatedCostUSD, tc.want.estimated)
			}
			if !approxEqual(got.EffectiveCostUSD, tc.want.effective) {
				t.Fatalf("effective = %v, want %v", got.EffectiveCostUSD, tc.want.effective)
			}
			if !approxEqual(got.CostUSD, tc.want.reported) {
				t.Fatalf("cost_usd = %v, want the reported %v only", got.CostUSD, tc.want.reported)
			}
			if got.CostSource != tc.want.source {
				t.Fatalf("cost_source = %q, want %q", got.CostSource, tc.want.source)
			}
			if got.UnpricedEvents != tc.want.unpricedEvent || got.UnpricedTokens != tc.want.unpricedTok {
				t.Fatalf("unpriced = %d events / %d tokens, want %d / %d", got.UnpricedEvents, got.UnpricedTokens, tc.want.unpricedEvent, tc.want.unpricedTok)
			}
			models := map[string]ModelPricing{}
			for _, m := range report.Pricing.Models {
				models[m.Model] = m
			}
			for model, scoped := range tc.want.scoped {
				if models[model].RequestScopedEvents != scoped {
					t.Fatalf("%s request_scoped_events = %d, want %d (%+v)", model, models[model].RequestScopedEvents, scoped, models[model])
				}
			}
			for model := range models {
				if !reflect.DeepEqual(models[model].BandsApplied, tc.want.bands[model]) {
					t.Fatalf("%s bands_applied = %+v, want %+v", model, models[model].BandsApplied, tc.want.bands[model])
				}
			}
			assertGroupsSumToTotals(t, report)
		})
	}
}

// Every rollup partitions the same events, so its estimates must add up to the total exactly.
func assertGroupsSumToTotals(t *testing.T, report Report) {
	t.Helper()
	check := func(name string, usages []Usage) {
		var est, effEst pricing.Microdollars
		var effRep float64
		unpriced := 0
		for _, u := range usages {
			est += u.estimated
			effEst += u.effectiveEstimated
			effRep += u.effectiveReported
			unpriced += u.UnpricedEvents
		}
		if len(usages) == 0 {
			return
		}
		if est != report.Totals.estimated || effEst != report.Totals.effectiveEstimated ||
			!approxEqual(effRep, report.Totals.effectiveReported) || unpriced != report.Totals.UnpricedEvents {
			t.Fatalf("%s sums (est %d, eff %d+%v, unpriced %d) differ from totals %+v", name, est, effEst, effRep, unpriced, report.Totals)
		}
	}
	groupUsages := func(groups []Group) []Usage {
		out := make([]Usage, 0, len(groups))
		for _, g := range groups {
			out = append(out, g.Usage)
		}
		return out
	}
	check("by_model", groupUsages(report.ByModel))
	check("by_harness", groupUsages(report.ByHarness))
	series := make([]Usage, 0, len(report.Series))
	for _, b := range report.Series {
		series = append(series, b.Usage)
	}
	check("series", series)
	var modelEst pricing.Microdollars
	for _, m := range report.Pricing.Models {
		modelEst += m.estimated
	}
	if modelEst != report.Totals.estimated {
		t.Fatalf("pricing.models estimates %d differ from totals %d", modelEst, report.Totals.estimated)
	}
}

func TestRequestScopedClassifier(t *testing.T) {
	cases := []struct {
		name string
		ue   usageEvent
		want bool
	}{
		{"claude api_request log", usageEvent{harness: "claude_code", name: "claude_code.api_request", collectionMethod: "otlp"}, true},
		{"claude api_request by event.name", usageEvent{harness: "claude_code", name: "api_request", collectionMethod: "otlp"}, true},
		{"cowork api_request log", usageEvent{harness: "claude_cowork", name: "claude_code.api_request"}, true},
		{"claude token metric", usageEvent{harness: "claude_code", name: "claude_code.token.usage", metricName: "claude_code.token.usage"}, false},
		{"claude session file", usageEvent{harness: "claude_code", action: "token.usage", collectionMethod: "poll"}, true},
		{"claude other log", usageEvent{harness: "claude_code", name: "claude_code.tool_result", collectionMethod: "otlp"}, false},
		{"codex token_count", usageEvent{harness: "codex_cli", codexSessionSource: "codex_session_token_count"}, true},
		{"codex token_usage_record turn", usageEvent{harness: "codex_cli", codexSessionSource: "codex_session_token_usage_record"}, false},
		{"codex turn span", usageEvent{harness: "codex_cli", rawSource: "codex_turn_span"}, false},
		{"codex legacy metric", usageEvent{harness: "codex", metricName: "codex.turn.token_usage"}, false},
		{"pi message_end", usageEvent{harness: "pi", action: "token.usage", collectionMethod: "plugin"}, true},
		{"pi tool result usage", usageEvent{harness: "pi_cli", action: "tool.completed", collectionMethod: "plugin"}, false},
		{"pi session file", usageEvent{harness: "pi_cli", action: "agent.message", collectionMethod: "poll"}, true},
		{"omp message_end", usageEvent{harness: "omp", action: "token.usage", collectionMethod: "plugin"}, true},
		{"prime message_end", usageEvent{harness: "prime_agent", action: "token.usage"}, true},
		{"senpi message_end", usageEvent{harness: "omo_senpi", action: "token.usage"}, true},
		{"opencode plugin", usageEvent{harness: "opencode", action: "agent.message", collectionMethod: "plugin"}, true},
		{"opencode poll", usageEvent{harness: "opencode", action: "token.usage", collectionMethod: "poll"}, true},
		{"openclaw llm_output turn", usageEvent{harness: "openclaw_gateway", action: "token.usage", collectionMethod: "plugin"}, false},
		{"openclaw session file", usageEvent{harness: "openclaw_gateway", action: "token.usage", collectionMethod: "poll"}, true},
		{"deepseek assistant message", usageEvent{harness: "deepseek_harness", tokenSource: "assistant_message"}, true},
		{"cline task total", usageEvent{harness: "cline", action: "task.completed", collectionMethod: "plugin"}, false},
		{"copilot shutdown", usageEvent{harness: "copilot_cli", tokenSource: "session_shutdown_model_metrics_delta", collectionMethod: "poll"}, false},
		{"hermes cumulative delta", usageEvent{harness: "hermes", collectionMethod: "poll"}, false},
		{"factory settings", usageEvent{harness: "factory", rawSource: "factory_settings", collectionMethod: "poll"}, false},
		{"fx turn", usageEvent{harness: "vercel_fx", collectionMethod: "poll"}, false},
		{"cursor admin api", usageEvent{harness: "cursor", collectionMethod: "poll"}, false},
		{"gemini generic span", usageEvent{harness: "gemini_cli", collectionMethod: "otlp"}, false},
		{"opencode cumulative metric", usageEvent{harness: "opencode", cumulative: true, metricName: "x"}, false},
	}
	for _, tc := range cases {
		ue := tc.ue
		if got := requestScoped(&ue); got != tc.want {
			t.Errorf("%s: requestScoped = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A scope with a reported cost is reported through and through: its token events do not add
// their estimate to the effective figure, on any grouping.
func TestEffectiveCostUsesReportedScopeOnEveryGrouping(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", chain(
			withUsage(1000, 0, 0, 0, 0, 0), metricRaw("claude_code.token.usage", "Delta"))),
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5[1m]", chain(
			withUsage(0, 0, 0, 0, 0, 0.01), metricRaw("claude_code.cost.usage", "Delta"))),
		// Another session of the same runtime without any reported cost is estimated.
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s2", "claude-sonnet-4-5", chain(
			withUsage(1000, 0, 0, 0, 0, 0), metricRaw("claude_code.token.usage", "Delta"))),
	}
	report := Aggregate(events, Options{SessionID: "s1"})
	bySession := map[string]Usage{}
	for _, g := range report.BySession {
		bySession[g.Key] = g.Usage
	}
	if s1 := bySession["s1"]; !approxEqual(s1.EffectiveCostUSD, 0.01) || s1.CostSource != CostSourceReported || s1.EstimatedCostUSD != 0.003 {
		t.Fatalf("s1 = %+v, want reported 0.01 effective with a 0.003 estimate alongside", s1)
	}
	if s2 := bySession["s2"]; s2.EffectiveCostUSD != 0.003 || s2.CostSource != CostSourceEstimated {
		t.Fatalf("s2 = %+v, want its estimate as effective", s2)
	}
	if got := report.SessionDetail.Usage; !approxEqual(got.EffectiveCostUSD, 0.01) || got.CostSource != CostSourceReported {
		t.Fatalf("session detail = %+v", got)
	}
	for _, step := range report.SessionDetail.Steps {
		if step.Usage.CostSource != CostSourceReported {
			t.Fatalf("step %+v should be reported-sourced", step.Usage)
		}
	}
	if report.Totals.CostSource != CostSourceMixed {
		t.Fatalf("totals cost_source = %q, want mixed", report.Totals.CostSource)
	}
}

// Context-only events are not spend: no estimate, no unpriced count.
func TestContextOnlyEventsAreNotPriced(t *testing.T) {
	event := usageEventFixture("2026-06-11T10:00:00Z", "qwen_code", "q1", "acme-unknown", func(e *schema.Event) {
		e.GenAI.Usage = nil
		e.GenAI.Context = &schema.GenAIContextInfo{UsedTokens: int64Ptr(5000), LimitTokens: int64Ptr(100000)}
	})
	report := Aggregate([]schema.Event{event}, Options{})
	if report.Totals.UnpricedEvents != 0 || len(report.Pricing.Unpriced) != 0 || report.Totals.EstimatedCostUSD != 0 {
		t.Fatalf("context-only event was priced: %+v %+v", report.Totals, report.Pricing)
	}
}

func TestPricingSummaryIsDeterministicAndComplete(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "opencode", "o1", "gpt-5.5", withUsage(300000, 10, 0, 0, 0, 0)),
		usageEventFixture("2026-06-11T10:00:01Z", "deepseek_harness", "d1", "deepseek-chat", chain(withUsage(10, 10, 0, 100, 0, 0),
			func(e *schema.Event) { e.Raw = map[string]interface{}{"token_source": "assistant_message"} })),
		usageEventFixture("2026-06-11T10:00:02Z", "gemini_cli", "g1", "zeta-unpriced", withUsage(5, 5, 0, 0, 0, 0)),
		usageEventFixture("2026-06-11T10:00:03Z", "gemini_cli", "g1", "", withUsage(7, 0, 0, 0, 0, 0)),
		usageEventFixture("2026-06-11T10:00:04Z", "copilot_cli", "c1", "claude-sonnet-4.6", withUsage(10, 10, 0, 0, 0, 0)),
	}
	first, _ := json.Marshal(Aggregate(events, Options{}).Pricing)
	reversed := make([]schema.Event, len(events))
	for i := range events {
		reversed[len(events)-1-i] = events[i]
	}
	second, _ := json.Marshal(Aggregate(reversed, Options{}).Pricing)
	if string(first) != string(second) {
		t.Fatalf("pricing summary depends on event order:\n%s\n%s", first, second)
	}
	summary := Aggregate(events, Options{}).Pricing
	var names []string
	for _, m := range summary.Models {
		names = append(names, m.Model)
	}
	if !reflect.DeepEqual(names, []string{"claude-sonnet-4.6", "deepseek-chat", "gpt-5.5"}) {
		t.Fatalf("models = %v, want sorted", names)
	}
	for _, m := range summary.Models {
		switch m.Model {
		case "claude-sonnet-4.6":
			if m.Key != "claude-sonnet-4-6" || m.Match != string(pricing.MatchDottedVersion) || m.Rates.Input != 3 || m.Rates.Output != 15 {
				t.Fatalf("copilot model pricing = %+v", m)
			}
		case "gpt-5.5":
			if len(m.BandsApplied) != 1 || m.BandsApplied[0].AboveTokens != 272000 || m.RequestScopedEvents != 1 {
				t.Fatalf("opencode request above 272k should be banded: %+v", m)
			}
		case "deepseek-chat":
			if !reflect.DeepEqual(m.Fallbacks, []string{"cache_write_at_input_rate"}) {
				t.Fatalf("deepseek fallbacks = %v", m.Fallbacks)
			}
		}
	}
	if len(summary.Unpriced) != 2 || summary.Unpriced[0].Model != "" || summary.Unpriced[0].Tokens != 7 ||
		summary.Unpriced[1].Model != "zeta-unpriced" || summary.Unpriced[1].Tokens != 10 {
		t.Fatalf("unpriced = %+v", summary.Unpriced)
	}
	if summary.Catalog.Commit == "" || summary.Catalog.GeneratedAt == "" || summary.Tier != "standard" {
		t.Fatalf("catalog provenance = %+v", summary.Catalog)
	}
}

func TestUsageJSONCarriesCostFields(t *testing.T) {
	report := Aggregate([]schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "c1", "gpt-5", withUsage(1000, 100, 0, 0, 0, 0)),
	}, Options{SessionID: "c1"})
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Totals        map[string]interface{} `json:"totals"`
		SessionDetail struct {
			Steps []struct {
				Usage map[string]interface{} `json:"usage"`
			} `json:"steps"`
		} `json:"session_detail"`
		Pricing map[string]interface{} `json:"pricing"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cost_usd", "estimated_cost_usd", "effective_cost_usd", "cost_source"} {
		if _, ok := decoded.Totals[key]; !ok {
			t.Fatalf("totals missing %q: %s", key, encoded)
		}
		if _, ok := decoded.SessionDetail.Steps[0].Usage[key]; !ok {
			t.Fatalf("step usage missing %q: %s", key, encoded)
		}
	}
	for _, key := range []string{"catalog", "tier", "models"} {
		if _, ok := decoded.Pricing[key]; !ok {
			t.Fatalf("pricing missing %q: %s", key, encoded)
		}
	}
	// 1000 x $1.25 + 100 x $10 per MTok.
	if decoded.Totals["estimated_cost_usd"] != 0.00225 || decoded.Totals["cost_source"] != CostSourceEstimated {
		t.Fatalf("totals = %v", decoded.Totals)
	}
}
