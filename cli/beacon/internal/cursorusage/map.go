package cursorusage

import (
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// MetricName marks synced events as a metric-style usage channel in
// raw.metric_name. The tokens rollup keys channel dedupe on this field: if
// Cursor ever ships usage in hook payloads, those hook events (which have no
// metric_name) take precedence per (harness, session) and synced events stop
// counting, so the two sources never double count.
const MetricName = "cursor.db.token.usage"

// EventFromGeneration converts one extracted generation into a canonical
// token.usage endpoint event attributed to the cursor harness. session.id is
// the composer id, which matches the conversation_id Cursor hooks record, so
// per-session rollups join hook activity with synced usage.
func EventFromGeneration(g Generation) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   "token.usage",
		Category: "metric",
		Severity: schema.SeverityInfo,
		Harness:  schema.HarnessInfo{Name: "cursor"},
		Message:  "Cursor token usage (state.vscdb)",
		Origin:   schema.OriginLocal,
	})
	if !g.Timestamp.IsZero() {
		ev.Timestamp = g.Timestamp.UTC().Format(time.RFC3339)
	}
	if g.ComposerID != "" {
		ev.Session = &schema.SessionInfo{ID: g.ComposerID}
	}
	ev.Model = g.Model
	usage := &schema.GenAIUsageInfo{
		InputTokens:  g.InputTokens,
		OutputTokens: g.OutputTokens,
		CostUSD:      g.CostUSD,
	}
	if g.CacheReadTokens != nil {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: g.CacheReadTokens}
	}
	if g.CacheCreationTokens != nil {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: g.CacheCreationTokens}
	}
	if g.ReasoningTokens != nil {
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: g.ReasoningTokens}
	}
	ev.GenAI = &schema.GenAIInfo{Usage: usage}
	ev.Raw = map[string]interface{}{
		"metric_name": MetricName,
		"cursor": map[string]interface{}{
			"composer_id":      g.ComposerID,
			"bubble_id":        g.BubbleID,
			"source":           "state.vscdb",
			"timestamp_source": g.TimestampSource,
		},
	}
	return ev
}
