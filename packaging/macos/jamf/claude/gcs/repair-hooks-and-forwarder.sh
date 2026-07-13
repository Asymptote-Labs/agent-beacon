#!/bin/sh
set -eu

REPAIR_SCRIPT="${BEACON_REPAIR_HOOKS_SCRIPT:-/opt/beacon/jamf/claude/common/repair-hooks.sh}"
FORWARDER_SCRIPT="${BEACON_GCS_VECTOR_SCRIPT:-/opt/beacon/jamf/claude/gcs/install-forwarder.sh}"

GCS_BUCKET="${BEACON_GCS_BUCKET:-${4:-}}"
GCS_PREFIX="${BEACON_GCS_PREFIX:-${5:-beacon}}"
GCS_STORAGE_CLASS="${BEACON_GCS_STORAGE_CLASS:-${6:-STANDARD}}"
VECTOR_READ_FROM="${BEACON_VECTOR_READ_FROM:-${7:-end}}"
OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${8:-4317}}"
OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${9:-4318}}"

if [ ! -x "$REPAIR_SCRIPT" ]; then
  echo "Repair script not found or not executable at $REPAIR_SCRIPT" >&2
  exit 1
fi
if [ ! -x "$FORWARDER_SCRIPT" ]; then
  echo "GCS Vector forwarder script not found or not executable at $FORWARDER_SCRIPT" >&2
  exit 1
fi

echo "Installing GCS Vector runtime and inventory forwarder..."
"$FORWARDER_SCRIPT" _ _ _ "$GCS_BUCKET" "$GCS_PREFIX" "$GCS_STORAGE_CLASS" "$VECTOR_READ_FROM"

echo "Repairing endpoint and Claude hooks..."
"$REPAIR_SCRIPT" _ _ _ "$OTLP_GRPC_PORT" "$OTLP_HTTP_PORT"
