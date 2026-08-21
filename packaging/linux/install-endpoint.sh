#!/bin/sh
# Installs the Beacon endpoint in system mode.
#
# Called by the .deb/.rpm postinstall, and usable directly for fleet tooling. Configuration is
# environment-first with positional fallbacks, matching packaging/macos/install-endpoint.sh so a
# mixed fleet has one contract rather than two.
set -eu

if [ -z "${BEACON_BIN:-}" ]; then
  if [ -x "/opt/beacon/bin/beacon" ]; then
    BEACON_BIN="/opt/beacon/bin/beacon"
  else
    BEACON_BIN="beacon"
  fi
fi

BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-${1:-claude,codex}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${2:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${3:-4318}}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-${4:-}}"
BEACON_NO_START="${BEACON_NO_START:-${5:-0}}"
BEACON_SERVICE="${BEACON_SERVICE:-${6:-}}"
BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-}"
BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-}"
BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-}"
BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-}"
BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-}"
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-0}"
BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-}"

# The packaged collector sits beside the CLI. Auto-detected rather than required, so a manual
# invocation without BEACON_COLLECTOR still works.
if [ -z "$BEACON_COLLECTOR" ] && [ -x "/opt/beacon/bin/beacon-otelcol" ]; then
  BEACON_COLLECTOR="/opt/beacon/bin/beacon-otelcol"
fi

set -- endpoint install --system \
  --harness "$BEACON_ENDPOINT_HARNESSES" \
  --otlp-grpc-port "$BEACON_OTLP_GRPC_PORT" \
  --otlp-http-port "$BEACON_OTLP_HTTP_PORT"

[ -n "$BEACON_COLLECTOR" ] && set -- "$@" --collector "$BEACON_COLLECTOR"
# Empty means auto-detect: systemd when it is PID 1, otherwise a supervised collector.
[ -n "$BEACON_SERVICE" ] && set -- "$@" --service "$BEACON_SERVICE"
[ "$BEACON_NO_START" = "1" ] && set -- "$@" --no-start

[ -n "$BEACON_SPLUNK_HEC_ENDPOINT" ] && set -- "$@" --splunk-hec-endpoint "$BEACON_SPLUNK_HEC_ENDPOINT"
[ -n "$BEACON_SPLUNK_HEC_TOKEN" ] && set -- "$@" --splunk-hec-token "$BEACON_SPLUNK_HEC_TOKEN"
[ -n "$BEACON_SPLUNK_INDEX" ] && set -- "$@" --splunk-index "$BEACON_SPLUNK_INDEX"
[ -n "$BEACON_SPLUNK_SOURCE" ] && set -- "$@" --splunk-source "$BEACON_SPLUNK_SOURCE"
[ -n "$BEACON_SPLUNK_SOURCETYPE" ] && set -- "$@" --splunk-sourcetype "$BEACON_SPLUNK_SOURCETYPE"
[ "$BEACON_SPLUNK_INSECURE_SKIP_VERIFY" = "1" ] && set -- "$@" --splunk-insecure-skip-verify
[ -n "$BEACON_SPLUNK_CA_FILE" ] && set -- "$@" --splunk-ca-file "$BEACON_SPLUNK_CA_FILE"

exec "$BEACON_BIN" "$@"
