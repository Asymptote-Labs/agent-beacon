# Beacon Endpoint Agent Google Cloud Storage Pack

This pack forwards Beacon endpoint JSONL events into a Google Cloud Storage
bucket. Beacon writes runtime activity to `runtime.jsonl` and configuration
inventory telemetry to `inventory_state.jsonl`. Google credentials, service
accounts, workload identity, bucket IAM, object lifecycle rules, retention
policies, and encryption stay in Google Cloud, Vector, or customer-managed
deployment tooling, not in Beacon endpoint configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- A Google Cloud Storage bucket for Beacon runtime and inventory logs.
- Credentials supported by the process doing the upload. The packaged macOS
  Vector 0.56 path requires service-account JSON through
  `GOOGLE_APPLICATION_CREDENTIALS`; `gcloud`/`gsutil` smoke tests may use their
  own active configuration.

Recommended GCS layout:

```text
gs://example-security-logs/beacon/runtime/date=YYYY-MM-DD/<timestamp>-<uuid>.jsonl.gz
gs://example-security-logs/beacon/inventory/date=YYYY-MM-DD/<timestamp>-<uuid>.jsonl.gz
```

Grant the Vector service identity only the bucket/prefix permissions it needs.
For a dedicated Beacon bucket, `roles/storage.objectCreator` is usually enough
for production uploads because it can create objects without listing or reading
them. Add viewer, retention, CMEK, or bucket-specific conditional IAM only if
your Google Cloud controls require them. Configure lifecycle, retention,
versioning, audit logs, and encryption in Google Cloud.

Vector's GCS startup healthcheck requires `storage.objects.get`, which the
write-only role intentionally lacks. The generated sinks disable that
healthcheck and rely on upload errors plus remote validation by a separate
reader identity; this preserves write-only endpoint credentials.

## Install

Generate this pack:

```bash
beacon endpoint gcs install-pack --output ./beacon-gcs-pack
```

The generated smoke-test script points at the Beacon log path selected by the
CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

The generated Vector config and smoke-test script also use the sibling inventory
log:

- User mode: `~/.beacon/endpoint/logs/inventory_state.jsonl`
- System mode: `/var/log/beacon-agent/inventory_state.jsonl`
- Custom mode: `inventory_state.jsonl` in the same directory as `--log-path`

For MDM or managed endpoint deployment, prefer Beacon system mode so your
customer-managed log shipper can tail `/var/log/beacon-agent/runtime.jsonl`
without per-user home directory ACLs.

The generated `vector.toml` uses the selected Beacon runtime log path and the
derived sibling inventory log path.

## One-Shot Smoke Test

Use the generated script to upload the current file once. This is only for
validation; do not use it as production forwarding because it re-uploads the
whole file every time.

```bash
export BEACON_GCS_BUCKET="example-security-logs"
export BEACON_GCS_PREFIX="beacon"
./beacon-gcs-pack/gcs-upload-smoke-test.sh
```

The script uses `gcloud storage cp` when available and falls back to `gsutil cp`.
Both rely on Application Default Credentials, workload identity, active gcloud
configuration, or your managed endpoint secret tooling. Beacon does not store
Google Cloud credentials.

Beacon does not store Google Cloud credentials in endpoint configuration or
generated runtime state.

Confirm the uploaded object:

```bash
gcloud storage ls "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/runtime/smoke-tests/"
gcloud storage cat "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/runtime/smoke-tests/<object>.jsonl" | grep "Beacon endpoint GCS validation event"
```

## Production Forwarding

For production, use the generated Vector config as a customer-managed host-agent
forwarding template. Beacon remains the local JSONL producer; Vector tails
`runtime.jsonl` and `inventory_state.jsonl`, checkpoints file offsets in its
`data_dir`, batches Beacon events, and writes newline-delimited JSON objects
into Google Cloud Storage.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory. On a macOS system-mode Beacon
deployment, the generated config tails `/var/log/beacon-agent/runtime.jsonl` and
`/var/log/beacon-agent/inventory_state.jsonl`:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-gcs-pack/vector.toml /etc/vector/beacon-gcs.toml
export BEACON_GCS_BUCKET="example-security-logs"
export BEACON_GCS_PREFIX="beacon"
vector validate /etc/vector/beacon-gcs.toml
vector --config /etc/vector/beacon-gcs.toml
```

In managed deployments, provide `BEACON_GCS_BUCKET`, optional
`BEACON_GCS_PREFIX`, optional `BEACON_GCS_STORAGE_CLASS`, and a
`GOOGLE_APPLICATION_CREDENTIALS` path through the Vector service environment,
MDM, or secret tooling. The signed macOS package pins Vector 0.56, whose GCS sink
supports service-account JSON or GCE metadata but not external-account Workload
Identity Federation. Interactive `gcloud auth application-default login`
credentials are not a reliable root launchd contract. Do not store Google Cloud
destination secrets in Beacon endpoint configuration.

`BEACON_GCS_PREFIX` is the shared root prefix (for example, `beacon`), not the
runtime path. The template appends `runtime/` and `inventory/`. For compatibility
with older smoke-test settings, the generated smoke script normalizes one trailing
`/runtime` or `/inventory` back to the shared root before uploading.

The Vector template is intentionally simple and expects a Vector version with
the `file` source, `remap` transform, and `gcp_cloud_storage` sink. It parses
each Beacon JSONL line and re-encodes the original Beacon event as NDJSON so GCS
receives one Beacon event per line, without a Vector wrapper. Runtime activity
is written under `${BEACON_GCS_PREFIX}/runtime/date=.../`; inventory telemetry
is written under `${BEACON_GCS_PREFIX}/inventory/date=.../`.

When an event contains token or runtime-reported cost attribution, the Vector
template preserves the canonical nested `gen_ai.usage` payload without adding
parallel usage fields.

The template uses date-partitioned `key_prefix`, `filename_time_format = "%s"`,
and `filename_append_uuid = true` so production forwarding does not overwrite
previous GCS objects. It writes uncompressed `.jsonl` with
`content_type = "application/x-ndjson"`. This avoids ambiguous GCS
`Content-Encoding` and filename-extension behavior that causes HTTP readers
such as ClickHouse to decompress an object twice.
Runtime log forwarding starts at the end of the active log to avoid backfilling
historical session activity when Vector is first installed. Inventory forwarding
starts at the beginning of `inventory_state.jsonl` so the first snapshot is not
missed if the file was created before Vector began watching it.

If you adapt the config or use another forwarder, it should:

- checkpoint file offsets,
- batch newline-delimited JSON records,
- use non-overwriting object keys,
- retry transient failures without duplicating the whole file,
- keep Google credentials, service-account bindings, bucket IAM, lifecycle,
  retention, and encryption outside Beacon endpoint configuration.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint gcs validate
```

Run the one-shot smoke test or wait for your production forwarder to ship the
new line. Beacon can write the local validation event, but remote delivery must
be confirmed with Google Cloud tooling:

```bash
gcloud storage ls "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/runtime/**"
gcloud storage cat "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/runtime/date=<date>/<object>.jsonl" | grep "Beacon endpoint GCS validation event"
```

Expected validation fields:

```text
vendor=beacon product=endpoint-agent destination.type=gcs destination.mode=google_cloud_storage_jsonl
```

## Content Handling

Beacon forwards retained prompt text, tool input, command output, raw tool
payloads, and related local telemetry to GCS subject to Beacon's secret
redaction, sanitization, truncation, and event-size limits.
