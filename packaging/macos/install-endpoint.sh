#!/bin/sh
set -eu

if [ -z "${BEACON_BIN:-}" ]; then
  if [ -x "/opt/beacon/bin/beacon" ]; then
    BEACON_BIN="/opt/beacon/bin/beacon"
  else
    BEACON_BIN="beacon"
  fi
fi

BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-${4:-claude,codex}}"
BEACON_CONTENT_RETENTION="${BEACON_CONTENT_RETENTION:-${5:-full}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${6:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${7:-4318}}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-${8:-}}"
BEACON_NO_START="${BEACON_NO_START:-${9:-0}}"
BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-${10:-}}"
BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-${11:-}}"
BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-${12:-}}"
BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-${13:-}}"
BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-${14:-}}"
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-${15:-0}}"
BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-${16:-}}"

if [ -z "$BEACON_COLLECTOR" ] && [ -x "/opt/beacon/bin/beacon-otelcol" ]; then
  BEACON_COLLECTOR="/opt/beacon/bin/beacon-otelcol"
fi

set -- endpoint install \
  --system \
  --harness "$BEACON_ENDPOINT_HARNESSES" \
  --content-retention "$BEACON_CONTENT_RETENTION" \
  --otlp-grpc-port "$BEACON_OTLP_GRPC_PORT" \
  --otlp-http-port "$BEACON_OTLP_HTTP_PORT"

if [ -n "$BEACON_COLLECTOR" ]; then
  set -- "$@" --collector "$BEACON_COLLECTOR"
fi

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

case "$BEACON_NO_START" in
  1|true|TRUE|yes|YES)
    set -- "$@" --no-start
    ;;
esac

exec "$BEACON_BIN" "$@"

