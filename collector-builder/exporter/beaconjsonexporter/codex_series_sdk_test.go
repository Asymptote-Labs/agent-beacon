package beaconjsonexporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Codex emits this histogram separately for each actual response model. The
// native SDK collection supplies the shared end timestamp; no clock is faked.
func TestCodexUsageSeriesFromNativeSDKRemainPerModel(t *testing.T) {
	for _, models := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		t.Run(strings.Join(models, "+"), func(t *testing.T) {
			ctx := context.Background()
			reader := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(func(sdkmetric.InstrumentKind) metricdata.Temporality { return metricdata.DeltaTemporality }))
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() { _ = provider.Shutdown(ctx) })
			histogram, err := provider.Meter("codex-series-proof").Int64Histogram("codex.turn.token_usage")
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]int64{"alpha": 20, "beta": 40}
			values := map[string]map[string]int64{"alpha": {"input": 120, "cached_input": 90, "cache_write_input": 10}, "beta": {"input": 60, "cached_input": 20, "cache_write_input": 0}}
			for _, model := range models {
				for kind, value := range values[model] {
					histogram.Record(ctx, value, metric.WithAttributes(attribute.String("model", model), attribute.String("token_type", kind)))
				}
			}
			var collected metricdata.ResourceMetrics
			if err = reader.Collect(ctx, &collected); err != nil {
				t.Fatal(err)
			}
			native := collected.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[int64])
			wire := pmetric.NewMetrics()
			resource := wire.ResourceMetrics().AppendEmpty()
			resource.Resource().Attributes().PutStr("beacon.harness.name", "codex_cli")
			m := resource.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
			m.SetName("codex.turn.token_usage")
			h := m.SetEmptyHistogram()
			h.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
			// Transfer native SDK datapoints to the collector's OTLP representation,
			// preserving their complete labels, times and independently recorded sums.
			timestamp := native.DataPoints[0].Time
			for _, dp := range native.DataPoints {
				if !dp.Time.Equal(timestamp) {
					t.Fatal("SDK collection did not share an end timestamp")
				}
				point := h.DataPoints().AppendEmpty()
				point.SetTimestamp(pcommon.NewTimestampFromTime(dp.Time))
				point.SetStartTimestamp(pcommon.NewTimestampFromTime(dp.StartTime))
				point.SetSum(float64(dp.Sum))
				point.SetCount(dp.Count)
				point.ExplicitBounds().FromRaw(dp.Bounds)
				point.BucketCounts().FromRaw(dp.BucketCounts)
				for _, kv := range dp.Attributes.ToSlice() {
					if err := point.Attributes().PutEmpty(string(kv.Key)).FromRaw(kv.Value.AsInterface()); err != nil {
						t.Fatal(err)
					}
				}
			}
			encoded, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(wire)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("native SDK OTLP representation=%s", encoded)
			decoded, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(encoded)
			if err != nil {
				t.Fatal(err)
			}
			cfg := createDefaultConfig()
			cfg.Path = filepath.Join(t.TempDir(), "runtime.jsonl")
			exp, err := newExporter(cfg, exporter.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			if err = exp.consumeMetrics(ctx, decoded); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(cfg.Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("actual exporter JSONL=%s", data)
			input := map[string]int64{}
			inclusive := map[string]int64{}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var event asymptoteobserve.Event
				if err = json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event.GenAI == nil || event.GenAI.Usage == nil {
					continue
				}
				u := event.GenAI.Usage
				if u.InputTokens != nil {
					input[event.Model] += *u.InputTokens
					inclusive[event.Model] += *u.InputTokens
				}
				if u.CacheRead != nil && u.CacheRead.InputTokens != nil {
					inclusive[event.Model] += *u.CacheRead.InputTokens
				}
				if u.CacheCreation != nil && u.CacheCreation.InputTokens != nil {
					inclusive[event.Model] += *u.CacheCreation.InputTokens
				}
			}
			for _, model := range models {
				if input[model] != expected[model] || inclusive[model] != values[model]["input"] {
					t.Errorf("model %s: uncached=%d want%d; reconstructed inclusive=%d want%d", model, input[model], expected[model], inclusive[model], values[model]["input"])
				}
			}
			t.Logf("native SDK datapoints=%d; exported uncached=%v inclusive=%v", len(native.DataPoints), input, inclusive)
		})
	}
}
