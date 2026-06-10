#!/usr/bin/env bash
set -euo pipefail

BEACON_HOME="${BEACON_HOME:-/tmp/beacon}"
BEACON_LOG_PATH="${BEACON_CLOUD_LOG_PATH:-$BEACON_HOME/runtime.jsonl}"

mkdir -p "$(dirname "$BEACON_LOG_PATH")"

if [ -n "${BEACON_CLOUD_GCS_BUCKET:-}" ] && [ -n "${BEACON_CLOUD_GCS_CREDENTIALS_B64:-}" ]; then
  echo "Beacon Cloud GCS forwarding is configured for bucket: $BEACON_CLOUD_GCS_BUCKET"
else
  echo "Beacon Cloud GCS forwarding is not active; set BEACON_CLOUD_GCS_BUCKET and BEACON_CLOUD_GCS_CREDENTIALS_B64 to enable uploads."
fi

echo "Beacon runtime log path: $BEACON_LOG_PATH"
