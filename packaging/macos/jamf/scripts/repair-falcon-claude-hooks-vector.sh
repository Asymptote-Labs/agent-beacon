#!/bin/sh
set -eu

REPAIR_SCRIPT="${BEACON_REPAIR_HOOKS_SCRIPT:-/opt/beacon/jamf/scripts/repair-falcon-claude-hooks.sh}"
FORWARDER_SCRIPT="${BEACON_FALCON_VECTOR_SCRIPT:-/opt/beacon/jamf/scripts/install-falcon-vector-forwarder.sh}"

FALCON_HEC_ENDPOINT="${BEACON_FALCON_HEC_ENDPOINT:-${4:-}}"
FALCON_HEC_TOKEN="${BEACON_FALCON_HEC_TOKEN:-${5:-}}"
FALCON_SOURCE="${BEACON_FALCON_SOURCE:-${6:-beacon-endpoint-agent}}"
FALCON_SOURCETYPE="${BEACON_FALCON_SOURCETYPE:-${7:-json}}"
OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${8:-4317}}"
OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${9:-4318}}"
FALCON_INDEX="${BEACON_FALCON_INDEX:-${10:-}}"

if [ ! -x "$REPAIR_SCRIPT" ]; then
  echo "Repair script not found or not executable at $REPAIR_SCRIPT" >&2
  exit 1
fi

if [ ! -x "$FORWARDER_SCRIPT" ]; then
  echo "Falcon Vector forwarder script not found or not executable at $FORWARDER_SCRIPT" >&2
  exit 1
fi

echo "Repairing endpoint and Claude hooks without collector-based Falcon forwarding..."
"$REPAIR_SCRIPT" _ _ _ "" "" "" "" "$OTLP_GRPC_PORT" "$OTLP_HTTP_PORT" ""

echo "Installing Falcon Vector runtime-log forwarder..."
"$FORWARDER_SCRIPT" _ _ _ "$FALCON_HEC_ENDPOINT" "$FALCON_HEC_TOKEN" "$FALCON_SOURCE" "$FALCON_SOURCETYPE" "$FALCON_INDEX"
