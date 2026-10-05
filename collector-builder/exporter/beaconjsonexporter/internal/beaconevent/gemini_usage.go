package beaconevent

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// Gemini CLI token usage.
//
// Gemini CLI reports each model response's usageMetadata three ways: the gemini_cli.api_response
// log record (input_token_count, output_token_count, cached_content_token_count,
// thoughts_token_count, tool_token_count, total_token_count), the gemini_cli.token.usage counter
// (one series per type: input, output, thought, cache, tool), and an OTel GenAI restatement --
// the gen_ai.client.inference.operation.details log record and the gen_ai.client.token.usage
// histogram -- that carries input and output only.
//
// Gemini's counts are not disjoint the way gen_ai.usage is. The API defines promptTokenCount
// (input_token_count) as including the cached content, so uncached input is prompt minus
// cachedContentTokenCount. candidatesTokenCount (output_token_count) leaves out
// thoughtsTokenCount, while Beacon treats reasoning as a breakdown of output, so output is
// candidates plus thoughts and reasoning is thoughts. toolUsePromptTokenCount is the results of
// tool executions fed back to the model as input and is not inside promptTokenCount
// (totalTokenCount is the sum of prompt, candidates, tool-use prompt and thoughts), so it is added
// to uncached input. With that reading input + output + cache_read equals Gemini's own
// total_token_count, and nothing is counted twice. Gemini reports no cache writes.

const (
	geminiAPIResponseEvent        = "gemini_cli.api_response"
	geminiOperationDetailsEvent   = "gen_ai.client.inference.operation.details"
	geminiTokenUsageMetric        = "gemini_cli.token.usage"
	geminiTokenTypeAttribute      = "type"
	geminiHarness                 = "gemini_cli"
	geminiTokenTypeInput          = "input"
	geminiTokenTypeOutput         = "output"
	geminiTokenTypeThought        = "thought"
	geminiTokenTypeCache          = "cache"
	geminiTokenTypeToolUsePrompts = "tool"
)

// NormalizeGeminiLogEvent gives the api_response record the response's usage and takes it off
// the operation-details record. Gemini CLI emits both records for every response from one call
// (logApiResponse), and the operation-details one restates only input and output -- with input
// still including the cache -- so reading usage from both would count every response twice.
func NormalizeGeminiLogEvent(event *Event, attrs map[string]interface{}) {
	if event == nil || event.Harness.Name != geminiHarness {
		return
	}
	switch FirstString(attrs, "event.name") {
	case geminiOperationDetailsEvent:
		if event.GenAI != nil {
			event.GenAI.Usage = nil
		}
	case geminiAPIResponseEvent:
		usage := geminiAPIResponseUsage(attrs)
		if usage == nil {
			return
		}
		if event.GenAI == nil {
			event.GenAI = &GenAIInfo{}
		}
		event.GenAI.Usage = usage
	}
}

func geminiAPIResponseUsage(attrs map[string]interface{}) *GenAIUsageInfo {
	prompt, hasPrompt := Int64Attr(attrs, "input_token_count")
	candidates, hasCandidates := Int64Attr(attrs, "output_token_count")
	cached, hasCached := Int64Attr(attrs, "cached_content_token_count")
	thoughts, hasThoughts := Int64Attr(attrs, "thoughts_token_count")
	tool, hasTool := Int64Attr(attrs, "tool_token_count")
	if !hasPrompt && !hasCandidates && !hasCached && !hasThoughts && !hasTool {
		return nil
	}
	nonNegative := func(v int64) int64 {
		if v < 0 {
			return 0
		}
		return v
	}
	prompt, candidates, cached, thoughts, tool = nonNegative(prompt), nonNegative(candidates), nonNegative(cached), nonNegative(thoughts), nonNegative(tool)
	usage := &GenAIUsageInfo{}
	if hasPrompt || hasTool {
		input := nonNegative(prompt-cached) + tool
		usage.InputTokens = &input
	}
	if hasCached {
		usage.CacheRead = &GenAIUsageCacheReadInfo{InputTokens: &cached}
	}
	if hasCandidates || hasThoughts {
		output := candidates + thoughts
		usage.OutputTokens = &output
	}
	if hasThoughts {
		usage.Reasoning = &GenAIUsageReasoningInfo{OutputTokens: &thoughts}
	}
	return usage
}

// geminiTokenSeries holds, for one collection of gemini_cli.token.usage, every series' value by
// its attribute set without the type, so the input and output series can be read alongside the
// cache, tool and thought series of the same model, session and timestamp.
type geminiTokenSeries map[string]map[string]float64

func isGeminiTokenUsageMetric(metric pmetric.Metric) bool {
	return strings.EqualFold(strings.TrimSpace(metric.Name()), geminiTokenUsageMetric) && metric.Type() == pmetric.MetricTypeSum
}

// geminiTokenSeriesByScope indexes a gemini_cli.token.usage Sum by series scope. Returns nil
// for every other metric. Gemini CLI's counter is cumulative by default, and a running total of
// (input - cache + tool) is still a running total of uncached input, so the same per-collection
// reading holds for cumulative and delta temporality alike.
func geminiTokenSeriesByScope(metric pmetric.Metric) geminiTokenSeries {
	if !isGeminiTokenUsageMetric(metric) {
		return nil
	}
	out := geminiTokenSeries{}
	dps := metric.Sum().DataPoints()
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		scope, tokenType := geminiSeriesScope(dp.Attributes(), dp.Timestamp())
		if out[scope] == nil {
			out[scope] = map[string]float64{}
		}
		out[scope][tokenType] += numberDataPointValue(dp)
	}
	return out
}

// geminiSeriesScope returns the series identity without its type, plus the type. Every attribute
// is part of the identity, so two sessions or two models in one export never share siblings.
func geminiSeriesScope(attrs pcommon.Map, ts pcommon.Timestamp) (string, string) {
	values := AttrsToMap(attrs)
	tokenType := strings.ToLower(strings.TrimSpace(FirstString(values, geminiTokenTypeAttribute)))
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != geminiTokenTypeAttribute {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%d", ts.AsTime().UnixNano())
	for _, key := range keys {
		fmt.Fprintf(&b, "\x00%s=%v", key, values[key])
	}
	return b.String(), tokenType
}

// apply replaces the usage usageEventFromDataPoint read from one datapoint's own type and value
// with the Gemini reading of it. The event keeps Gemini's type name in gen_ai.token.type, and
// raw.metric_value keeps the value as reported.
func (series geminiTokenSeries) apply(event *Event, attrs pcommon.Map, ts pcommon.Timestamp, value float64) {
	adjusted, tokenType := series.adjust(attrs, ts, value)
	if event.GenAI == nil {
		event.GenAI = &GenAIInfo{}
	}
	event.GenAI.Usage = nil
	event.GenAI.Token = nil
	ApplyTokenUsage(event, tokenType, int64(math.Round(adjusted)))
	if original := FirstString(AttrsToMap(attrs), geminiTokenTypeAttribute); original != "" {
		event.GenAI.Token = &GenAITokenInfo{Type: original}
	}
}

// adjust maps one gemini_cli.token.usage datapoint onto Beacon's disjoint usage. It returns the
// value to record and the token type ApplyTokenUsage should record it as. The tool series keeps
// its own type, which ApplyTokenUsage leaves unmapped (raw only), because its count is already
// folded into the input series.
func (series geminiTokenSeries) adjust(attrs pcommon.Map, ts pcommon.Timestamp, value float64) (float64, string) {
	scope, tokenType := geminiSeriesScope(attrs, ts)
	siblings := series[scope]
	switch tokenType {
	case geminiTokenTypeInput:
		uncached := value - siblings[geminiTokenTypeCache]
		if uncached < 0 {
			uncached = 0
		}
		return uncached + siblings[geminiTokenTypeToolUsePrompts], "input"
	case geminiTokenTypeOutput:
		return value + siblings[geminiTokenTypeThought], "output"
	case geminiTokenTypeThought:
		return value, "reasoning"
	case geminiTokenTypeCache:
		return value, "cacheRead"
	default:
		return value, tokenType
	}
}
