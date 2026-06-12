package tokens

import (
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func usageEventFixture(ts, harness, session, model string, mutate func(*schema.Event)) schema.Event {
	event := schema.Event{
		Timestamp: ts,
		Event:     schema.EventInfo{Kind: "agent_runtime", Action: "token.usage", Category: "metric"},
		Harness:   schema.HarnessInfo{Name: harness},
		Model:     model,
		GenAI:     &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{}},
	}
	if session != "" {
		event.Session = &schema.SessionInfo{ID: session}
	}
	if mutate != nil {
		mutate(&event)
	}
	return event
}

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func TestAggregateSumsDeltaUsageAcrossGroups(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
			e.Repository = "github.com/acme/app"
		}),
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.OutputTokens = int64Ptr(40)
		}),
		usageEventFixture("2026-06-11T11:00:00Z", "asymptote_observe", "s2", "gpt-4o-mini", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(60)
			e.GenAI.Usage.OutputTokens = int64Ptr(20)
			e.GenAI.Usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: int64Ptr(5)}
		}),
		usageEventFixture("2026-06-11T11:30:00Z", "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.Event.Action = "cost.usage"
			e.GenAI.Usage.CostUSD = float64Ptr(0.25)
		}),
		// Event without usage is counted in TotalEvents only.
		{Timestamp: "2026-06-11T11:45:00Z", Event: schema.EventInfo{Action: "tool.invoked"}, Harness: schema.HarnessInfo{Name: "claude_code"}},
	}

	report := Aggregate(events, Options{BucketSize: time.Hour})
	if report.TotalEvents != 5 || report.EventsWithUsage != 4 {
		t.Fatalf("event counts = %d/%d, want 4/5", report.EventsWithUsage, report.TotalEvents)
	}
	if report.Totals.InputTokens != 160 || report.Totals.OutputTokens != 60 || report.Totals.ReasoningOutputTokens != 5 || report.Totals.CostUSD != 0.25 {
		t.Fatalf("totals = %#v", report.Totals)
	}
	if len(report.ByModel) != 2 || report.ByModel[0].Key != "claude-sonnet-4-5" || report.ByModel[0].Usage.InputTokens != 100 || report.ByModel[0].Usage.CostUSD != 0.25 {
		t.Fatalf("by_model = %#v", report.ByModel)
	}
	if len(report.BySession) != 2 || report.BySession[0].Key != "s1" {
		t.Fatalf("by_session = %#v", report.BySession)
	}
	if len(report.ByHarness) != 2 || report.ByHarness[0].Key != "claude_code" {
		t.Fatalf("by_harness = %#v", report.ByHarness)
	}
	if len(report.ByRepository) != 1 || report.ByRepository[0].Key != "github.com/acme/app" || report.ByRepository[0].Usage.InputTokens != 100 {
		t.Fatalf("by_repository = %#v", report.ByRepository)
	}
	if len(report.Series) != 2 || report.Series[0].Start != "2026-06-11T10:00:00Z" || report.Series[0].Usage.InputTokens != 100 || report.Series[1].Usage.CostUSD != 0.25 {
		t.Fatalf("series = %#v", report.Series)
	}
}

func TestAggregateDedupesCumulativeSeries(t *testing.T) {
	cumulative := func(ts string, value int64) schema.Event {
		return usageEventFixture(ts, "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(value)
			e.Raw = map[string]interface{}{
				"metric_name":        "claude_code.token.usage",
				"metric_temporality": "Cumulative",
			}
		})
	}
	events := []schema.Event{
		cumulative("2026-06-11T10:00:00Z", 100),
		cumulative("2026-06-11T10:01:00Z", 250),
		cumulative("2026-06-11T10:02:00Z", 400),
		// Counter reset: raw value becomes the interval contribution.
		cumulative("2026-06-11T10:03:00Z", 50),
	}

	report := Aggregate(events, Options{})
	if report.Totals.InputTokens != 450 {
		t.Fatalf("cumulative input total = %d, want 450 (100+150+150+50)", report.Totals.InputTokens)
	}
}

func TestAggregateAttributesCIRuns(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "ci-1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(10)
			e.Run = &schema.RunInfo{Provider: "github_actions", RunID: "12345"}
		}),
		usageEventFixture("2026-06-11T10:05:00Z", "claude_code", "ci-1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.OutputTokens = int64Ptr(4)
			e.Run = &schema.RunInfo{Provider: "github_actions", RunID: "12345"}
		}),
	}
	report := Aggregate(events, Options{})
	if len(report.ByRun) != 1 || report.ByRun[0].Key != "github_actions/12345" {
		t.Fatalf("by_run = %#v", report.ByRun)
	}
	if report.ByRun[0].Usage.InputTokens != 10 || report.ByRun[0].Usage.OutputTokens != 4 {
		t.Fatalf("by_run usage = %#v", report.ByRun[0].Usage)
	}
}

func cumulativeEvent(ts, session string, value int64, attrs map[string]interface{}) schema.Event {
	return schema.Event{
		Timestamp: ts,
		Event:     schema.EventInfo{Kind: "agent_runtime", Action: "token.usage", Category: "metric"},
		Harness:   schema.HarnessInfo{Name: "claude_code"},
		Model:     "claude-sonnet-4-5",
		Session:   &schema.SessionInfo{ID: session},
		GenAI:     &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(value)}},
		Raw: map[string]interface{}{
			"metric_name":        "claude_code.token.usage",
			"metric_temporality": "Cumulative",
			"attributes":         attrs,
		},
	}
}

func TestAggregateCumulativeSeriesSplitByAttributesWithoutSession(t *testing.T) {
	// Two cumulative input series with no session.id, distinguished only by a
	// datapoint attribute (query_source). They must not be chained into one
	// series. Interleaved order makes a merged diff visibly wrong.
	main := map[string]interface{}{"type": "input", "query_source": "main"}
	sub := map[string]interface{}{"type": "input", "query_source": "subagent"}
	events := []schema.Event{
		cumulativeEvent("2026-06-11T10:00:00Z", "", 100, main), // main delta 100
		cumulativeEvent("2026-06-11T10:00:01Z", "", 50, sub),   // sub delta 50
		cumulativeEvent("2026-06-11T10:00:02Z", "", 300, main), // main delta 200
		cumulativeEvent("2026-06-11T10:00:03Z", "", 120, sub),  // sub delta 70
	}
	report := Aggregate(events, Options{})
	if report.Totals.InputTokens != 420 {
		t.Fatalf("input tokens = %d, want 420 (merged series would over-count)", report.Totals.InputTokens)
	}
}

func TestAggregatePlateauCumulativeEventsDoNotInflateCounts(t *testing.T) {
	attrs := map[string]interface{}{"type": "input"}
	events := []schema.Event{
		cumulativeEvent("2026-06-11T10:00:00Z", "s1", 100, attrs), // delta 100
		cumulativeEvent("2026-06-11T10:00:01Z", "s1", 100, attrs), // delta 0 (plateau)
		cumulativeEvent("2026-06-11T10:00:02Z", "s1", 200, attrs), // delta 100
	}
	report := Aggregate(events, Options{})
	if report.Totals.InputTokens != 200 {
		t.Fatalf("input tokens = %d, want 200", report.Totals.InputTokens)
	}
	if report.EventsWithUsage != 2 {
		t.Fatalf("events_with_usage = %d, want 2 (plateau event must not count)", report.EventsWithUsage)
	}
	if report.Totals.Events != 2 {
		t.Fatalf("totals.events = %d, want 2", report.Totals.Events)
	}
}

func TestAggregateRunKeyWithoutProvider(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(7)
			e.Run = &schema.RunInfo{RunID: "run-abc"} // provider empty
		}),
	}
	report := Aggregate(events, Options{})
	if len(report.ByRun) != 1 || report.ByRun[0].Key != "run-abc" {
		t.Fatalf("by_run = %#v, want bare run id without leading slash", report.ByRun)
	}
}

func TestAggregateBuildsSessionStepTree(t *testing.T) {
	spanEvent := func(ts, span, parent string, input int64) schema.Event {
		return usageEventFixture(ts, "asymptote_observe", "s1", "gpt-4o-mini", func(e *schema.Event) {
			e.Event.Action = "tool.invoked"
			e.Message = "step-" + span
			e.GenAI.Usage.InputTokens = int64Ptr(input)
			e.Trace = &schema.TraceInfo{ID: "trace-1", SpanID: span, ParentSpanID: parent}
		})
	}
	events := []schema.Event{
		spanEvent("2026-06-11T10:00:00Z", "root", "", 100),
		spanEvent("2026-06-11T10:00:05Z", "child-a", "root", 30),
		spanEvent("2026-06-11T10:00:10Z", "child-b", "root", 20),
		spanEvent("2026-06-11T10:00:15Z", "grandchild", "child-a", 10),
		// Step without span identity stays flat at the top level.
		usageEventFixture("2026-06-11T10:00:20Z", "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.OutputTokens = int64Ptr(7)
		}),
	}

	report := Aggregate(events, Options{SessionID: "s1"})
	detail := report.SessionDetail
	if detail == nil || detail.SessionID != "s1" {
		t.Fatalf("session detail = %#v", detail)
	}
	if detail.Usage.InputTokens != 160 || detail.Usage.OutputTokens != 7 {
		t.Fatalf("session usage = %#v", detail.Usage)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("top-level steps = %d, want 2 (root + flat step): %#v", len(detail.Steps), detail.Steps)
	}
	root := detail.Steps[0]
	if root.SpanID != "root" || len(root.Children) != 2 {
		t.Fatalf("root step = %#v", root)
	}
	if root.Children[0].SpanID != "child-a" || len(root.Children[0].Children) != 1 || root.Children[0].Children[0].SpanID != "grandchild" {
		t.Fatalf("child tree = %#v", root.Children)
	}
	if detail.Steps[1].SpanID != "" || detail.Steps[1].Usage.OutputTokens != 7 {
		t.Fatalf("flat step = %#v", detail.Steps[1])
	}
}

func TestAggregateUtilizationCombinesPerIntervalDataPoints(t *testing.T) {
	// Claude Code reports one datapoint per token type at the same timestamp;
	// utilization must recombine them into one context-size sample.
	at := "2026-06-11T10:00:00Z"
	events := []schema.Event{
		usageEventFixture(at, "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(1000)
		}),
		usageEventFixture(at, "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(179000)}
		}),
		usageEventFixture(at, "claude_code", "s1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: int64Ptr(5000)}
		}),
		usageEventFixture("2026-06-11T11:00:00Z", "asymptote_observe", "s2", "experimental-model", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(123)
		}),
	}

	report := Aggregate(events, Options{})
	if len(report.Utilization) != 2 {
		t.Fatalf("utilization = %#v", report.Utilization)
	}
	claude := report.Utilization[0]
	if claude.Model != "claude-sonnet-4-5" || claude.ContextWindow != 200000 || claude.Calls != 1 {
		t.Fatalf("claude utilization = %#v", claude)
	}
	if claude.MaxInputTokens != 185000 || claude.MaxRatio != 0.925 || claude.NearLimitCalls != 1 {
		t.Fatalf("claude utilization sample = %#v", claude)
	}
	unknown := report.Utilization[1]
	if unknown.Model != "experimental-model" || unknown.ContextWindow != 0 || unknown.MaxRatio != 0 || unknown.MaxInputTokens != 123 {
		t.Fatalf("unknown model utilization = %#v", unknown)
	}
}

func TestAggregateTopLimitCapsGroups(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "model-a", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(300)
		}),
		usageEventFixture("2026-06-11T10:01:00Z", "claude_code", "s2", "model-b", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(200)
		}),
		usageEventFixture("2026-06-11T10:02:00Z", "claude_code", "s3", "model-c", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
		}),
	}
	report := Aggregate(events, Options{TopLimit: 2})
	if len(report.ByModel) != 2 || report.ByModel[0].Key != "model-a" || report.ByModel[1].Key != "model-b" {
		t.Fatalf("by_model = %#v", report.ByModel)
	}
}
