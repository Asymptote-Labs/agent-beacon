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
- Identify Claude Cowork OTLP resources and map prompts, tool/MCP calls, file
  access, approval decisions, API usage, token counts, costs, and errors into
  Beacon endpoint events.
- Include configured content fields by default, with `metadata` and `redacted`
  modes available for stricter deployments.
- Redact common secrets and cap event size before writing.
- Emit health failure events when write failures occur.

The production implementation should live here and be included by
`collector-builder/builder.yaml`.

