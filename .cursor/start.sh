#!/usr/bin/env bash
set -euo pipefail

BEACON_HOME="${BEACON_HOME:-/tmp/beacon}"
BEACON_LOG_PATH="${BEACON_CLOUD_LOG_PATH:-$BEACON_HOME/runtime.jsonl}"

mkdir -p "$(dirname "$BEACON_LOG_PATH")"

BEACON_CLOUD_UPLOAD_NORMALIZED="$(printf '%s' "${BEACON_CLOUD_UPLOAD:-gcs}" | tr '[:upper:]' '[:lower:]')"
BEACON_CLOUD_S3_REGION_EFFECTIVE="${BEACON_CLOUD_S3_REGION:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}"

case "$BEACON_CLOUD_UPLOAD_NORMALIZED" in
  s3)
    if [ -n "${BEACON_CLOUD_S3_BUCKET:-}" ] &&
      [ -n "$BEACON_CLOUD_S3_REGION_EFFECTIVE" ] &&
      [ -n "${AWS_ACCESS_KEY_ID:-}" ] &&
      [ -n "${AWS_SECRET_ACCESS_KEY:-}" ]; then
      echo "Beacon Cloud S3 forwarding is configured for bucket: $BEACON_CLOUD_S3_BUCKET in region: $BEACON_CLOUD_S3_REGION_EFFECTIVE"
    else
      echo "Beacon Cloud S3 forwarding is not active; set the S3 bucket, region, and AWS credentials to enable uploads."
    fi
    ;;
  *)
    if [ "$BEACON_CLOUD_UPLOAD_NORMALIZED" != "gcs" ]; then
      echo "Beacon Cloud upload destination '$BEACON_CLOUD_UPLOAD' is unknown; using the GCS fallback."
    fi
    if [ -n "${BEACON_CLOUD_GCS_BUCKET:-}" ] && [ -n "${BEACON_CLOUD_GCS_CREDENTIALS_B64:-}" ]; then
      echo "Beacon Cloud GCS forwarding is configured for bucket: $BEACON_CLOUD_GCS_BUCKET"
    else
      echo "Beacon Cloud GCS forwarding is not active; set the GCS bucket and credentials to enable uploads."
    fi
    ;;
esac

echo "Beacon runtime log path: $BEACON_LOG_PATH"
