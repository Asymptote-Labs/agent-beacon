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

func TestAggregateDedupesDualChannelUsage(t *testing.T) {
	// Claude Code reports each request's usage on both a claude_code.api_request
	// log record (no metric_name) and the claude_code.token.usage metric, under
	// the base and 1M-context model names respectively. Counting both doubles
	// every token field; cost rides only on claude_code.cost.usage.
	metric := func(name string, mutate func(*schema.Event)) schema.Event {
		return usageEventFixture("2026-06-11T10:00:05Z", "claude_code", "s1", "claude-opus-4-6[1m]", func(e *schema.Event) {
			mutate(e)
			e.Raw = map[string]interface{}{"metric_name": name, "metric_temporality": "Delta"}
		})
	}
	events := []schema.Event{
		// Log/span channel: full per-request usage, base model name, no cost.
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-4-6", func(e *schema.Event) {
			e.Event.Action = "tool.invoked"
			e.Message = "claude_code.api_request"
			e.GenAI.Usage.InputTokens = int64Ptr(11)
			e.GenAI.Usage.OutputTokens = int64Ptr(826)
			e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(119393)}
			e.GenAI.Usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: int64Ptr(15751)}
		}),
		// Metric channel: same tokens, field-split (must be suppressed) plus cost (must survive).
		metric("claude_code.token.usage", func(e *schema.Event) { e.GenAI.Usage.InputTokens = int64Ptr(11) }),
		metric("claude_code.token.usage", func(e *schema.Event) { e.GenAI.Usage.OutputTokens = int64Ptr(826) }),
		metric("claude_code.token.usage", func(e *schema.Event) {
			e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(119393)}
		}),
		metric("claude_code.token.usage", func(e *schema.Event) {
			e.GenAI.Usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: int64Ptr(15751)}
		}),
		metric("claude_code.cost.usage", func(e *schema.Event) { e.GenAI.Usage.CostUSD = float64Ptr(0.1788) }),
		// Metrics-only scope (no log/span channel): must be left untouched.
		usageEventFixture("2026-06-11T10:01:00Z", "codex", "s2", "gpt-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(500)
			e.Raw = map[string]interface{}{"metric_name": "gen_ai.client.token.usage", "metric_temporality": "Delta"}
		}),
	}

	report := Aggregate(events, Options{})
	if got := report.Totals; got.InputTokens != 511 || got.OutputTokens != 826 ||
		got.CacheReadInputTokens != 119393 || got.CacheCreationInputTokens != 15751 || got.CostUSD != 0.1788 {
		t.Fatalf("totals double-counted or dropped: %#v", got)
	}
	// Base model name carries the deduped tokens; the [1m] metric name carries cost only.
	byModel := map[string]Usage{}
	for _, g := range report.ByModel {
		byModel[g.Key] = g.Usage
	}
	if base := byModel["claude-opus-4-6"]; base.InputTokens != 11 || base.OutputTokens != 826 || base.CacheReadInputTokens != 119393 {
		t.Fatalf("base model tokens = %#v", base)
	}
	if oneM := byModel["claude-opus-4-6[1m]"]; oneM.TotalTokens() != 0 || oneM.CostUSD != 0.1788 {
		t.Fatalf("[1m] model should carry cost only, got %#v", oneM)
	}
	if codex := byModel["gpt-5"]; codex.InputTokens != 500 {
		t.Fatalf("metrics-only scope was dropped: %#v", codex)
	}
}

func TestAggregatePrefersCodexSessionUsageOverOTLP(t *testing.T) {
	sessionEvent := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "codex:s1", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(40)
		e.GenAI.Usage.OutputTokens = int64Ptr(10)
		e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(60)}
		e.Raw = map[string]interface{}{"source": "codex_session_jsonl", "dedup_key": "session-turn-1"}
	})
	otlpEvent := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(40)
		e.GenAI.Usage.OutputTokens = int64Ptr(10)
		e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(60)}
		e.Raw = map[string]interface{}{"metric_name": "codex.turn.token_usage", "metric_temporality": "Delta"}
	})

	report := Aggregate([]schema.Event{sessionEvent, otlpEvent}, Options{})
	if report.Totals.InputTokens != 40 || report.Totals.OutputTokens != 10 || report.Totals.CacheReadInputTokens != 60 {
		t.Fatalf("auto totals = %#v, want session event only", report.Totals)
	}
	if report.Source == nil || report.Source.Codex != "session files with OTLP fallback (overlaps suppressed)" || report.Source.CodexOTLPSuppressed != 1 {
		t.Fatalf("source summary = %#v", report.Source)
	}
	if len(report.BySession) != 1 || report.BySession[0].Key != "codex:s1" {
		t.Fatalf("by_session = %#v, want session-derived attribution", report.BySession)
	}

	otlpOnly := Aggregate([]schema.Event{sessionEvent, otlpEvent}, Options{CodexSource: CodexSourceOTLP})
	if otlpOnly.Totals.InputTokens != 40 || len(otlpOnly.BySession) != 0 {
		t.Fatalf("otlp-only report = totals %#v sessions %#v", otlpOnly.Totals, otlpOnly.BySession)
	}

	debug := Aggregate([]schema.Event{sessionEvent, otlpEvent}, Options{CodexSource: CodexSourceBothDebug})
	if debug.Totals.InputTokens != 80 || debug.Source == nil || !debug.Source.Diagnostic {
		t.Fatalf("both-debug report = totals %#v source %#v", debug.Totals, debug.Source)
	}
}

func TestAggregateAutoKeepsNonOverlappingCodexOTLPUsage(t *testing.T) {
	sessionEvent := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "codex:s1", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(40)
		e.GenAI.Usage.OutputTokens = int64Ptr(10)
		e.Raw = map[string]interface{}{"source": "codex_session_jsonl", "dedup_key": "session-turn-1"}
	})
	otlpOnlyEvent := usageEventFixture("2026-06-11T10:01:00Z", "codex_cli", "", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(9)
		e.GenAI.Usage.OutputTokens = int64Ptr(3)
		e.Raw = map[string]interface{}{"metric_name": "codex.turn.token_usage", "metric_temporality": "Delta"}
	})

	report := Aggregate([]schema.Event{sessionEvent, otlpOnlyEvent}, Options{})
	if report.Totals.InputTokens != 49 || report.Totals.OutputTokens != 13 || report.Totals.Events != 2 {
		t.Fatalf("auto totals = %#v, want session plus non-overlapping OTLP", report.Totals)
	}
	if report.Source == nil || report.Source.Codex != "session files with OTLP fallback" || report.Source.CodexOTLPSuppressed != 0 {
		t.Fatalf("source summary = %#v", report.Source)
	}
}

func TestAggregateAutoSuppressesSplitCodexOTLPDatapoints(t *testing.T) {
	sessionEvent := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "codex:s1", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(40)
		e.GenAI.Usage.OutputTokens = int64Ptr(10)
		e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(60)}
		e.Raw = map[string]interface{}{"source": "codex_session_jsonl", "dedup_key": "session-turn-1"}
	})
	otlpInput := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(40)
		e.Raw = map[string]interface{}{"metric_name": "codex.turn.token_usage", "metric_temporality": "Delta"}
	})
	otlpOutput := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.OutputTokens = int64Ptr(10)
		e.Raw = map[string]interface{}{"metric_name": "codex.turn.token_usage", "metric_temporality": "Delta"}
	})
	otlpCache := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(60)}
		e.Raw = map[string]interface{}{"metric_name": "codex.turn.token_usage", "metric_temporality": "Delta"}
	})

	report := Aggregate([]schema.Event{sessionEvent, otlpInput, otlpOutput, otlpCache}, Options{})
	if report.Totals.InputTokens != 40 || report.Totals.OutputTokens != 10 || report.Totals.CacheReadInputTokens != 60 || report.Totals.Events != 1 {
		t.Fatalf("auto totals = %#v, want session event only", report.Totals)
	}
	if report.Source == nil || report.Source.CodexOTLPSuppressed != 3 {
		t.Fatalf("source summary = %#v, want three suppressed OTLP datapoints", report.Source)
	}
}

func TestAggregateDedupesCodexSessionUsageByRawDedupKey(t *testing.T) {
	event := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "codex:s1", "gpt-5", func(e *schema.Event) {
		e.GenAI.Usage.InputTokens = int64Ptr(40)
		e.GenAI.Usage.OutputTokens = int64Ptr(10)
		e.Raw = map[string]interface{}{"source": "codex_session_jsonl", "dedup_key": "same-turn"}
	})
	duplicate := event

	report := Aggregate([]schema.Event{event, duplicate}, Options{})
	if report.Totals.InputTokens != 40 || report.Totals.OutputTokens != 10 || report.Totals.Events != 1 {
		t.Fatalf("duplicate session totals = %#v, want one contribution", report.Totals)
	}
	if report.TotalEvents != 2 || report.EventsWithUsage != 1 {
		t.Fatalf("event counts = %d/%d, want 1 usage event from 2 log events", report.EventsWithUsage, report.TotalEvents)
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

func TestAggregateScopedFiltersSessionAndRun(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "Session-1", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
			e.Run = &schema.RunInfo{Provider: "github_actions", RunID: "777"}
		}),
		usageEventFixture("2026-06-11T10:01:00Z", "claude_code", "session-2", "claude-sonnet-4-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(50)
			e.Run = &schema.RunInfo{Provider: "github_actions", RunID: "888"}
		}),
	}

	// Session match is exact but case-insensitive.
	report := AggregateScoped(events, "", Options{SessionID: "session-1"})
	if report.Totals.InputTokens != 100 || report.TotalEvents != 1 {
		t.Fatalf("session scope totals = %#v (events=%d), want only session-1", report.Totals, report.TotalEvents)
	}
	if report.SessionDetail == nil || report.SessionDetail.SessionID != "session-1" {
		t.Fatalf("session detail = %#v", report.SessionDetail)
	}

	// Run id accepts both the bare id and the composite provider/run_id key.
	for _, runID := range []string{"888", "github_actions/888"} {
		got := AggregateScoped(events, runID, Options{})
		if got.Totals.InputTokens != 50 || got.TotalEvents != 1 {
			t.Fatalf("run scope %q totals = %#v (events=%d), want only run 888", runID, got.Totals, got.TotalEvents)
		}
		if len(got.ByRun) != 1 || got.ByRun[0].Key != "github_actions/888" {
			t.Fatalf("run scope %q by_run = %#v", runID, got.ByRun)
		}
	}

	// No scope keeps every event.
	if all := AggregateScoped(events, "", Options{}); all.Totals.InputTokens != 150 || all.TotalEvents != 2 {
		t.Fatalf("unscoped totals = %#v (events=%d), want all events", all.Totals, all.TotalEvents)
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
