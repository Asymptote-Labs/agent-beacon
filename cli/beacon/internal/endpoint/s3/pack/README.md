# Beacon Endpoint Agent AWS S3 Pack

This pack forwards Beacon endpoint JSONL events into an AWS S3 bucket. Beacon
writes runtime activity to `runtime.jsonl` and configuration inventory telemetry
to `inventory_state.jsonl`. AWS credentials,
profiles, IAM roles, bucket policies, object lifecycle rules, and server-side
encryption stay in AWS, Vector, or customer-managed deployment tooling, not in
Beacon endpoint configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- An AWS S3 bucket for Beacon runtime and inventory logs.
- An IAM role or credentials available through the standard AWS credential
  provider chain for the process running Vector or the AWS CLI smoke test.

Recommended S3 layout:

```text
s3://example-security-logs/beacon/runtime/date=YYYY-MM-DD/<timestamp>-<uuid>.jsonl.gz
s3://example-security-logs/beacon/inventory/date=YYYY-MM-DD/<timestamp>-<uuid>.jsonl.gz
```

Example least-privilege IAM policy for a dedicated Beacon prefix:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:PutObject"],
      "Resource": "arn:aws:s3:::example-security-logs/beacon/*"
    }
  ]
}
```

Add `s3:PutObjectTagging`, KMS permissions, or bucket-specific conditions only
if your AWS controls require them. Configure bucket lifecycle, retention,
server-side encryption, and access logging in AWS.

## Install

Generate this pack:

```bash
beacon endpoint s3 install-pack --output ./beacon-s3-pack
```

The generated smoke-test script points at the Beacon log path selected by the
CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

The generated Vector config also tails the sibling inventory log:

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
export BEACON_S3_BUCKET="example-security-logs"
export BEACON_S3_PREFIX="beacon"
export AWS_REGION="us-east-1"
./beacon-s3-pack/s3-upload-smoke-test.sh
```

The script uses `aws s3 cp` and the standard AWS credential provider chain. You
can provide credentials with an instance profile, SSO/profile configuration,
environment variables, or your managed endpoint secret tooling. Beacon does not
store AWS credentials.

Beacon does not store AWS credentials in endpoint configuration or generated
runtime state.

Confirm the uploaded object:

```bash
aws s3 ls "s3://${BEACON_S3_BUCKET}/${BEACON_S3_PREFIX}/runtime/smoke-tests/" --region "$AWS_REGION"
aws s3 cp "s3://${BEACON_S3_BUCKET}/${BEACON_S3_PREFIX}/runtime/smoke-tests/<object>.jsonl" - --region "$AWS_REGION" | grep "Beacon endpoint S3 validation event"
```

## Production Forwarding

For production, use the generated Vector config as a customer-managed host-agent
forwarding template. Beacon remains the local JSONL producer; Vector tails
`runtime.jsonl` and `inventory_state.jsonl`, checkpoints file offsets in its
`data_dir`, batches Beacon events, and writes gzip-compressed newline-delimited
JSON objects into AWS S3.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory. On a macOS system-mode Beacon
deployment, the generated config tails `/var/log/beacon-agent/runtime.jsonl` and
`/var/log/beacon-agent/inventory_state.jsonl`:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-s3-pack/vector.toml /etc/vector/beacon-s3.toml
export BEACON_S3_BUCKET="example-security-logs"
export BEACON_S3_PREFIX="beacon"
export AWS_REGION="us-east-1"
vector validate /etc/vector/beacon-s3.toml
vector --config /etc/vector/beacon-s3.toml
```

In managed deployments, provide `BEACON_S3_BUCKET`, optional
`BEACON_S3_PREFIX`, `AWS_REGION`, and any AWS credential-provider settings
through the Vector service environment, host identity, or MDM/secret tooling.
For the packaged macOS helper, set AWS provider-chain variables in the MDM
script environment; the helper persists set values such as `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_PROFILE`,
`AWS_SHARED_CREDENTIALS_FILE`, `AWS_CONFIG_FILE`,
`AWS_WEB_IDENTITY_TOKEN_FILE`, and `AWS_ROLE_ARN` to
`/Library/Application Support/Beacon/Forwarders/s3-vector.env` with mode
`0600` so launchd-started Vector can authenticate. Do not store AWS
destination secrets in Beacon endpoint configuration or `s3-vector.toml`.

The Vector template is intentionally simple and expects a Vector version with
the `file` source, `remap` transform, and `aws_s3` sink. It parses each Beacon
JSONL line and re-encodes the original Beacon event as NDJSON so S3 receives one
Beacon event per line, without a Vector wrapper. Runtime activity is written
under `${BEACON_S3_PREFIX}/runtime/date=.../`; inventory telemetry is written
under `${BEACON_S3_PREFIX}/inventory/date=.../`.

The template uses date-partitioned `key_prefix`, `filename_time_format = "%s"`,
and `filename_append_uuid = true` so production forwarding does not overwrite
previous S3 objects. It also sets `compression = "gzip"`,
`content_encoding = "gzip"`, and `content_type = "application/x-ndjson"`.

If you adapt the config or use another forwarder, it should:

- checkpoint file offsets,
- batch newline-delimited JSON records,
- use non-overwriting object keys,
- retry transient failures without duplicating the whole file,
- keep AWS credentials, IAM roles, bucket policy, lifecycle, and encryption
  outside Beacon endpoint configuration.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint s3 validate
```

Run the one-shot smoke test or wait for your production forwarder to ship the
new line. Beacon can write the local validation event, but remote delivery must
be confirmed with AWS tooling:

```bash
aws s3 ls "s3://${BEACON_S3_BUCKET}/${BEACON_S3_PREFIX}/runtime/" --recursive --region "$AWS_REGION"
aws s3 cp "s3://${BEACON_S3_BUCKET}/${BEACON_S3_PREFIX}/runtime/date=<date>/<object>.jsonl.gz" - --region "$AWS_REGION" | gzip -dc | grep "Beacon endpoint S3 validation event"
```

Expected validation fields:

```text
vendor=beacon product=endpoint-agent destination.type=s3 destination.mode=aws_s3_jsonl
```

## Content Handling

Beacon forwards retained prompt text, tool input, command output, raw tool
payloads, and related local telemetry to S3 subject to Beacon's secret
redaction, sanitization, truncation, and event-size limits.
