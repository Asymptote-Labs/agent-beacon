#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-/opt/beacon/bin/beacon-otelcol}"
BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-${1:-claude,codex}}"
BEACON_CONTENT_RETENTION="${BEACON_CONTENT_RETENTION:-${2:-full}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${3:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${4:-4318}}"
BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-${5:-}}"
BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-${6:-}}"
BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-${7:-}}"
BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-${8:-}}"
BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-${9:-}}"
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-${10:-0}}"
BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-${11:-}}"

set -- endpoint repair \
  --collector "$BEACON_COLLECTOR" \
  --harness "$BEACON_ENDPOINT_HARNESSES" \
  --content-retention "$BEACON_CONTENT_RETENTION" \
  --otlp-grpc-port "$BEACON_OTLP_GRPC_PORT" \
  --otlp-http-port "$BEACON_OTLP_HTTP_PORT"

if [ -n "$BEACON_SPLUNK_HEC_ENDPOINT" ]; then
  set -- "$@" --splunk-hec-endpoint "$BEACON_SPLUNK_HEC_ENDPOINT"
fi
if [ -n "$BEACON_SPLUNK_HEC_TOKEN" ]; then
  set -- "$@" --splunk-hec-token "$BEACON_SPLUNK_HEC_TOKEN"
fi
if [ -n "$BEACON_SPLUNK_INDEX" ]; then
  set -- "$@" --splunk-index "$BEACON_SPLUNK_INDEX"
fi
if [ -n "$BEACON_SPLUNK_SOURCE" ]; then
  set -- "$@" --splunk-source "$BEACON_SPLUNK_SOURCE"
fi
if [ -n "$BEACON_SPLUNK_SOURCETYPE" ]; then
  set -- "$@" --splunk-sourcetype "$BEACON_SPLUNK_SOURCETYPE"
fi
case "$BEACON_SPLUNK_INSECURE_SKIP_VERIFY" in
  1|true|TRUE|yes|YES)
    set -- "$@" --splunk-insecure-skip-verify
    ;;
esac
if [ -n "$BEACON_SPLUNK_CA_FILE" ]; then
  set -- "$@" --splunk-ca-file "$BEACON_SPLUNK_CA_FILE"
fi

exec "$BEACON_BIN" "$@"
