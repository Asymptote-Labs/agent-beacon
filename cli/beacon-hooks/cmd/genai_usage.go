package cmd

import (
	"encoding/json"
	"strconv"
)

// usageContainerKeys are payload keys that may hold a nested token-usage
// object. Runtimes have not converged on one shape, so the extractor accepts
// the common container spellings before falling back to bare top-level fields.
var usageContainerKeys = []string{"tokens", "token_usage", "tokenUsage", "usage", "token_count", "tokenCount"}

// usageAliases maps each canonical gen_ai.usage field to the payload keys it
// may arrive under. Only runtime-reported values pass through; totals are
// derived data and are deliberately not read.
var (
	usageInputAliases         = []string{"input_tokens", "inputTokens", "prompt_tokens", "promptTokens"}
	usageOutputAliases        = []string{"output_tokens", "outputTokens", "completion_tokens", "completionTokens"}
	usageCacheReadAliases     = []string{"cache_read_input_tokens", "cacheReadInputTokens", "cache_read_tokens", "cacheReadTokens", "cached_input_tokens", "cachedInputTokens"}
	usageCacheCreationAliases = []string{"cache_creation_input_tokens", "cacheCreationInputTokens", "cache_write_tokens", "cacheWriteTokens"}
	usageReasoningAliases     = []string{"reasoning_output_tokens", "reasoningOutputTokens", "reasoning_tokens", "reasoningTokens"}
	usageCostAliases          = []string{"cost_usd", "costUsd", "cost"}
)

// extractGenAIUsage returns a canonical gen_ai.usage object (OTel GenAI
// semconv JSON names) from a hook payload, or nil when the payload carries no
// usage. No hook runtime reports usage in every payload, so absence is the
// normal case; token counts are never estimated and cost is never computed
// locally.
func extractGenAIUsage(input map[string]interface{}) map[string]interface{} {
	containers := usageContainers(input)
	if len(containers) == 0 {
		return nil
	}
	usage := map[string]interface{}{}
	if v, ok := firstUsageInt64(containers, usageInputAliases...); ok {
		usage["input_tokens"] = v
	}
	if v, ok := firstUsageInt64(containers, usageOutputAliases...); ok {
		usage["output_tokens"] = v
	}
	if v, ok := firstUsageInt64(containers, usageCacheReadAliases...); ok {
		usage["cache_read"] = map[string]interface{}{"input_tokens": v}
	}
	if v, ok := firstUsageInt64(containers, usageCacheCreationAliases...); ok {
		usage["cache_creation"] = map[string]interface{}{"input_tokens": v}
	}
	if v, ok := firstUsageInt64(containers, usageReasoningAliases...); ok {
		usage["reasoning"] = map[string]interface{}{"output_tokens": v}
	}
	if v, ok := firstUsageFloat(containers, usageCostAliases...); ok {
		usage["cost_usd"] = v
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}

// applyGenAIUsageFields merges extracted usage into fields["gen_ai"].usage so
// it composes with the gen_ai.tool block some emitters already set.
func applyGenAIUsageFields(fields, input map[string]interface{}) {
	usage := extractGenAIUsage(input)
	if usage == nil {
		return
	}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
}

// usageContainers returns the candidate objects usage fields may live in, in
// priority order: nested containers first, then the payload itself for bare
// top-level fields.
func usageContainers(input map[string]interface{}) []map[string]interface{} {
	var containers []map[string]interface{}
	for _, key := range usageContainerKeys {
		if nested, ok := input[key].(map[string]interface{}); ok && len(nested) > 0 {
			containers = append(containers, nested)
		}
	}
	if input != nil {
		containers = append(containers, input)
	}
	return containers
}

// firstUsageInt64 resolves an alias chain across candidate containers,
// returning the first non-negative integer value found.
func firstUsageInt64(containers []map[string]interface{}, keys ...string) (int64, bool) {
	for _, container := range containers {
		for _, key := range keys {
			value, ok := container[key]
			if !ok {
				continue
			}
			if n, ok := usageInt64(value); ok && n >= 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// firstUsageFloat resolves an alias chain across candidate containers,
// returning the first non-negative numeric value found. The payload itself
// (last container) only resolves explicit cost_usd/costUsd keys; a bare
// top-level "cost" is too likely to mean something other than model spend.
func firstUsageFloat(containers []map[string]interface{}, keys ...string) (float64, bool) {
	for i, container := range containers {
		topLevel := i == len(containers)-1
		for _, key := range keys {
			if topLevel && key == "cost" {
				continue
			}
			value, ok := container[key]
			if !ok {
				continue
			}
			if f, ok := usageFloat(value); ok && f >= 0 {
				return f, true
			}
		}
	}
	return 0, false
}

func usageInt64(value interface{}) (int64, bool) {
	if f, ok := usageFloat(value); ok {
		return int64(f), true
	}
	return 0, false
}

func usageFloat(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
