# Beacon Endpoint Agent AWS CloudWatch Logs Pack

This pack forwards Beacon endpoint JSONL events into AWS CloudWatch Logs. Beacon
still writes one local source of truth: `runtime.jsonl`. AWS credentials,
profiles, IAM roles, log groups, retention settings, and encryption stay in AWS,
Vector, or customer-managed deployment tooling, not in Beacon endpoint
configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- An AWS CloudWatch Logs log group for Beacon runtime logs.
- An IAM role or credentials available through the standard AWS credential
  provider chain for the process running Vector.

Recommended log group and stream layout:

```text
/aws/beacon/runtime
beacon-runtime/<hostname>
```

Example least-privilege IAM policy for a pre-created log group:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogStream",
        "logs:DescribeLogStreams",
        "logs:PutLogEvents"
      ],
      "Resource": "arn:aws:logs:us-east-1:111122223333:log-group:/aws/beacon/runtime:*"
    }
  ]
}
```

Add `logs:CreateLogGroup`, KMS permissions, or account-specific conditions only
if your AWS controls require them. Configure CloudWatch Logs retention, log group
encryption, subscription filters, exports, and access policies in AWS.

## Install

Generate this pack:

```bash
beacon endpoint cloudwatch install-pack --output ./beacon-cloudwatch-pack
```

The generated `vector.toml` points at the Beacon log path selected by the CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

For MDM or managed endpoint deployment, prefer Beacon system mode so your
customer-managed log shipper can tail `/var/log/beacon-agent/runtime.jsonl`
without per-user home directory ACLs.

## Production Forwarding

Use the generated Vector config as a customer-managed host-agent forwarding
template. Beacon remains the local JSONL producer; Vector tails `runtime.jsonl`,
checkpoints file offsets in its `data_dir`, batches Beacon events, and writes
JSON log events into AWS CloudWatch Logs.

Install Vector using your normal endpoint management tooling, then copy the
generated config into Vector's config directory. On a macOS system-mode Beacon
deployment, the generated config tails `/var/log/beacon-agent/runtime.jsonl`:

```bash
sudo mkdir -p /etc/vector
sudo cp ./beacon-cloudwatch-pack/vector.toml /etc/vector/beacon-cloudwatch.toml
export BEACON_CLOUDWATCH_LOG_GROUP="/aws/beacon/runtime"
export BEACON_CLOUDWATCH_LOG_STREAM_PREFIX="beacon-runtime"
export AWS_REGION="us-east-1"
vector validate /etc/vector/beacon-cloudwatch.toml
vector --config /etc/vector/beacon-cloudwatch.toml
```

In managed deployments, provide `BEACON_CLOUDWATCH_LOG_GROUP`, optional
`BEACON_CLOUDWATCH_LOG_STREAM_PREFIX`, `AWS_REGION`, and any AWS
credential-provider settings through the Vector service environment, host
identity, or MDM/secret tooling. Do not store AWS destination secrets in Beacon
endpoint configuration.

The Vector template is intentionally simple and expects a Vector version with
the `file` source, `remap` transform, and `aws_cloudwatch_logs` sink. It parses
each Beacon JSONL line and re-encodes the original Beacon event as a JSON
CloudWatch Logs event, without a Vector wrapper.

If you adapt the config or use another forwarder, it should:

- checkpoint file offsets,
- batch JSON records,
- use host-specific log streams,
- retry transient failures without duplicating the whole file,
- keep AWS credentials, IAM roles, log group retention, and encryption outside
  Beacon endpoint configuration.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint cloudwatch validate
```

Wait for your production forwarder to ship the new line. Beacon can write the
local validation event, but remote delivery must be confirmed with AWS tooling:

```bash
aws logs filter-log-events \
  --log-group-name "$BEACON_CLOUDWATCH_LOG_GROUP" \
  --filter-pattern '"Beacon endpoint AWS CloudWatch Logs validation event"' \
  --region "$AWS_REGION"
```

CloudWatch Logs Insights query:

```sql
fields @timestamp, vendor, product, destination.type, destination.mode, message
| filter message like /Beacon endpoint AWS CloudWatch Logs validation event/
| sort @timestamp desc
| limit 20
```

Expected validation fields:

```text
vendor=beacon product=endpoint-agent destination.type=cloudwatch destination.mode=aws_cloudwatch_logs
```

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, raw tool payloads, and other retained content may be forwarded to AWS
CloudWatch Logs. Use Beacon's `metadata` or `redacted` content retention modes
for stricter deployments.
