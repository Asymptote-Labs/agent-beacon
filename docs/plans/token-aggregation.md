# Token Aggregation Feature Plan

**Branch**: `claude/token-aggregation-planning-4qdQQ`  
**Date**: 2026-05-27

## Overview

Add structured token usage tracking to Beacon so operators can slice and dice
token consumption by user, session, model, harness, repository, and time. The
feature has four layers: schema, OTEL converter, dashboard/API, and CLI.

---

## Current State

Token data arrives from AI runtimes as OpenTelemetry metrics (and sometimes
span attributes), stored wholesale in `event.Raw["attributes"]`. Nothing
extracts or aggregates them. To find token usage today you must:

1. Filter for `category == "metric"` events
2. Grep `raw.metric_name` for `token` keywords
3. Manually parse numeric values out of `raw.attributes`

**Known token metric names** (already tracked in collector drop-lists):

| Metric | Harness | Notes |
|---|---|---|
| `gen_ai.client.token.usage` | all | Standard GenAI SemConv; `gen_ai.token.type` attr = `input`/`output` |
| `claude_code.token.usage` | `claude_code` | may also emit cache attrs |
| `codex.run.token_usage` / `codex.turn.token_usage` | `codex_cli` | `token_type` attr |
| `openclaw.tokens` / `openclaw.context.tokens` | `openclaw_gateway` | direct value |

**Span attributes** emitted by Claude Code for per-LLM-call token counts:

```
gen_ai.usage.input_tokens
gen_ai.usage.output_tokens
gen_ai.usage.input_token.cache_read        (Anthropic prompt-cache)
gen_ai.usage.input_token.cache_creation    (Anthropic prompt-cache)
```

---

## Implementation Plan

### Step 1 — Schema: add `TokenUsage` to `Event`

**File**: `cli/beacon/internal/endpoint/schema/event.go`

Add a new struct and a pointer field on `Event`. Keeping it as a pointer
preserves backward compatibility (events without token data omit the field):

```go
// TokenUsage holds token consumption counts for a single event.
// Fields that are zero are omitted from JSON to keep non-token events lean.
type TokenUsage struct {
    Input      int64 `json:"input,omitempty"`
    Output     int64 `json:"output,omitempty"`
    CacheRead  int64 `json:"cache_read,omitempty"`
    CacheWrite int64 `json:"cache_write,omitempty"`
}

// Total returns the sum of all token types.
func (t TokenUsage) Total() int64 {
    return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

// IsZero reports whether no token counts are recorded.
func (t TokenUsage) IsZero() bool {
    return t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheWrite == 0
}
```

Add to `Event`:

```go
Tokens *TokenUsage `json:"tokens,omitempty"`
```

> **Stability note**: `TokenUsage` field names match the GenAI semantic
> convention token types. `CacheRead`/`CacheWrite` are Anthropic-specific but
> intentionally named to be provider-agnostic.

---

### Step 2 — OTEL Converter: extract tokens from metrics and spans

**Package**: `collector-builder/exporter/beaconjsonexporter/internal/beaconevent/`

Add a new file `tokens.go` with two extraction functions.

#### 2a. From metric data points

```go
// ExtractTokensFromMetric reads token counts from the metric's data points.
// It handles Sum and Gauge types and uses gen_ai.token.type / token_type
// attributes to classify input vs output vs cache.
func ExtractTokensFromMetric(metric pmetric.Metric) *schema.TokenUsage {
    usage := &schema.TokenUsage{}
    switch metric.Type() {
    case pmetric.MetricTypeSum:
        for i := 0; i < metric.Sum().DataPoints().Len(); i++ {
            dp := metric.Sum().DataPoints().At(i)
            attrs := AttrsToMap(dp.Attributes())
            addDataPoint(usage, dp.IntValue(), dp.DoubleValue(), dp.ValueType(), attrs, metric.Name())
        }
    case pmetric.MetricTypeGauge:
        for i := 0; i < metric.Gauge().DataPoints().Len(); i++ {
            dp := metric.Gauge().DataPoints().At(i)
            attrs := AttrsToMap(dp.Attributes())
            addDataPoint(usage, dp.IntValue(), dp.DoubleValue(), dp.ValueType(), attrs, metric.Name())
        }
    }
    if usage.IsZero() {
        return nil
    }
    return usage
}
```

`addDataPoint` maps `gen_ai.token.type` attribute values:

| Attribute value | Field |
|---|---|
| `input` | `Input` |
| `output` | `Output` |
| `input_cache_read` | `CacheRead` |
| `input_cache_write` / `input_cache_creation` | `CacheWrite` |
| (missing / unknown) | `Input` (conservative fallback for simple gauges) |

For Codex metrics (`codex.run.token_usage`), map `token_type` attribute the same way.

#### 2b. From span/log attributes

```go
// ExtractTokensFromAttrs reads token counts embedded directly as span or log
// attributes (e.g. Claude Code trace spans).
func ExtractTokensFromAttrs(attrs map[string]interface{}) *schema.TokenUsage {
    input, _      := Int64Attr(attrs, "gen_ai.usage.input_tokens", "input_tokens")
    output, _     := Int64Attr(attrs, "gen_ai.usage.output_tokens", "output_tokens")
    cacheRead, _  := Int64Attr(attrs, "gen_ai.usage.input_token.cache_read",     "cache_read_input_tokens")
    cacheWrite, _ := Int64Attr(attrs, "gen_ai.usage.input_token.cache_creation", "cache_creation_input_tokens")
    usage := &schema.TokenUsage{
        Input: input, Output: output,
        CacheRead: cacheRead, CacheWrite: cacheWrite,
    }
    if usage.IsZero() {
        return nil
    }
    return usage
}
```

#### 2c. Wire into existing event builders

- `EventFromMetric()` → call `ExtractTokensFromMetric(metric)`, assign to `event.Tokens`
- `PopulateCommon()` → call `ExtractTokensFromAttrs(attrs)`, assign to `event.Tokens`  
  (only if not already set, so metric events win over span fallbacks)

---

### Step 3 — Dashboard: `TokenReport` aggregation

**New file**: `cli/beacon/internal/endpoint/dashboard/tokens.go`

```go
// TokenTotals accumulates token counts and event counts.
type TokenTotals struct {
    Input      int64 `json:"input"`
    Output     int64 `json:"output"`
    CacheRead  int64 `json:"cache_read"`
    CacheWrite int64 `json:"cache_write"`
    Total      int64 `json:"total"`
    Events     int   `json:"events"`
}

// TokenDimension is a named bucket with its token totals.
type TokenDimension struct {
    Name   string      `json:"name"`
    Tokens TokenTotals `json:"tokens"`
}

// TokenReport is the top-level token aggregation result.
type TokenReport struct {
    Totals       TokenTotals      `json:"totals"`
    ByUser       []TokenDimension `json:"by_user"`
    BySession    []TokenDimension `json:"by_session"`
    ByModel      []TokenDimension `json:"by_model"`
    ByHarness    []TokenDimension `json:"by_harness"`
    ByRepository []TokenDimension `json:"by_repository"`
    ByDay        []TokenDimension `json:"by_day,omitempty"` // YYYY-MM-DD buckets
}

// BuildTokenReport iterates matched events and builds a TokenReport.
// Only events with a non-nil Tokens field contribute to counts.
func BuildTokenReport(result EventResult) TokenReport
```

Dimension keys:

| Dimension | Key field |
|---|---|
| `by_user` | `event.User.Name` (fallback `"unknown"`) |
| `by_session` | `event.Session.ID` (skip if empty) |
| `by_model` | `event.Model` (fallback `"unknown"`) |
| `by_harness` | `event.Harness.Name` |
| `by_repository` | `event.Repository` (skip if empty) |
| `by_day` | `event.Timestamp[:10]` (YYYY-MM-DD) |

Each dimension slice is sorted descending by `Total` tokens.

**Add `TokenReport` to `Summary`**:

```go
// In summary.go Summary struct:
TokenReport TokenReport `json:"token_report"`
```

`BuildSummary` calls `BuildTokenReport(result)` and assigns it.

---

### Step 4 — Dashboard API: `/api/tokens` endpoint

**File**: `cli/beacon/internal/endpoint/dashboard/server.go`

Add a new route `/api/tokens` that:
- Accepts the same query parameters as `/api/events` (harness, model, session,
  repository, since, until, etc.)
- Runs `ReadEvents` with `NoLimit: true` (reads all matching events)
- Returns `BuildTokenReport(result)` as JSON

The separate endpoint keeps the tokens view fast when the caller only needs
aggregated counts without the full event list.

---

### Step 5 — CLI command: `beacon endpoint tokens`

**Files**: `cli/beacon/cmd/` (new file `tokens.go` or added to `endpoint.go`)

```
Usage:
  beacon endpoint tokens [flags]

Flags:
  --since duration    lookback window (default 7d)
  --harness string    filter by harness name
  --model string      filter by model
  --session string    filter by session ID
  --repository string filter by repository
  --json              emit raw JSON instead of table
```

**Example output** (table mode):

```
Token Usage  •  last 7d  •  3 harnesses  •  2 users

  Total           2,345,678  (input 890,123 · output 456,789 · cache_r 789,012 · cache_w 209,754)

  By Model
    claude-opus-4-7     1,234,567   52.6%
    claude-sonnet-4-6   1,111,111   47.4%

  By Harness
    claude_code         2,100,000   89.5%
    codex_cli             245,678   10.5%

  By User
    alice               1,800,000   76.7%
    bob                   545,678   23.3%

  By Session  (top 10)
    sess-abc123           345,678
    sess-def456           234,567
    …

  By Repository  (top 10)
    github.com/org/repo   800,000
    …

  By Day
    2026-05-27            456,789
    2026-05-26            398,012
    …
```

---

### Step 6 — Tests

| Test file | What it covers |
|---|---|
| `schema/event_test.go` | `TokenUsage.Total()`, `IsZero()`, JSON round-trip with zero-omit |
| `beaconevent/tokens_test.go` | `gen_ai.client.token.usage` Sum metric, input+output data points; cache attrs; span attribute extraction; all-zero returns nil |
| `dashboard/tokens_test.go` | `BuildTokenReport`: by_user, by_session, by_model, by_harness, by_repository, by_day buckets; events without Tokens don't inflate counts |
| existing converter tests | Update `exporter_test.go` to assert `event.Tokens != nil` and correct input/output split for token metric fixtures |

---

### Step 7 — Downstream considerations

- **JSONL format**: `tokens` is a new optional field. Existing consumers
  ignore unknown fields; no breaking change.
- **Wazuh / Elastic content packs**: token fields fall through automatically
  via the JSONL schema. Index-mapping additions are optional follow-up work.
- **Content retention**: `tokens` field is always emitted regardless of
  `content.retention` mode — it contains only counts, never raw text.
- **`beacon endpoint status`**: consider adding a one-liner token total to the
  status output as a convenience.

---

## Implementation Order

```
1. schema/event.go          — TokenUsage struct + Event.Tokens field
2. beaconevent/tokens.go    — ExtractTokensFromMetric + ExtractTokensFromAttrs
3. beaconevent/converter.go — wire tokens into EventFromMetric + PopulateCommon
4. dashboard/tokens.go      — BuildTokenReport
5. dashboard/summary.go     — embed TokenReport in Summary
6. dashboard/server.go      — /api/tokens route
7. cmd/tokens.go            — beacon endpoint tokens CLI command
8. tests (parallel with above)
```

Each step is independently testable; steps 1–3 can be done in one PR and
steps 4–7 in a second if preferred.
