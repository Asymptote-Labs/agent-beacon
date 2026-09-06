package dashboard

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// The same model under two spellings is one model here too. This summary renders beside a spend
// rollup that groups canonically, so counting the raw string would have one page report two models
// where the other reports one.
func TestSummaryCountsOneModelUnderItsCanonicalName(t *testing.T) {
	result := EventResult{
		TotalMatched: 2,
		Events: []EventRecord{
			{Event: schema.Event{Timestamp: "2026-06-11T10:00:00Z", Model: "claude-opus-5"}},
			{Event: schema.Event{Timestamp: "2026-06-11T10:01:00Z", Model: "anthropic/Claude-Opus-5"}},
		},
	}
	summary := BuildSummary(result)
	if got := summary.CountsByModel["claude-opus-5"]; got != 2 {
		t.Errorf("counts_by_model[claude-opus-5] = %d, want 2; full map %v", got, summary.CountsByModel)
	}
	if len(summary.CountsByModel) != 1 {
		t.Errorf("counts_by_model = %v, want a single model", summary.CountsByModel)
	}
}
