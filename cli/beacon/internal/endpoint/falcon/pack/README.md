# Beacon Endpoint Agent Falcon Pack

This pack forwards Beacon endpoint JSONL events into CrowdStrike Falcon through
an HTTP Event Collector compatible endpoint. Beacon still writes one local source
of truth: `runtime.jsonl`. CrowdStrike endpoint URLs and tokens stay in Vector
service environment, Jamf parameters, or secret tooling, not in Beacon endpoint
configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- A CrowdStrike Falcon HEC or Next-Gen SIEM connector endpoint.
- The connector token stored securely as `BEACON_FALCON_HEC_TOKEN`.
- Vector installed on the endpoint, or Beacon's managed macOS package with
  `/opt/beacon/bin/vector`.

CrowdStrike connector URL formats vary. Use the exact URL shown by the
connector. Common examples are:

```text
https://<tenant>.ingest.<region>.crowdstrike.com/services/collector
https://<tenant>.ingest.<region>.crowdstrike.com/api/v1/ingest/hec
```

## Install

Generate this pack:

```bash
beacon endpoint falcon install-pack --output ./beacon-falcon-pack
```

The generated smoke-test script and `vector.toml` point at the Beacon log path
selected by the CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

For MDM or managed endpoint deployment, prefer Beacon system mode so the
forwarder can tail `/var/log/beacon-agent/runtime.jsonl` without per-user home
directory ACLs. When Claude telemetry is hook-only, make sure the interactive
user's hooks write to the system log path.

## One-Shot Smoke Test

Use the generated script to POST a synthetic Beacon-shaped HEC event. This tests
the CrowdStrike URL and token without relying on Vector or Claude.

```bash
export BEACON_FALCON_HEC_ENDPOINT="https://..."
export BEACON_FALCON_HEC_TOKEN="..."
export BEACON_FALCON_SOURCE="beacon-endpoint-agent"
export BEACON_FALCON_SOURCETYPE="json"
./beacon-falcon-pack/falcon-hec-smoke-test.sh
```

The script does not print the token. A successful connector returns a 2xx HTTP
status, often with a body similar to:

```json
{"text":"Success","code":0}
```

## Production Forwarding

For production, run Vector as a managed host agent. Beacon remains the local
JSONL producer; Vector tails `runtime.jsonl`, checkpoints file offsets in its
`data_dir`, batches Beacon events, wraps them as HEC payloads, and POSTs them to
CrowdStrike.

Vector topologies follow Vector's source/transform/sink model:

- `sources.beacon_runtime`: tails Beacon JSONL.
- `transforms.beacon_json`: parses Beacon JSON and wraps it in HEC shape.
- `sinks.falcon_hec`: sends newline-delimited JSON to CrowdStrike.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-falcon-pack/vector.toml /etc/vector/beacon-falcon.toml
export BEACON_FALCON_HEC_ENDPOINT="https://..."
export BEACON_FALCON_HEC_TOKEN="..."
vector validate /etc/vector/beacon-falcon.toml
vector --config /etc/vector/beacon-falcon.toml
```

In Jamf deployments using the Beacon managed package, run the packaged Falcon
Vector forwarder script instead of starting Vector manually. Provide the
endpoint and token as Jamf parameters or environment variables.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint falcon validate
```

Wait for Vector to ship the new line. In Falcon Event Search, start with:

```text
source = "beacon-endpoint-agent" "Beacon endpoint Falcon validation event"
```

You can also confirm normalized Beacon fields are present:

```text
vendor = "beacon" product = "endpoint-agent"
harness.name = "claude" OR harness.name = "claude_code"
```

For hook-only validation, generate a unique Claude prompt after the forwarder is
running and search for that exact marker:

```text
source = "beacon-endpoint-agent" "hook-only unique marker"
```

## Troubleshooting

- If local `runtime.jsonl` contains events but Falcon does not, check the Vector
  service stderr log for HTTP status, TLS, or auth failures.
- If `curl` to `/api/v1/ingest/hec` returns an empty reply but
  `/services/collector` returns `403` without auth, the connector likely expects
  `/services/collector`.
- Successful Vector sends are usually quiet; absence of stderr errors after new
  events can mean delivery is working.
- Use `read_from = "beginning"` only for controlled diagnostics. Production
  defaults to `end` to avoid re-uploading historical logs on first start.

## Content Handling

Beacon forwards retained prompt text, command output, raw tool inputs, raw OTLP
attributes, and related local telemetry to CrowdStrike subject to Beacon's local
redaction, sanitization, truncation, and event-size limits.
