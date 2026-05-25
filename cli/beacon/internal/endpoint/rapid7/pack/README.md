# Beacon Endpoint Agent Rapid7 Pack

This pack forwards Beacon endpoint JSONL events into Rapid7 InsightIDR Custom
Logs through a webhook event source. Beacon still writes one local source of
truth: `runtime.jsonl`. Rapid7 webhook URLs stay in your log shipper or
deployment tooling, not in Beacon endpoint configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- A Rapid7 InsightIDR Custom Logs event source configured with the Webhook
  collection method.
- The generated webhook URL stored securely as `RAPID7_WEBHOOK_URL` for smoke
  testing or in your customer-managed forwarder.

Recommended Rapid7 setup:

```text
Data Collection > Setup Event Source > Add Event Source > Raw Data > Custom Logs
Collection Method: Webhook
Name: Asymptote Agent Beacon
JSON Events Key: leave blank for Beacon NDJSON payloads
```

Rapid7 webhook URLs are bearer destinations. Protect them like credentials and
avoid committing them to endpoint configuration or source control.

## Install

Generate this pack:

```bash
beacon endpoint rapid7 install-pack --output ./beacon-rapid7-pack
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

```bash
export RAPID7_WEBHOOK_URL="https://..."
./beacon-rapid7-pack/rapid7-upload-smoke-test.sh
```

You can override the Beacon log path:

```bash
export BEACON_LOG="$HOME/.beacon/endpoint/logs/runtime.jsonl"
```

The script sends `Content-Type: application/x-ndjson` and preserves one Beacon
event per line. Rapid7 InsightIDR Custom Logs treats each NDJSON line as an
individual event.

## Production Forwarding

For production, use the generated Vector config as a customer-managed host-agent
forwarding template. Beacon remains the local JSONL producer; Vector tails
`runtime.jsonl`, checkpoints file offsets in its `data_dir`, batches Beacon
events, and posts NDJSON payloads to the Rapid7 Custom Logs webhook.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory. On a macOS system-mode Beacon
deployment, the generated config tails `/var/log/beacon-agent/runtime.jsonl`:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-rapid7-pack/vector.toml /etc/vector/beacon-rapid7.toml
export RAPID7_WEBHOOK_URL="https://..."
vector validate /etc/vector/beacon-rapid7.toml
vector --config /etc/vector/beacon-rapid7.toml
```

In managed deployments, provide `RAPID7_WEBHOOK_URL` through the Vector service
environment or your MDM/secret tooling. Do not store Rapid7 webhook URLs in
Beacon endpoint configuration.

The Vector template is intentionally simple and expects a Vector version with
the `file` source, `remap` transform, and `http` sink. It parses each Beacon
JSONL line and re-encodes the original Beacon event as NDJSON so Rapid7 receives
one Beacon event per line, without a Vector wrapper.

If you adapt the config or use another forwarder, it should:

- checkpoint file offsets,
- batch newline-delimited JSON records,
- preserve one Beacon event per line,
- retry transient failures without duplicating the whole file,
- keep the Rapid7 webhook URL outside Beacon endpoint configuration.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint rapid7 validate
```

Run the one-shot smoke test or wait for your production forwarder to ship the
new line. In Rapid7 Log Search, start with the Custom Logs event source and
search for:

```text
"Beacon endpoint Rapid7 validation event"
```

You can also confirm normalized Beacon fields are present:

```text
vendor=beacon product=endpoint-agent destination.type=rapid7
```

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, raw tool payloads, and other retained content may be forwarded to
Rapid7. Use Beacon's `metadata` or `redacted` content retention modes for
stricter deployments.
