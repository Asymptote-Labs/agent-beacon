package beaconevent

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/pmetric"
)

// isTokenMetricName reports whether name is a known token-usage metric.
func isTokenMetricName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "token")
}

// ExtractTokensFromMetric reads token counts from a metric's data points.
// It handles Sum and Gauge types and routes values by the gen_ai.token.type
// (standard GenAI SemConv) or token_type (Codex) attribute.
// For simple total metrics with no type attribute (e.g. openclaw.tokens),
// the value is accumulated in Input as a conservative total.
// Returns nil if the metric name is not token-related or all values are zero.
func ExtractTokensFromMetric(metric pmetric.Metric) *TokenUsage {
	if !isTokenMetricName(metric.Name()) {
		return nil
	}
	usage := &TokenUsage{}
	switch metric.Type() {
	case pmetric.MetricTypeSum:
		dps := metric.Sum().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			dp := dps.At(i)
			addTokensByType(usage, numberDataPointInt64(dp), AttrsToMap(dp.Attributes()))
		}
	case pmetric.MetricTypeGauge:
		dps := metric.Gauge().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			dp := dps.At(i)
			addTokensByType(usage, numberDataPointInt64(dp), AttrsToMap(dp.Attributes()))
		}
	}
	if usage.IsZero() {
		return nil
	}
	return usage
}

// ExtractTokensFromAttrs reads token counts embedded directly as span or log
// attributes (e.g. Claude Code trace spans that carry per-call usage).
// Returns nil if none of the recognised token attributes are present.
func ExtractTokensFromAttrs(attrs map[string]interface{}) *TokenUsage {
	input, hasInput := Int64Attr(attrs,
		"gen_ai.usage.input_tokens",
		"input_tokens",
	)
	output, hasOutput := Int64Attr(attrs,
		"gen_ai.usage.output_tokens",
		"output_tokens",
	)
	cacheRead, hasCacheRead := Int64Attr(attrs,
		"gen_ai.usage.input_token.cache_read",
		"cache_read_input_tokens",
	)
	cacheWrite, hasCacheWrite := Int64Attr(attrs,
		"gen_ai.usage.input_token.cache_creation",
		"cache_creation_input_tokens",
		"cache_write_input_tokens",
	)
	if !hasInput && !hasOutput && !hasCacheRead && !hasCacheWrite {
		return nil
	}
	return &TokenUsage{
		Input:      input,
		Output:     output,
		CacheRead:  cacheRead,
		CacheWrite: cacheWrite,
	}
}

// addTokensByType accumulates val into the correct TokenUsage field based on
// the gen_ai.token.type (GenAI SemConv) or token_type (Codex) attribute.
func addTokensByType(usage *TokenUsage, val int64, attrs map[string]interface{}) {
	tokenType := strings.ToLower(strings.TrimSpace(
		FirstString(attrs, "gen_ai.token.type", "token_type"),
	))
	switch tokenType {
	case "input":
		usage.Input += val
	case "output":
		usage.Output += val
	case "input_cache_read":
		usage.CacheRead += val
	case "input_cache_write", "input_cache_creation":
		usage.CacheWrite += val
	default:
		// No recognised type attribute — treat the value as an input/total.
		// This covers simple gauge metrics like openclaw.tokens and
		// openclaw.context.tokens that carry a single undifferentiated count.
		usage.Input += val
	}
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
