#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-/opt/beacon/bin/beacon-otelcol}"
BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-${4:-claude,codex}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${5:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${6:-4318}}"
BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-${7:-}}"
BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-${8:-}}"
BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-${9:-}}"
BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-${10:-}}"
BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-${11:-}}"
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-${12:-0}}"
BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-${13:-}}"
BEACON_FALCON_HEC_ENDPOINT="${BEACON_FALCON_HEC_ENDPOINT:-}"
BEACON_FALCON_HEC_TOKEN="${BEACON_FALCON_HEC_TOKEN:-}"
BEACON_FALCON_INDEX="${BEACON_FALCON_INDEX:-}"
BEACON_FALCON_SOURCE="${BEACON_FALCON_SOURCE:-}"
BEACON_FALCON_SOURCETYPE="${BEACON_FALCON_SOURCETYPE:-}"
BEACON_FALCON_INSECURE_SKIP_VERIFY="${BEACON_FALCON_INSECURE_SKIP_VERIFY:-0}"
BEACON_FALCON_CA_FILE="${BEACON_FALCON_CA_FILE:-}"

set -- endpoint repair \
  --collector "$BEACON_COLLECTOR" \
  --harness "$BEACON_ENDPOINT_HARNESSES" \
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

if [ -n "$BEACON_FALCON_HEC_ENDPOINT" ]; then
  set -- "$@" --falcon-hec-endpoint "$BEACON_FALCON_HEC_ENDPOINT"
fi
if [ -n "$BEACON_FALCON_HEC_TOKEN" ]; then
  set -- "$@" --falcon-hec-token "$BEACON_FALCON_HEC_TOKEN"
fi
if [ -n "$BEACON_FALCON_INDEX" ]; then
  set -- "$@" --falcon-index "$BEACON_FALCON_INDEX"
fi
if [ -n "$BEACON_FALCON_SOURCE" ]; then
  set -- "$@" --falcon-source "$BEACON_FALCON_SOURCE"
fi
if [ -n "$BEACON_FALCON_SOURCETYPE" ]; then
  set -- "$@" --falcon-sourcetype "$BEACON_FALCON_SOURCETYPE"
fi
case "$BEACON_FALCON_INSECURE_SKIP_VERIFY" in
  1|true|TRUE|yes|YES)
    set -- "$@" --falcon-insecure-skip-verify
    ;;
esac
if [ -n "$BEACON_FALCON_CA_FILE" ]; then
  set -- "$@" --falcon-ca-file "$BEACON_FALCON_CA_FILE"
fi

exec "$BEACON_BIN" "$@"
