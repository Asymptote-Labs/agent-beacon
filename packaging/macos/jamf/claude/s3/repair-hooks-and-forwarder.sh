#!/bin/sh
set -eu

REPAIR_SCRIPT="${BEACON_REPAIR_HOOKS_SCRIPT:-/opt/beacon/jamf/claude/common/repair-hooks.sh}"
FORWARDER_SCRIPT="${BEACON_S3_VECTOR_SCRIPT:-/opt/beacon/jamf/claude/s3/install-forwarder.sh}"

S3_BUCKET="${BEACON_S3_BUCKET:-${4:-}}"
AWS_REGION_VALUE="${AWS_REGION:-${5:-}}"
S3_PREFIX="${BEACON_S3_PREFIX:-${6:-beacon/runtime}}"
S3_STORAGE_CLASS="${BEACON_S3_STORAGE_CLASS:-${7:-STANDARD}}"
VECTOR_READ_FROM="${BEACON_VECTOR_READ_FROM:-${8:-end}}"
OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${9:-4317}}"
OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${10:-4318}}"

if [ ! -x "$REPAIR_SCRIPT" ]; then
  echo "Repair script not found or not executable at $REPAIR_SCRIPT" >&2
  exit 1
fi

if [ ! -x "$FORWARDER_SCRIPT" ]; then
  echo "S3 Vector forwarder script not found or not executable at $FORWARDER_SCRIPT" >&2
  exit 1
fi

echo "Installing S3 Vector runtime-log forwarder..."
"$FORWARDER_SCRIPT" _ _ _ "$S3_BUCKET" "$AWS_REGION_VALUE" "$S3_PREFIX" "$S3_STORAGE_CLASS" "$VECTOR_READ_FROM"

echo "Repairing endpoint and Claude hooks..."
"$REPAIR_SCRIPT" _ _ _ "$OTLP_GRPC_PORT" "$OTLP_HTTP_PORT"
