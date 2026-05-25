# Beacon Endpoint Agent Sumo Logic Pack

This pack forwards Beacon endpoint JSONL events into a Sumo Logic Hosted
Collector HTTP Logs & Metrics Source. Beacon still writes one local source of
truth: `runtime.jsonl`. Sumo source URLs and tokens stay in your log shipper or
deployment tooling, not in Beacon endpoint configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- A Sumo Logic Hosted Collector with an HTTP Logs & Metrics Source.
- The Source URL copied as either a presigned URL or an Auth Header URL with a
  separate `x-sumo-token` value.

Recommended Sumo source metadata:

```text
_sourceCategory=security/agentbeacon
source name=agentbeacon
fields=product=agentbeacon,telemetry=ai_agent,env=prod
```

If you use Sumo fields, confirm the fields exist and are enabled in the Sumo
Fields table schema so they are not dropped at ingest.

## Install

Generate this pack:

```bash
beacon endpoint sumo install-pack --output ./beacon-sumo-pack
```

The generated smoke-test script points at the Beacon log path selected by the
CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

For MDM or managed endpoint deployment, prefer Beacon system mode so your
customer-managed log shipper can tail `/var/log/beacon-agent/runtime.jsonl`
without per-user home directory ACLs.

The generated `vector.toml` uses the same selected Beacon log path.

## One-Shot Smoke Test

Use the generated script to upload the current file once. This is only for
validation; do not use it as production forwarding because it re-uploads the
whole file every time.

With a presigned Sumo Source URL:

```bash
export SUMO_URL="https://collectors.sumologic.com/receiver/v1/http/..."
./beacon-sumo-pack/sumo-upload-smoke-test.sh
```

With Sumo's Auth Header option:

```bash
export SUMO_URL="https://collectors.sumologic.com/receiver/v1/http"
export SUMO_TOKEN="..."
./beacon-sumo-pack/sumo-upload-smoke-test.sh
```

You can override the defaults:

```bash
export BEACON_LOG="$HOME/.beacon/endpoint/logs/runtime.jsonl"
export SUMO_SOURCE_CATEGORY="security/agentbeacon"
export SUMO_FIELDS="product=agentbeacon,telemetry=ai_agent,env=prod"
```

The script uses `curl -T` so JSONL line breaks are preserved for Sumo message
boundary detection.

## Production Forwarding

For production, use the generated Vector config as a customer-managed host-agent
forwarding template. Beacon remains the local JSONL producer; Vector tails
`runtime.jsonl`, checkpoints file offsets in its `data_dir`, batches Beacon
events, and posts JSONL payloads to the Sumo HTTP Logs & Metrics Source.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory. On a macOS system-mode Beacon
deployment, the generated config tails `/var/log/beacon-agent/runtime.jsonl`:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-sumo-pack/vector.toml /etc/vector/beacon-sumo.toml
export SUMO_URL="https://collectors.sumologic.com/receiver/v1/http/..."
export SUMO_TOKEN="..."
vector validate /etc/vector/beacon-sumo.toml
vector --config /etc/vector/beacon-sumo.toml
```

`SUMO_TOKEN` is optional when `SUMO_URL` is a presigned Source URL. In managed
deployments, provide `SUMO_URL`, optional `SUMO_TOKEN`, `SUMO_SOURCE_CATEGORY`,
and `SUMO_FIELDS` through the Vector service environment or your MDM/secret
tooling. Do not store Sumo destination secrets in Beacon endpoint configuration.

The Vector template is intentionally simple and expects a Vector version with
the `file` source, `remap` transform, and `http` sink. It parses each Beacon
JSONL line and re-encodes the original Beacon event as NDJSON so Sumo receives
one Beacon event per line, without a Vector wrapper.

If you adapt the config or use another forwarder, it should:

- checkpoint file offsets,
- batch newline-delimited JSON records,
- keep uncompressed POST payloads near Sumo's 100 KB to 1 MB guidance,
- gzip payloads with `Content-Encoding: gzip`,
- retry transient failures without duplicating the whole file.

Keep each Beacon event as one JSON object per line. In the Sumo HTTP Source
advanced log options, avoid `One Message Per Request` when sending batched JSONL
payloads. Use automatic message boundary detection or a boundary configuration
that treats each JSON line as a distinct log record.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint sumo validate
```

Run the one-shot smoke test or wait for your production forwarder to ship the
new line. In Sumo Logic, validate with Live Tail first because normal Search can
lag while data is indexed.

Suggested Live Tail or Search filters:

```text
_sourceCategory=security/agentbeacon product=agentbeacon telemetry=ai_agent
```

```text
_sourceCategory=security/agentbeacon "Beacon endpoint Sumo validation event"
```

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, raw tool payloads, and other retained content may be forwarded to Sumo.
Use Beacon's `metadata` or `redacted` content retention modes for stricter
deployments.
