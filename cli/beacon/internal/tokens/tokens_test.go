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

func TestAggregateAttributesUsageToSessionUserContext(t *testing.T) {
	contextEvent := func(host, user, uid string) schema.Event {
		return schema.Event{
			Timestamp: "2026-06-11T09:59:00Z",
			Event:     schema.EventInfo{Kind: "agent_runtime", Action: "session.context", Category: "session"},
			Endpoint:  schema.EndpointInfo{Hostname: host, OS: "darwin"},
			User:      schema.UserInfo{Name: user, UID: uid},
			Harness:   schema.HarnessInfo{Name: "codex_cli"},
			Session:   &schema.SessionInfo{ID: "shared-session"},
		}
	}
	usageEvent := func(host string, input int64) schema.Event {
		return usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "shared-session", "gpt-5.6-sol", func(e *schema.Event) {
			e.Endpoint = schema.EndpointInfo{Hostname: host, OS: "darwin"}
			e.User = schema.UserInfo{Name: "root", UID: "0"}
			e.GenAI.Usage.InputTokens = int64Ptr(input)
		})
	}

	report := Aggregate([]schema.Event{
		contextEvent("host-a", "alice", "501"),
		usageEvent("host-a", 10),
		contextEvent("host-b", "bob", "502"),
		usageEvent("host-b", 20),
	}, Options{})

	if len(report.ByUser) != 2 {
		t.Fatalf("by_user = %#v, want two endpoint-scoped users", report.ByUser)
	}
	users := map[string]Usage{}
	for _, group := range report.ByUser {
		users[group.Key] = group.Usage
	}
	if users["alice [501]"].InputTokens != 10 || users["bob [502]"].InputTokens != 20 {
		t.Fatalf("by_user = %#v, want session context to override collector identity", users)
	}
}

func TestAggregateAttributesAuxiliaryCodexSessionToUniqueEndpointUser(t *testing.T) {
	contextEvent := schema.Event{
		Timestamp: "2026-06-11T09:59:00Z",
		Event:     schema.EventInfo{Kind: "agent_runtime", Action: "session.context", Category: "session"},
		Endpoint:  schema.EndpointInfo{Hostname: "host-a", OS: "darwin"},
		User:      schema.UserInfo{Name: "shukan", UID: "501"},
		Harness:   schema.HarnessInfo{Name: "codex_cli"},
		Session:   &schema.SessionInfo{ID: "main-session"},
	}
	auxiliaryStarted := schema.Event{
		Timestamp: "2026-06-11T10:00:00Z",
		Event:     schema.EventInfo{Kind: "agent_runtime", Action: "session.started", Category: "session"},
		Endpoint:  schema.EndpointInfo{Hostname: "host-a", OS: "darwin"},
		User:      schema.UserInfo{Name: "shukan"},
		Harness:   schema.HarnessInfo{Name: "codex_cli"},
		Session:   &schema.SessionInfo{ID: "title-session"},
	}
	mainUsage := usageEventFixture("2026-06-11T10:00:05Z", "codex_cli", "main-session", "gpt-5.6-sol", func(e *schema.Event) {
		e.Endpoint = schema.EndpointInfo{Hostname: "host-a", OS: "darwin"}
		e.User = schema.UserInfo{Name: "shukan"}
		e.GenAI.Usage.InputTokens = int64Ptr(10)
	})
	auxiliaryUsage := usageEventFixture("2026-06-11T10:00:06Z", "codex_cli", "title-session", "gpt-5.6-sol", func(e *schema.Event) {
		e.Endpoint = schema.EndpointInfo{Hostname: "host-a", OS: "darwin"}
		e.User = schema.UserInfo{Name: "shukan"}
		e.GenAI.Usage.InputTokens = int64Ptr(20)
	})

	report := Aggregate([]schema.Event{contextEvent, auxiliaryStarted, mainUsage, auxiliaryUsage}, Options{})
	if len(report.ByUser) != 1 || report.ByUser[0].Key != "shukan [501]" || report.ByUser[0].Usage.InputTokens != 30 {
		t.Fatalf("by_user = %#v, want both Codex sessions attributed to the unique endpoint user", report.ByUser)
	}
	scoped := AggregateScoped(
		[]schema.Event{contextEvent, auxiliaryStarted, mainUsage, auxiliaryUsage},
		"",
		Options{SessionID: "title-session"},
	)
	if len(scoped.ByUser) != 1 || scoped.ByUser[0].Key != "shukan [501]" || scoped.ByUser[0].Usage.InputTokens != 20 {
		t.Fatalf("scoped by_user = %#v, want auxiliary session attributed from unfiltered context", scoped.ByUser)
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

func TestAggregatePrefersCodexTurnSpansAfterCutover(t *testing.T) {
	codexMetric := func(ts string, input int64) schema.Event {
		return usageEventFixture(ts, "codex_cli", "", "gpt-5.6-sol", func(e *schema.Event) {
			e.Endpoint = schema.EndpointInfo{Hostname: "host-a", OS: "darwin"}
			e.GenAI.Usage.InputTokens = int64Ptr(input)
			e.Raw = map[string]interface{}{
				"metric_name":        "codex.turn.token_usage",
				"metric_temporality": "Delta",
			}
		})
	}
	turnSpan := usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "session-1", "gpt-5.6-sol", func(e *schema.Event) {
		e.Endpoint = schema.EndpointInfo{Hostname: "host-a", OS: "darwin"}
		e.GenAI.Usage.InputTokens = int64Ptr(20)
		e.Raw = map[string]interface{}{
			"source":               "codex_turn_span",
			"turn_id":              "turn-1",
			"turn_start_timestamp": "2026-06-11T09:59:55Z",
		}
	})
	events := []schema.Event{
		// A pre-upgrade metric remains part of historical totals.
		codexMetric("2026-06-11T09:00:00Z", 10),
		turnSpan,
		// The same live turn also flushed through a manually retained metric
		// exporter; the turn span wins after cutover.
		codexMetric("2026-06-11T09:59:59Z", 20),
	}

	report := Aggregate(events, Options{})
	if report.Totals.InputTokens != 30 || report.Totals.Events != 2 {
		t.Fatalf("totals = %#v, want legacy metric plus turn span only", report.Totals)
	}
	if len(report.BySession) != 1 || report.BySession[0].Key != "session-1" {
		t.Fatalf("by_session = %#v, want attributable turn span", report.BySession)
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

// A runtime that reports its own context size and window is measured against those, not against
// the static model table and not against a sum of usage fields.
//
// Both substitutions matter. Summing input + cache read + cache creation approximates occupancy and
// is all Beacon has for runtimes that report nothing; a reported number is the measurement itself.
// And a reported window reflects the tier the call actually ran under, which a table keyed on a
// model name cannot know.
func TestUtilizationPrefersReportedContextOverInference(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "qwen_code", "s1", "qwen3-coder-plus", func(e *schema.Event) {
			e.GenAI.Usage = nil
			e.GenAI.Context = &schema.GenAIContextInfo{
				UsedTokens:  int64Ptr(110100),
				LimitTokens: int64Ptr(262144),
			}
		}),
	}
	report := Aggregate(events, Options{})
	if len(report.Utilization) != 1 {
		t.Fatalf("utilization = %+v, want one row for a context-only event", report.Utilization)
	}
	row := report.Utilization[0]
	if row.ContextWindow != 262144 {
		t.Errorf("context window = %d, want the runtime's reported 262144", row.ContextWindow)
	}
	if row.MaxInputTokens != 110100 {
		t.Errorf("max input = %d, want the runtime's reported 110100", row.MaxInputTokens)
	}
	// 110100 / 262144 is the 0.42 Qwen reports as context_usage, which is how the fixture's three
	// fields are known to describe one measurement.
	if row.MaxRatio < 0.419 || row.MaxRatio > 0.421 {
		t.Errorf("max ratio = %v, want ~0.42", row.MaxRatio)
	}
}

// Context is a level, not spend. A context-only event must not add anything a report sums, or a
// Qwen session's cost would grow with the square of its length.
//
// "Anything" is the whole shape, not just the token and cost fields. An earlier version of this
// test checked only those two and passed while the event was still incrementing the event counts
// and creating zero-token rows under every grouping -- which reads as a runtime that spent nothing
// but was nonetheless active in a spend report, and which the coverage report in beacon
// token-usage --coverage would have read as "this runtime is reporting its spend".
func TestContextOnlyEventsAddNothingToTotals(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "qwen_code", "s1", "qwen3-coder-plus", func(e *schema.Event) {
			e.GenAI.Usage = nil
			e.GenAI.Context = &schema.GenAIContextInfo{
				UsedTokens:  int64Ptr(110100),
				LimitTokens: int64Ptr(262144),
			}
		}),
	}
	report := Aggregate(events, Options{BucketSize: time.Hour, SessionID: "s1"})
	if got := report.Totals.TotalTokens(); got != 0 {
		t.Errorf("totals = %d tokens, want 0 -- context occupancy is not spend", got)
	}
	if report.Totals.CostUSD != 0 {
		t.Errorf("totals cost = %v, want 0", report.Totals.CostUSD)
	}
	if report.Totals.Events != 0 {
		t.Errorf("totals events = %d, want 0 -- the event carries no usage to have counted", report.Totals.Events)
	}
	if report.EventsWithUsage != 0 {
		t.Errorf("events_with_usage = %d, want 0", report.EventsWithUsage)
	}
	for name, group := range map[string][]Group{
		"by_model": report.ByModel, "by_session": report.BySession,
		"by_harness": report.ByHarness, "by_user": report.ByUser,
		"by_repository": report.ByRepository, "by_run": report.ByRun,
	} {
		if len(group) != 0 {
			t.Errorf("%s = %+v, want no rows -- a zero-token row reads as a runtime that spent nothing but was active", name, group)
		}
	}
	if len(report.Series) != 0 {
		t.Errorf("series = %+v, want no buckets", report.Series)
	}
	if report.SessionDetail != nil && len(report.SessionDetail.Steps) != 0 {
		t.Errorf("session steps = %+v, want none", report.SessionDetail.Steps)
	}
	// The one thing it must still do.
	if len(report.Utilization) != 1 {
		t.Fatalf("utilization = %+v, want the context-only event to still be measured", report.Utilization)
	}
}

// An event reporting both spend and context is not context-only: its usage counts normally and its
// context still feeds utilization.
func TestEventsWithBothUsageAndContextCountAsSpend(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "some_runtime", "s1", "some-model", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(900)
			e.GenAI.Usage.OutputTokens = int64Ptr(100)
			e.GenAI.Context = &schema.GenAIContextInfo{
				UsedTokens:  int64Ptr(50000),
				LimitTokens: int64Ptr(200000),
			}
		}),
	}
	report := Aggregate(events, Options{})
	if got := report.Totals.TotalTokens(); got != 1000 {
		t.Errorf("totals = %d, want the reported 1000 spend tokens", got)
	}
	if report.EventsWithUsage != 1 {
		t.Errorf("events_with_usage = %d, want 1", report.EventsWithUsage)
	}
	if len(report.Utilization) != 1 || report.Utilization[0].MaxInputTokens != 50000 {
		t.Errorf("utilization = %+v, want the reported 50000 occupancy, not the 900 input", report.Utilization)
	}
}

// A runtime that reports no context keeps the inferred behaviour and the static window.
func TestUtilizationStillInfersWhenNoContextIsReported(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(1000)
			e.GenAI.Usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: int64Ptr(500)}
		}),
	}
	report := Aggregate(events, Options{})
	if len(report.Utilization) != 1 || report.Utilization[0].MaxInputTokens != 1500 {
		t.Fatalf("utilization = %+v, want the summed 1500", report.Utilization)
	}
}

// The real Qwen payload, not the fixture shape the other utilization tests use. Qwen declares its
// model on session start and never again: its Stop hook -- the only producer of gen_ai.context --
// carries session_id, context_usage, context_limit and input_tokens, and no model at all. Since
// buildUtilization keys rows on the model and skips events without one, occupancy was recorded on
// the event and then dropped before reaching the report this change exists to fill. The session's
// model is the right attribution because the events belong to one session that declared it.
func TestUtilizationAttributesContextToTheSessionModelWhenTheEventCarriesNone(t *testing.T) {
	events := []schema.Event{
		{
			Timestamp: "2026-06-11T10:00:00Z",
			Event:     schema.EventInfo{Kind: "agent_runtime", Action: "session.start", Category: "session"},
			Harness:   schema.HarnessInfo{Name: "qwen_code"},
			Session:   &schema.SessionInfo{ID: "qwen-8f21c4a0"},
			Model:     "qwen3-coder-plus",
		},
		{
			Timestamp: "2026-06-11T10:01:00Z",
			Event:     schema.EventInfo{Kind: "agent_runtime", Action: "tool.completed", Category: "tool"},
			Harness:   schema.HarnessInfo{Name: "qwen_code"},
			Session:   &schema.SessionInfo{ID: "qwen-8f21c4a0"},
			GenAI: &schema.GenAIInfo{Context: &schema.GenAIContextInfo{
				UsedTokens:  int64Ptr(110100),
				LimitTokens: int64Ptr(262144),
			}},
		},
	}
	report := Aggregate(events, Options{})
	if len(report.Utilization) != 1 {
		t.Fatalf("utilization = %+v, want one row attributed to the session model", report.Utilization)
	}
	row := report.Utilization[0]
	if row.Model != "qwen3-coder-plus" {
		t.Errorf("model = %q, want the model the session declared", row.Model)
	}
	if row.MaxInputTokens != 110100 {
		t.Errorf("max input tokens = %d, want the reported 110100", row.MaxInputTokens)
	}
	if row.ContextWindow != 262144 {
		t.Errorf("context window = %d, want the reported 262144", row.ContextWindow)
	}
	// The carry-forward is for attribution only. Nothing about it makes occupancy spend.
	if report.Totals.TotalTokens() != 0 {
		t.Errorf("totals = %d, want 0", report.Totals.TotalTokens())
	}
	if report.EventsWithUsage != 0 {
		t.Errorf("events with usage = %d, want 0", report.EventsWithUsage)
	}
}

// The carry-forward must not reach spend. An event reporting real usage with no model of its own
// is attributed as it always was -- summed into the totals, absent from the per-model rows -- and
// changing that would silently move other runtimes' spend between models.
func TestSessionModelCarryForwardDoesNotRelabelSpend(t *testing.T) {
	events := []schema.Event{
		{
			Timestamp: "2026-06-11T10:00:00Z",
			Event:     schema.EventInfo{Kind: "agent_runtime", Action: "session.start", Category: "session"},
			Harness:   schema.HarnessInfo{Name: "claude_code"},
			Session:   &schema.SessionInfo{ID: "s1"},
			Model:     "claude-opus-5",
		},
		usageEventFixture("2026-06-11T10:01:00Z", "claude_code", "s1", "", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
			e.GenAI.Usage.OutputTokens = int64Ptr(20)
		}),
	}
	report := Aggregate(events, Options{})
	if report.Totals.TotalTokens() != 120 {
		t.Fatalf("totals = %d, want 120", report.Totals.TotalTokens())
	}
	for _, group := range report.ByModel {
		if group.Key == "claude-opus-5" {
			t.Errorf("by-model has a claude-opus-5 row (%+v); spend with no model of its own must not be relabelled", group)
		}
	}
}
