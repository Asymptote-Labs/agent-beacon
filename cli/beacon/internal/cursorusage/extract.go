package cursorusage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrNoBubbleStore indicates the snapshot has no cursorDiskKV table, meaning
// either the path is not a Cursor state database or Cursor's storage layout
// changed out from under this reader.
var ErrNoBubbleStore = errors.New("cursor state database has no cursorDiskKV table")

// Generation is one runtime-recorded model generation extracted from the
// store. Token counts are pointers so absent stays distinct from zero; only
// runtime-recorded values are carried, never estimates.
type Generation struct {
	ComposerID          string
	BubbleID            string
	Model               string
	Timestamp           time.Time
	TimestampSource     string // "bubble" | "composer" | "sync"
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64
	ReasoningTokens     *int64
	CostUSD             *float64
}

// ExtractStats reports what a sweep saw, so an empty result is explainable
// (schema drift vs. genuinely no recorded usage).
type ExtractStats struct {
	Bubbles     int // bubble rows examined
	SkippedZero int // bubbles with no recorded token counts (some Cursor builds record zeros)
	ParseErrors int // rows that were not parseable JSON in a known shape
}

// tokenAliases mirrors the hooks-side extractor philosophy: tolerate the
// container and key spellings observed across Cursor versions, and read only
// runtime-reported fields (derived totals are ignored).
var (
	genContainerKeys      = []string{"tokenCount", "token_count", "tokenUsage", "token_usage", "usage", "tokens"}
	genInputAliases       = []string{"inputTokens", "input_tokens", "promptTokens", "prompt_tokens"}
	genOutputAliases      = []string{"outputTokens", "output_tokens", "completionTokens", "completion_tokens"}
	genCacheReadAliases   = []string{"cacheReadTokens", "cache_read_tokens", "cacheReadInputTokens", "cache_read_input_tokens", "cachedTokens", "cached_tokens", "cachedInputTokens", "cached_input_tokens"}
	genCacheCreateAliases = []string{"cacheWriteTokens", "cache_write_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens"}
	genReasoningAliases   = []string{"reasoningTokens", "reasoning_tokens", "reasoningOutputTokens", "reasoning_output_tokens"}
	genCostAliases        = []string{"costUsd", "cost_usd"}
	bubbleKeyPrefix       = "bubbleId:"
	composerDataKeyPrefix = "composerData:"
)

// ExtractGenerations reads every bubble row from the snapshot and returns the
// generations that carry recorded token usage, ordered by key. Unparseable
// rows are skipped and counted — schema drift must degrade to fewer rows,
// never a failed sweep.
func ExtractGenerations(db *sql.DB) ([]Generation, ExtractStats, error) {
	var stats ExtractStats
	if ok, err := hasBubbleStore(db); err != nil {
		return nil, stats, err
	} else if !ok {
		return nil, stats, ErrNoBubbleStore
	}
	composerCreated := composerTimestamps(db)

	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE ? ORDER BY key`, bubbleKeyPrefix+"%")
	if err != nil {
		return nil, stats, err
	}
	defer rows.Close()

	var out []Generation
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			stats.ParseErrors++
			continue
		}
		stats.Bubbles++
		composerID, bubbleID, ok := splitBubbleKey(key)
		if !ok {
			stats.ParseErrors++
			continue
		}
		var bubble map[string]interface{}
		if err := json.Unmarshal(value, &bubble); err != nil {
			stats.ParseErrors++
			continue
		}
		gen, ok := generationFromBubble(composerID, bubbleID, bubble, composerCreated)
		if !ok {
			stats.SkippedZero++
			continue
		}
		out = append(out, gen)
	}
	return out, stats, rows.Err()
}

func hasBubbleStore(db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'cursorDiskKV'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// composerTimestamps bulk-loads composerData createdAt values so bubbles
// without their own timestamp can fall back to their conversation's start
// time. Any read or parse failure just yields an empty map.
func composerTimestamps(db *sql.DB) map[string]time.Time {
	out := map[string]time.Time{}
	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE ?`, composerDataKeyPrefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		composerID := strings.TrimPrefix(key, composerDataKeyPrefix)
		var data map[string]interface{}
		if err := json.Unmarshal(value, &data); err != nil {
			continue
		}
		if ts, ok := timestampValue(data["createdAt"]); ok {
			out[composerID] = ts
		}
	}
	return out
}

func splitBubbleKey(key string) (composerID, bubbleID string, ok bool) {
	rest := strings.TrimPrefix(key, bubbleKeyPrefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// generationFromBubble maps one decoded bubble to a Generation. It returns
// ok=false when the bubble records no positive token count — zero-count rows
// are indistinguishable from Cursor builds that stopped recording usage, and
// counting them would only pollute rollups.
func generationFromBubble(composerID, bubbleID string, bubble map[string]interface{}, composerCreated map[string]time.Time) (Generation, bool) {
	containers := tokenContainers(bubble)
	gen := Generation{ComposerID: composerID, BubbleID: bubbleID}
	total := int64(0)
	assign := func(dst **int64, aliases []string) {
		if v, ok := firstInt64(containers, aliases...); ok {
			*dst = &v
			total += v
		}
	}
	assign(&gen.InputTokens, genInputAliases)
	assign(&gen.OutputTokens, genOutputAliases)
	assign(&gen.CacheReadTokens, genCacheReadAliases)
	assign(&gen.CacheCreationTokens, genCacheCreateAliases)
	assign(&gen.ReasoningTokens, genReasoningAliases)
	if v, ok := firstFloat(containers, genCostAliases...); ok && v > 0 {
		gen.CostUSD = &v
	}
	if total <= 0 && gen.CostUSD == nil {
		return Generation{}, false
	}

	gen.Model = bubbleModel(bubble)
	if ts, ok := timestampValue(bubble["createdAt"]); ok {
		gen.Timestamp = ts
		gen.TimestampSource = "bubble"
	} else if ts, ok := composerCreated[composerID]; ok {
		gen.Timestamp = ts
		gen.TimestampSource = "composer"
	} else {
		gen.TimestampSource = "sync"
	}
	return gen, true
}

func tokenContainers(bubble map[string]interface{}) []map[string]interface{} {
	var containers []map[string]interface{}
	for _, key := range genContainerKeys {
		if nested, ok := bubble[key].(map[string]interface{}); ok && len(nested) > 0 {
			containers = append(containers, nested)
		}
	}
	return containers
}

func bubbleModel(bubble map[string]interface{}) string {
	if info, ok := bubble["modelInfo"].(map[string]interface{}); ok {
		if model, ok := info["modelName"].(string); ok && model != "" {
			return model
		}
	}
	for _, key := range []string{"modelName", "model_name", "model"} {
		if model, ok := bubble[key].(string); ok && model != "" {
			return model
		}
	}
	return ""
}

// timestampValue tolerates the timestamp encodings seen in the store:
// epoch milliseconds (JSON number or numeric string) and RFC3339 strings.
func timestampValue(value interface{}) (time.Time, bool) {
	switch v := value.(type) {
	case float64:
		return epochMillis(int64(v))
	case string:
		if v == "" {
			return time.Time{}, false
		}
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			return epochMillis(ms)
		}
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

func epochMillis(ms int64) (time.Time, bool) {
	if ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).UTC(), true
}

func firstInt64(containers []map[string]interface{}, keys ...string) (int64, bool) {
	for _, container := range containers {
		for _, key := range keys {
			value, ok := container[key]
			if !ok {
				continue
			}
			if f, ok := numericValue(value); ok && f >= 0 {
				return int64(f), true
			}
		}
	}
	return 0, false
}

func firstFloat(containers []map[string]interface{}, keys ...string) (float64, bool) {
	for _, container := range containers {
		for _, key := range keys {
			value, ok := container[key]
			if !ok {
				continue
			}
			if f, ok := numericValue(value); ok && f >= 0 {
				return f, true
			}
		}
	}
	return 0, false
}

func numericValue(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
