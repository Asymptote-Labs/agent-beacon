# beaconjsonexporter

Production Collector exporter for Beacon Endpoint Agent.

This exporter is built into the custom `beacon-otelcol` distribution. It is
responsible for converting OTLP logs, traces, metrics, and resource attributes
into Wazuh-compatible Beacon endpoint JSONL events.

Required behavior:

- Bind only through Collector receivers configured on localhost.
- Write one complete JSON object per line.
- Use the canonical `vendor=beacon`, `product=endpoint-agent`,
  `schema_version=1.0` event contract.
- Identify Claude Code and Claude Cowork OTLP resources and map prompts,
  tool/MCP calls, file access, approval decisions, API usage, token counts,
  costs, and errors into Beacon endpoint events.
- For Claude Code logs, decode JSON-string `tool_input` and `tool_parameters`
  attributes into canonical `command.*` and `file.*` fields. Only actual tool
  results may become tool, command, or file actions; API, assistant, plugin,
  hook, skill, and other lifecycle logs use session actions, while MCP
  connection lifecycle uses `mcp.connection`.
- Preserve explicitly supplied Beacon action/category attributes and retain the
  original OTLP attributes under `raw.attributes`.
- Include configured content fields by default, with `metadata` and `redacted`
  modes available for stricter deployments.
- Redact common secrets and cap event size before writing.
- Emit health failure events when write failures occur.

Token and cost usage:

- Metrics named `gen_ai.client.token.usage` or ending in `.token.usage` or
  `.cost.usage` (for example `claude_code.token.usage` and
  `claude_code.cost.usage`) expand to one event per datapoint so the value,
  token type, model, and session attributes survive into JSONL.
- Token datapoints normalize into the canonical `gen_ai.usage` struct using
  the `type`/`gen_ai.token.type` attribute (`input`, `output`, `cacheRead`,
  `cacheCreation`, `reasoning`); cost datapoints map to
  `gen_ai.usage.cost_usd`. Unknown token types keep the raw value in
  `raw.metric_value` only.
- Datapoint events use the datapoint timestamp and record
  `raw.metric_temporality`, `raw.metric_monotonic`, `raw.metric_value`, and
  (for histograms) `raw.metric_count` so downstream aggregation can dedupe
  cumulative series.
- All other metrics, and usage metrics without datapoints, keep the single
  `metric.observed` event.
- Codex's current live token source is the completed `session_task.turn` trace,
  not its anonymous histogram. The exporter keeps that one span by default,
  promotes `thread.id`/`turn.id`/model, and maps cache reads and cache writes to
  disjoint canonical usage fields. Legacy Codex metric events remain readable.

Noise controls:

- Generic process/runtime metrics are dropped by default unless
  `include_runtime_metrics: true` is set.
- Codex spans are dropped by default except for a completed turn span carrying
  `codex.turn.token_usage.*`. Set `include_codex_spans: true` only when
  troubleshooting all Codex OTLP internals.
- Codex metrics and transport/startup/debug logs remain suppressed by default so
  one prompt does not flood the endpoint runtime log.
- Copilot CLI metrics are suppressed by default, including
  `github.copilot.*`, legacy `copilot_chat.*`, and `gen_ai.client.*` metrics.
  Activity comes from OTLP spans. Set `include_runtime_metrics: true` for
  troubleshooting.
- OpenClaw metrics are suppressed by default, including `openclaw.*` and
  `gen_ai.client.*` token metrics. Activity comes from OTLP logs and traces.
  Set `include_runtime_metrics: true` for troubleshooting.

The production implementation should live here and be included by
`collector-builder/builder.yaml`.

