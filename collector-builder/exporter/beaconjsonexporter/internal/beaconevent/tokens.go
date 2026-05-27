package beaconevent

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// tokenMetricNames is the exact allowlist of metric names that carry LLM token
// usage counts. Only metrics on this list enter ExtractTokensFromMetric.
// Add a new entry here when a harness starts emitting a new token-usage metric.
var tokenMetricNames = map[string]bool{
	"gen_ai.client.token.usage": true,
	"claude_code.token.usage":   true,
	"openclaw.tokens":           true,
	"openclaw.context.tokens":   true,
}

// isTokenMetricName reports whether name is a known token-usage metric.
func isTokenMetricName(name string) bool {
	return tokenMetricNames[strings.ToLower(strings.TrimSpace(name))]
}

// tokenAttrDef maps a set of OTel attribute keys to the TokenUsage field they
// populate. The first key that resolves to a non-zero int64 wins.
// To add a new token type (e.g. Reasoning), add one entry here and the
// corresponding field to TokenUsage — nothing else needs changing.
type tokenAttrDef struct {
	keys []string
	set  func(*TokenUsage, int64)
}

// tokenAttrDefs drives ExtractTokensFromAttrs. Attribute keys follow the GenAI
// semantic conventions; aliases for older or provider-specific names come after
// the canonical key. Adding a new token type means one new entry here.
var tokenAttrDefs = []tokenAttrDef{
	{
		keys: []string{"gen_ai.usage.input_tokens"},
		set:  func(u *TokenUsage, v int64) { u.Input = v },
	},
	{
		keys: []string{"gen_ai.usage.output_tokens"},
		set:  func(u *TokenUsage, v int64) { u.Output = v },
	},
	{
		keys: []string{"gen_ai.usage.input_token.cache_read"},
		set:  func(u *TokenUsage, v int64) { u.CacheRead = v },
	},
	{
		keys: []string{"gen_ai.usage.input_token.cache_creation"},
		set:  func(u *TokenUsage, v int64) { u.CacheWrite = v },
	},
}

// tokenTypeToField maps gen_ai.token.type / token_type attribute values to the
// accumulator for the corresponding TokenUsage field. Adding a new token type
// means adding one entry here (and the field to TokenUsage).
var tokenTypeToField = map[string]func(*TokenUsage, int64){
	"input":                func(u *TokenUsage, v int64) { u.Input += v },
	"output":               func(u *TokenUsage, v int64) { u.Output += v },
	"input_cache_read":     func(u *TokenUsage, v int64) { u.CacheRead += v },
	"input_cache_write":    func(u *TokenUsage, v int64) { u.CacheWrite += v },
	"input_cache_creation": func(u *TokenUsage, v int64) { u.CacheWrite += v },
}

// ExtractTokensFromMetric reads token counts from a metric's data points.
// Returns nil if the metric is not on the token-metric allowlist or all values
// are zero.
func ExtractTokensFromMetric(metric pmetric.Metric) *TokenUsage {
	if !isTokenMetricName(metric.Name()) {
		return nil
	}
	usage := &TokenUsage{}
	switch metric.Type() {
	case pmetric.MetricTypeSum:
		addTokensFromDataPoints(usage, metric.Sum().DataPoints())
	case pmetric.MetricTypeGauge:
		addTokensFromDataPoints(usage, metric.Gauge().DataPoints())
	}
	if usage.IsZero() {
		return nil
	}
	return usage
}

// ExtractTokensFromAttrs reads token counts from span or log attributes
// (e.g. Claude Code trace spans that carry per-call usage via gen_ai.usage.*).
// Returns nil if none of the recognised token attributes are present.
func ExtractTokensFromAttrs(attrs map[string]interface{}) *TokenUsage {
	usage := &TokenUsage{}
	found := false
	for _, def := range tokenAttrDefs {
		if v, ok := Int64Attr(attrs, def.keys...); ok {
			def.set(usage, v)
			found = true
		}
	}
	if !found {
		return nil
	}
	return usage
}

// addTokensFromDataPoints iterates a data-point slice and accumulates token
// counts into usage, eliminating the duplicated loop body for Sum and Gauge.
func addTokensFromDataPoints(usage *TokenUsage, dps pmetric.NumberDataPointSlice) {
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		addTokensByType(usage, numberDataPointInt64(dp), dp.Attributes())
	}
}

// addTokensByType accumulates val into the correct TokenUsage field based on
// the gen_ai.token.type (GenAI SemConv) or token_type (Codex) data-point
// attribute.
//
// If no type attribute is present (empty string), the value is treated as an
// undifferentiated total and accumulated into Input. This covers simple gauge
// metrics like openclaw.tokens that carry a single count with no breakdown.
//
// If an unrecognised non-empty type is present the value is dropped. This
// prevents future token types (e.g. "reasoning") from silently inflating Input
// with values that are not input tokens.
func addTokensByType(usage *TokenUsage, val int64, dpAttrs pcommon.Map) {
	tokenType := tokenTypeFromAttrs(dpAttrs)
	if tokenType == "" {
		// No type attribute — undifferentiated total (e.g. openclaw.tokens).
		usage.Input += val
		return
	}
	if setter, ok := tokenTypeToField[tokenType]; ok {
		setter(usage, val)
	}
	// Unknown non-empty type: drop. A future "reasoning" token type should not
	// corrupt Input counts while waiting for an explicit entry in tokenTypeToField.
}

// tokenTypeFromAttrs extracts the normalised token type string from a data
// point's attribute map, checking gen_ai.token.type then token_type.
func tokenTypeFromAttrs(dpAttrs pcommon.Map) string {
	if v, ok := dpAttrs.Get("gen_ai.token.type"); ok {
		if s := strings.ToLower(strings.TrimSpace(v.AsString())); s != "" {
			return s
		}
	}
	if v, ok := dpAttrs.Get("token_type"); ok {
		return strings.ToLower(strings.TrimSpace(v.AsString()))
	}
	return ""
}

// numberDataPointInt64 returns the integer value of a number data point,
// converting float64 to int64 when necessary.
func numberDataPointInt64(dp pmetric.NumberDataPoint) int64 {
	switch dp.ValueType() {
	case pmetric.NumberDataPointValueTypeInt:
		return dp.IntValue()
	case pmetric.NumberDataPointValueTypeDouble:
		return int64(dp.DoubleValue())
	}
	return 0
}
