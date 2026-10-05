package tokens

import (
	"encoding/json"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
)

// decodeEvents reads JSONL-shaped events the way the report does, so these tests exercise the
// wire field rather than a Go struct literal.
func decodeEvents(t *testing.T, lines ...string) []schema.Event {
	t.Helper()
	out := make([]schema.Event, 0, len(lines))
	for _, line := range lines {
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		out = append(out, event)
	}
	return out
}

// claudePollLine is a Claude Code session-file token.usage event (one API response, so request
// scoped) on claude-opus-4-6: $5 in, $25 out, $0.50 cache read, $6.25 five-minute cache write and
// $10 one-hour cache write per MTok.
func claudePollLine(ts, cacheCreation string) string {
	return `{"timestamp":"` + ts + `","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},` +
		`"harness":{"name":"claude_code","collection_method":"poll"},"session":{"id":"s1"},"model":"claude-opus-4-6",` +
		`"gen_ai":{"usage":{"input_tokens":10,"output_tokens":100,"cache_creation":` + cacheCreation + `}}}`
}

func TestEstimatePricesOneHourCacheWritesAtTheOneHourRate(t *testing.T) {
	cases := []struct {
		name          string
		cacheCreation string
		// microdollars, worked by hand: 10x5 + 100x25 = 2550 for input and output, plus the writes.
		estimated int64
		oneHour   int64
	}{
		{
			// 4000 five-minute writes x $6.25 + 16000 one-hour writes x $10 = 25000 + 160000.
			name:          "split reported",
			cacheCreation: `{"input_tokens":20000,"ephemeral_1h_input_tokens":16000}`,
			estimated:     2550 + 25000 + 160000,
			oneHour:       16000,
		},
		{
			// Without the split every write is a five-minute write: 20000 x $6.25.
			name:          "split not reported",
			cacheCreation: `{"input_tokens":20000}`,
			estimated:     2550 + 125000,
		},
		{
			// A breakdown cannot exceed the count it breaks down: all 20000 are one-hour writes.
			name:          "split larger than the write count is clamped",
			cacheCreation: `{"input_tokens":20000,"ephemeral_1h_input_tokens":30000}`,
			estimated:     2550 + 200000,
			oneHour:       20000,
		},
		{
			name:          "negative split is ignored",
			cacheCreation: `{"input_tokens":20000,"ephemeral_1h_input_tokens":-5}`,
			estimated:     2550 + 125000,
		},
		{
			name:          "zero split",
			cacheCreation: `{"input_tokens":20000,"ephemeral_1h_input_tokens":0}`,
			estimated:     2550 + 125000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Aggregate(decodeEvents(t, claudePollLine("2026-06-11T10:00:00Z", tc.cacheCreation)), Options{})
			got := report.Totals
			if got.estimated != pricing.Microdollars(tc.estimated) {
				t.Fatalf("estimated = %d, want %d", got.estimated, tc.estimated)
			}
			if got.CacheCreation1hInputTokens != tc.oneHour {
				t.Fatalf("cache_creation_1h_input_tokens = %d, want %d", got.CacheCreation1hInputTokens, tc.oneHour)
			}
			// The subset is a breakdown, never an addition to the total.
			if got.TotalTokens() != 10+100+20000 {
				t.Fatalf("total tokens = %d, want the one-hour subset left out", got.TotalTokens())
			}
		})
	}
}

// Repeated events sum the subset with the count it belongs to, and the report JSON carries it
// only when it is non-zero, so reports over sources without the split are unchanged.
func TestOneHourSubsetSumsAndIsOmittedWhenAbsent(t *testing.T) {
	report := Aggregate(decodeEvents(t,
		claudePollLine("2026-06-11T10:00:00Z", `{"input_tokens":20000,"ephemeral_1h_input_tokens":16000}`),
		claudePollLine("2026-06-11T10:00:01Z", `{"input_tokens":1000,"ephemeral_1h_input_tokens":1000}`),
		claudePollLine("2026-06-11T10:00:02Z", `{"input_tokens":500}`),
	), Options{})
	if got := report.Totals; got.CacheCreationInputTokens != 21500 || got.CacheCreation1hInputTokens != 17000 {
		t.Fatalf("totals = %+v, want 21500 writes of which 17000 one-hour", got)
	}
	// 2550x3 + (4000+0+500)x6.25 + 17000x10 = 7650 + 28125 + 170000.
	if got := report.Totals.estimated; got != 205775 {
		t.Fatalf("estimated = %d, want 205775", got)
	}
	encoded, _ := json.Marshal(report.Totals)
	var totals map[string]interface{}
	_ = json.Unmarshal(encoded, &totals)
	if totals["cache_creation_1h_input_tokens"] != float64(17000) {
		t.Fatalf("totals JSON = %s", encoded)
	}

	plain := Aggregate(decodeEvents(t, claudePollLine("2026-06-11T10:00:00Z", `{"input_tokens":500}`)), Options{})
	encoded, _ = json.Marshal(plain.Totals)
	totals = map[string]interface{}{}
	_ = json.Unmarshal(encoded, &totals)
	if _, ok := totals["cache_creation_1h_input_tokens"]; ok {
		t.Fatalf("an unreported split must stay absent: %s", encoded)
	}
}

// When a log channel and a metric channel both report cache writes, the metric's writes are
// removed field by field. The one-hour subset is part of those writes and goes with them, or the
// report would be left with one-hour writes larger than its writes.
func TestChannelDedupDropsTheOneHourSubsetWithItsWrites(t *testing.T) {
	report := Aggregate(decodeEvents(t,
		`{"timestamp":"2026-06-11T10:00:00Z","event":{"kind":"agent_runtime","action":"tool.invoked","category":"metric"},`+
			`"message":"claude_code.api_request","harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"s1"},`+
			`"model":"claude-opus-4-6","gen_ai":{"usage":{"input_tokens":10,"cache_creation":{"input_tokens":20000}}}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},`+
			`"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"s1"},"model":"claude-opus-4-6",`+
			`"raw":{"metric_name":"claude_code.token.usage","metric_temporality":"delta"},`+
			`"gen_ai":{"usage":{"cache_creation":{"input_tokens":20000,"ephemeral_1h_input_tokens":20000}}}}`,
	), Options{})
	if got := report.Totals; got.CacheCreationInputTokens != 20000 || got.CacheCreation1hInputTokens != 0 {
		t.Fatalf("totals = %+v, want the metric's writes and their one-hour subset removed", got)
	}
}

// A cumulative series of cache writes is differenced, and its one-hour subset is differenced with
// it, including across a counter reset.
func TestCumulativeCacheWritesDifferenceTheOneHourSubset(t *testing.T) {
	line := func(ts string, total, oneHour int) string {
		return `{"timestamp":"` + ts + `","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},` +
			`"harness":{"name":"example_runtime","collection_method":"otlp"},"session":{"id":"s1"},"model":"claude-opus-4-6",` +
			`"raw":{"metric_name":"example.token.usage","metric_temporality":"cumulative"},` +
			`"gen_ai":{"usage":{"cache_creation":{"input_tokens":` + itoa(total) + `,"ephemeral_1h_input_tokens":` + itoa(oneHour) + `}}}}`
	}
	report := Aggregate(decodeEvents(t,
		line("2026-06-11T10:00:00Z", 1000, 800),
		line("2026-06-11T10:01:00Z", 3000, 1800),
		// Counter reset: the raw values are the new interval's totals.
		line("2026-06-11T10:02:00Z", 500, 500),
	), Options{})
	// Deltas: (1000, 800), (2000, 1000), (500, 500).
	if got := report.Totals; got.CacheCreationInputTokens != 3500 || got.CacheCreation1hInputTokens != 2300 {
		t.Fatalf("totals = %+v, want 3500 writes of which 2300 one-hour", got)
	}
}

// A model with no published one-hour write rate prices its one-hour writes at the five-minute
// rate and names that fallback. anthropic.claude-3-5-haiku-20241022-v1:0 (Bedrock): $0.80 in,
// $4 out, $1 cache write, no one-hour rate.
func TestOneHourWritesWithoutAPublishedRateNameTheFallback(t *testing.T) {
	line := `{"timestamp":"2026-06-11T10:00:00Z","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},` +
		`"harness":{"name":"claude_code","collection_method":"poll"},"session":{"id":"s1"},"model":"anthropic.claude-3-5-haiku-20241022-v1:0",` +
		`"gen_ai":{"usage":{"input_tokens":1000,"cache_creation":{"input_tokens":2000,"ephemeral_1h_input_tokens":2000}}}}`
	report := Aggregate(decodeEvents(t, line), Options{})
	// 1000 x 0.80 + 2000 x 1.00 = 2800 microdollars.
	if got := report.Totals.estimated; got != 2800 {
		t.Fatalf("estimated = %d, want 2800", got)
	}
	if len(report.Pricing.Models) != 1 || len(report.Pricing.Models[0].Fallbacks) != 1 ||
		report.Pricing.Models[0].Fallbacks[0] != "cache_write_1h_at_write_rate" {
		t.Fatalf("pricing models = %+v, want the one-hour fallback named", report.Pricing.Models)
	}
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}
