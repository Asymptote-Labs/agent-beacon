# Beacon Endpoint Agent Google Cloud Storage Pack

This pack forwards Beacon endpoint JSONL events into a Google Cloud Storage
bucket. Beacon still writes one local source of truth: `runtime.jsonl`. Google
credentials, service accounts, workload identity, bucket IAM, object lifecycle
rules, retention policies, and encryption stay in Google Cloud, Vector, or
customer-managed deployment tooling, not in Beacon endpoint configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- A Google Cloud Storage bucket for Beacon runtime logs.
- A service account or workload identity available through Application Default
  Credentials for the process running Vector, `gcloud`, or `gsutil`.

Recommended GCS layout:

```text
gs://example-security-logs/beacon/runtime/date=YYYY-MM-DD/<timestamp>-<uuid>.jsonl.gz
```

Grant the Vector service identity only the bucket/prefix permissions it needs.
For a dedicated Beacon bucket, `roles/storage.objectCreator` is usually enough
for production uploads because it can create objects without listing or reading
them. Add viewer, retention, CMEK, or bucket-specific conditional IAM only if
your Google Cloud controls require them. Configure lifecycle, retention,
versioning, audit logs, and encryption in Google Cloud.

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

For MDM or managed endpoint deployment, prefer Beacon system mode so your
customer-managed log shipper can tail `/var/log/beacon-agent/runtime.jsonl`
without per-user home directory ACLs.

The generated `vector.toml` uses the same selected Beacon log path.

## One-Shot Smoke Test

Use the generated script to upload the current file once. This is only for
validation; do not use it as production forwarding because it re-uploads the
whole file every time.

```bash
export BEACON_GCS_BUCKET="example-security-logs"
export BEACON_GCS_PREFIX="beacon/runtime"
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
gcloud storage ls "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/smoke-tests/"
gcloud storage cat "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/smoke-tests/<object>.jsonl" | grep "Beacon endpoint GCS validation event"
```

## Production Forwarding

For production, use the generated Vector config as a customer-managed host-agent
forwarding template. Beacon remains the local JSONL producer; Vector tails
`runtime.jsonl`, checkpoints file offsets in its `data_dir`, batches Beacon
events, and writes gzip-compressed newline-delimited JSON objects into Google
Cloud Storage.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory. On a macOS system-mode Beacon
deployment, the generated config tails `/var/log/beacon-agent/runtime.jsonl`:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-gcs-pack/vector.toml /etc/vector/beacon-gcs.toml
export BEACON_GCS_BUCKET="example-security-logs"
export BEACON_GCS_PREFIX="beacon/runtime"
vector validate /etc/vector/beacon-gcs.toml
vector --config /etc/vector/beacon-gcs.toml
```

In managed deployments, provide `BEACON_GCS_BUCKET`, optional
`BEACON_GCS_PREFIX`, optional `BEACON_GCS_STORAGE_CLASS`, and any Google
Application Default Credentials or workload identity settings through the Vector
service environment, host identity, MDM, or secret tooling. Do not store Google
Cloud destination secrets in Beacon endpoint configuration.

The Vector template is intentionally simple and expects a Vector version with
the `file` source, `remap` transform, and `gcp_cloud_storage` sink. It parses
each Beacon JSONL line and re-encodes the original Beacon event as NDJSON so GCS
receives one Beacon event per line, without a Vector wrapper.

The template uses date-partitioned `key_prefix`, `filename_time_format = "%s"`,
and `filename_append_uuid = true` so production forwarding does not overwrite
previous GCS objects. It also sets `compression = "gzip"`,
`content_encoding = "gzip"`, and `content_type = "application/x-ndjson"`.

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
gcloud storage ls "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/**"
gcloud storage cat "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/date=<date>/<object>.jsonl.gz" | gzip -dc | grep "Beacon endpoint GCS validation event"
```

Expected validation fields:

```text
vendor=beacon product=endpoint-agent destination.type=gcs destination.mode=google_cloud_storage_jsonl
```

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, raw tool payloads, and other retained content may be forwarded to GCS.
Use Beacon's `metadata` or `redacted` content retention modes for stricter
deployments.
