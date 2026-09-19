package opencodesession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func mapFromAny(value interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func stringFromMap(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringFromAny(m[key]); value != "" {
			return value
		}
	}
	return ""
}

func stringFromAny(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func intFromAny(value interface{}) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := strconv.ParseInt(v.String(), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func floatFromAny(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := strconv.ParseFloat(v.String(), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	default:
		return 0
	}
}

func boolFromAny(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

func firstMapValue(m map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func hasAnyKey(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func tokenUsageFromAny(value interface{}) TokenUsage {
	m := mapFromAny(value)
	cache := mapFromAny(m["cache"])
	return TokenUsage{
		Input:      intFromAny(m["input"]),
		Output:     intFromAny(m["output"]),
		Reasoning:  intFromAny(m["reasoning"]),
		CacheRead:  intFromAny(cache["read"]),
		CacheWrite: intFromAny(cache["write"]),
	}
}

func hasUsage(usage TokenUsage) bool {
	return usage.Input > 0 || usage.Output > 0 || usage.Reasoning > 0 || usage.CacheRead > 0 || usage.CacheWrite > 0 || usage.CostUSD > 0
}

func opencodeModel(raw map[string]interface{}) string {
	if model := stringFromMap(raw, "model"); model != "" {
		return model
	}
	if provider, model := stringFromMap(raw, "providerID", "provider_id"), stringFromMap(raw, "modelID", "model_id"); provider != "" && model != "" {
		return provider + "/" + model
	}
	modelInfo := mapFromAny(raw["model"])
	if len(modelInfo) == 0 {
		modelInfo = mapFromAny(raw["modelInfo"])
	}
	if len(modelInfo) == 0 {
		modelInfo = mapFromAny(raw["model_info"])
	}
	provider := stringFromMap(modelInfo, "providerID", "provider_id", "provider")
	name := stringFromMap(modelInfo, "modelID", "model_id", "id", "name")
	if provider != "" && name != "" {
		return provider + "/" + name
	}
	return name
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
