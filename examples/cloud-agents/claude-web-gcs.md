# Claude Code Web to GCS Telemetry

This guide captures Beacon cloud-agent telemetry from Claude Code on the web and
uploads a readable per-run `runtime.jsonl` object to a customer-managed GCS
bucket.

The flow is local-only inside the cloud sandbox until upload time:

1. Claude Code on the web starts a cloud VM and clones the repository.
2. The Claude environment setup script installs Beacon hook tooling into
   `/tmp/beacon/bin`.
3. Project-level `.claude/settings.local.json` is generated inside the sandbox clone.
4. Hooks write Beacon JSONL to `/tmp/beacon/runtime.jsonl`.
5. Beacon uploads the final session snapshot to GCS when Claude stops or ends the session.

## 1. Prepare GCS

Run this from a workstation with `gcloud` access to the target project:

```bash
gcloud config set project <your-gcp-project>

export GCP_PROJECT="<your-gcp-project>"
export BEACON_TEST_BUCKET="your-beacon-cloud-agent-traces"
export BEACON_TEST_LOCATION="us-central1"
export BEACON_CLOUD_GCS_PREFIX="agent-traces/customer=my-team"

beacon cloud gcs setup \
  --project "$GCP_PROJECT" \
  --bucket "${BEACON_TEST_BUCKET}" \
  --location "${BEACON_TEST_LOCATION}" \
  --prefix "${BEACON_CLOUD_GCS_PREFIX}" \
  --service-account beacon-cloud-trace-uploader \
  --print

beacon cloud gcs setup \
  --project "$GCP_PROJECT" \
  --bucket "${BEACON_TEST_BUCKET}" \
  --location "${BEACON_TEST_LOCATION}" \
  --prefix "${BEACON_CLOUD_GCS_PREFIX}" \
  --service-account beacon-cloud-trace-uploader \
  --apply \
  --print-env
```

Copy the printed `BEACON_CLOUD_GCS_*` values into the Claude Code web
environment.

## 2. Configure Claude Web

In `claude.ai/code`, edit the target repository's cloud environment.

Set network access so the sandbox can reach `storage.googleapis.com`.
The sandbox must also reach `oauth2.googleapis.com` for service-account token exchange.

Add environment variables:

```bash
BEACON_ORIGIN=cloud
BEACON_RUN_PROVIDER=claude_code_web
BEACON_RUN_EPHEMERAL=true
BEACON_CLOUD_USER_ID_HASH=<stable-user-or-test-hash>
BEACON_CLOUD_GCS_BUCKET=<bucket>
BEACON_CLOUD_GCS_PREFIX=<prefix>
BEACON_CLOUD_GCS_CREDENTIALS_B64=<base64-service-account-json>
```

Add this setup script, replacing `v0.0.54` with the Beacon release you want to test:
cloud-agent support:

```bash
set -euo pipefail
mkdir -p /tmp/beacon/bin /tmp/beacon/logs

BEACON_VERSION="v0.0.54"
OS="linux"
case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

ARCHIVE="beacon_${BEACON_VERSION#v}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/asymptote-labs/agent-beacon/releases/download/${BEACON_VERSION}"
curl -fsSL "${BASE}/${ARCHIVE}" -o "/tmp/beacon/${ARCHIVE}"
tar -xzf "/tmp/beacon/${ARCHIVE}" -C /tmp/beacon/bin
chmod +x /tmp/beacon/bin/beacon /tmp/beacon/bin/beacon-hooks

mkdir -p .claude
/tmp/beacon/bin/beacon cloud claude-web print-hooks \
  --binary-path /tmp/beacon/bin/beacon-hooks \
  --log-path /tmp/beacon/runtime.jsonl > .claude/settings.local.json
```

The generated `.claude/settings.local.json` lives only in the cloud sandbox clone
unless the agent explicitly commits it.

## 3. Run and Verify

Start a Claude Code web task that uses tools, for example:

```text
Read the README, run pwd && ls, create a tiny temporary markdown note, then summarize what you did.
```

Verify the uploaded JSONL:

```bash
gcloud storage ls "gs://${BEACON_TEST_BUCKET}/${BEACON_CLOUD_GCS_PREFIX}/provider=claude_code_web/"
gcloud storage cp "gs://${BEACON_TEST_BUCKET}/${BEACON_CLOUD_GCS_PREFIX}/provider=claude_code_web/**/runtime.jsonl" /tmp/beacon-cloud-runtime.jsonl
head -20 /tmp/beacon-cloud-runtime.jsonl
```

Uploaded objects use a text content type so the GCS console and authenticated
object URLs can display the JSONL directly instead of forcing an NDJSON
download.

Expected fields include `vendor=beacon`, `product=endpoint-agent`,
`schema_version=1.0`, `origin=cloud`, `harness.name=claude`,
`run.provider=claude_code_web`, and a `run.run_id` matching the Claude remote
session ID when available.
