package beaconevent

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"testing"
)

func TestCodexCollectorSeriesDoNotBorrowForeignCacheCounts(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		left, right            any
		secondStart, secondEnd pcommon.Timestamp
	}{
		{"numeric and string label", int64(1), "1", 1, 9},
		{"slice and string label", []any{"a", "b"}, `["a","b"]`, 1, 9},
		{"map and string label", map[string]any{"key": "value"}, `{"key":"value"}`, 1, 9},
		{"different collection start", nil, nil, 2, 9},
		{"different collection end", nil, nil, 1, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metric := pmetric.NewMetric()
			metric.SetName("codex.turn.token_usage")
			hist := metric.SetEmptyHistogram()
			hist.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
			for i, values := range []map[string]float64{{"input": 120, "cached_input": 90, "cache_write_input": 10}, {"input": 60, "cached_input": 20, "cache_write_input": 0}} {
				for kind, value := range values {
					dp := hist.DataPoints().AppendEmpty()
					dp.SetSum(value)
					dp.SetCount(1)
					dp.SetStartTimestamp(1)
					dp.SetTimestamp(9)
					dp.Attributes().PutStr("model", "alpha")
					dp.Attributes().PutStr("token_type", kind)
					label := tc.left
					if i == 1 {
						label = tc.right
						dp.SetStartTimestamp(tc.secondStart)
						dp.SetTimestamp(tc.secondEnd)
					}
					if label != nil {
						if err := dp.Attributes().PutEmpty("series.label").FromRaw(label); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			// The source records 180 inclusive tokens, of which120 are cached. Across
			// these independent series the canonical uncached total must therefore60.
			var uncached, inclusive int64
			for _, event := range NewConverter(Options{}).EventsFromMetric(map[string]interface{}{"beacon.harness.name": "codex_cli"}, metric) {
				if event.GenAI == nil || event.GenAI.Usage == nil {
					continue
				}
				u := event.GenAI.Usage
				if u.InputTokens != nil {
					uncached += *u.InputTokens
					inclusive += *u.InputTokens
				}
				if u.CacheRead != nil && u.CacheRead.InputTokens != nil {
					inclusive += *u.CacheRead.InputTokens
				}
				if u.CacheCreation != nil && u.CacheCreation.InputTokens != nil {
					inclusive += *u.CacheCreation.InputTokens
				}
			}
			if uncached != 60 || inclusive != 180 {
				t.Fatalf("uncached=%d want60; reconstructed inclusive=%d want180", uncached, inclusive)
			}
		})
	}
}
