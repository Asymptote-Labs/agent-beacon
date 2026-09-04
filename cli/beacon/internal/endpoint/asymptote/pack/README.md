# Beacon Endpoint Agent Asymptote Managed Forwarding Pack

This pack forwards Beacon endpoint JSONL events to Asymptote's managed ingest
service so they appear on the Asymptote dashboard. Beacon writes runtime
activity to `runtime.jsonl` and configuration inventory to
`inventory_state.jsonl`; Vector tails both files and POSTs gzip-compressed
NDJSON batches over HTTPS with a per-device key.

Forwarding is opt-in and revocable. Beacon's default posture is local-only;
nothing in this pack runs until a device has been approved by a member of your
Asymptote organization and Vector has been started with the resulting key.

## What leaves the machine

Every line of `runtime.jsonl` written after the forwarder starts, and every
line of `inventory_state.jsonl`, exactly as Beacon wrote them locally. Local
redaction, retention and size limits apply before a line is written, so they
apply to what is forwarded. The ingest service adds a `tenant` block
(organization, device, approving user, receive time) server-side and ignores
any tenant fields sent by the client. Nothing else is sent: no prompts or files
beyond what the events already contain, no environment variables, no shell
history.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- Vector 0.56 or newer (`/opt/beacon/bin/vector` from the signed macOS
  package, Homebrew's `vector`, or the Linux `vector` packages).
- A device key and ingest URL from enrollment. `beacon endpoint connect`
  performs enrollment and writes this configuration for you; use this pack when
  you want to run or inspect the forwarder by hand.

## Files

- `vector.toml`: the forwarder. Reads the device key from a JSON secrets file
  through Vector's `file` secret backend, so the key is in neither the config
  nor the environment. Its startup healthcheck calls the authenticated
  `/v1/ingest/health`, so a revoked key is reported when Vector starts.
- `asymptote-ingest-smoke-test.sh`: one-shot credential check plus a manual
  POST of the log tail, to prove the key works before starting Vector.
- `sample-event.jsonl`: example Beacon events in the shape the service accepts.

## Install

```bash
beacon endpoint asymptote install-pack --output ./beacon-asymptote-pack
```

The generated files point at the Beacon log path selected by the CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

The inventory log is the sibling `inventory_state.jsonl` in the same directory.

## Run Vector by hand

1. Put the device key in a file only the forwarder's user can read:

   ```bash
   umask 077
   printf '{"device_key": "%s"}\n' "$DEVICE_KEY" > ~/.beacon/endpoint/asymptote/vector-secrets.json
   ```

2. Export the values the template reads and validate the config:

   ```bash
   export BEACON_ASYMPTOTE_INGEST_URL=https://<ingest host returned by enrollment>
   export BEACON_ASYMPTOTE_SECRETS_FILE=~/.beacon/endpoint/asymptote/vector-secrets.json
   export BEACON_ASYMPTOTE_DATA_DIR=~/.beacon/endpoint/asymptote/vector-data
   mkdir -p "$BEACON_ASYMPTOTE_DATA_DIR"
   vector validate ./beacon-asymptote-pack/vector.toml
   vector --config ./beacon-asymptote-pack/vector.toml
   ```

3. Confirm delivery. `sh ./beacon-asymptote-pack/asymptote-ingest-smoke-test.sh`
   checks the key against `GET /v1/ingest/health` (200 means valid, 401 means
   revoked) and posts the log tail once. Events reach the dashboard's telemetry
   page within a minute or two.

## Wire contract

- `POST <ingest url>/v1/ingest/runtime` and `/v1/ingest/inventory`,
  `Authorization: Bearer bcn_device_…`, `Content-Type: application/x-ndjson`,
  `Content-Encoding: gzip`. HTTPS only.
- Limits: 8 MiB compressed, 10,000 lines and 1 MiB per line per request. The
  template batches well below that.
- `200` returns accepted and rejected line counts; `401` means the key is
  unknown, revoked or expired, or the approving user is no longer a member of
  the organization; `403` means the organization is not enabled for managed
  ingest; `429` and `5xx` are retried by Vector; other `4xx` are dropped.
- Revoking a device from the dashboard's Beacon Endpoints page takes effect at
  the ingest service within about a minute. Vector then logs 401s and drops the
  batches; re-run enrollment to issue a new key.

## Offline behavior

Vector keeps reading into a disk buffer (512 MiB for runtime, 256 MiB for
inventory) while the network is unavailable and drains it on reconnect. The
service accepts events with old timestamps; nothing is lost across a laptop
sleeping or a flight, up to the buffer size.
