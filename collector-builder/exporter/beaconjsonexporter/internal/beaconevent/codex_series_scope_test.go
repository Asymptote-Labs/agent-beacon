package beaconevent

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pmetric"
)

// One export can hold several Codex series at the same timestamp (a model switch mid-interval).
// Each input must be reduced by its own series' cache subsets, not by every subset collected then.
func TestCodexInputSubtractsOnlyItsOwnSeriesCacheCounts(t *testing.T) {
	metric := pmetric.NewMetric()
	metric.SetName("codex.turn.token_usage")
	hist := metric.SetEmptyHistogram()
	hist.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	for model, values := range map[string]map[string]float64{
		"alpha": {"input": 120, "cached_input": 90, "cache_write_input": 10},
		"beta":  {"input": 60, "cached_input": 20},
	} {
		for tokenType, value := range values {
			dp := hist.DataPoints().AppendEmpty()
			dp.SetSum(value)
			dp.SetCount(1)
			dp.SetTimestamp(9)
			dp.Attributes().PutStr("model", model)
			dp.Attributes().PutStr("token_type", tokenType)
		}
	}

	uncached := map[string]int64{}
	for _, event := range NewConverter(Options{}).EventsFromMetric(map[string]interface{}{"beacon.harness.name": "codex_cli"}, metric) {
		if event.GenAI == nil || event.GenAI.Usage == nil || event.GenAI.Usage.InputTokens == nil {
			continue
		}
		uncached[event.Model] += *event.GenAI.Usage.InputTokens
	}
	if uncached["alpha"] != 20 || uncached["beta"] != 40 {
		t.Fatalf("uncached input by model = %v, want alpha=20 beta=40", uncached)
	}
}
