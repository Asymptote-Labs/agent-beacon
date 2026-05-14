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
BEACON_CONTENT_RETENTION="${BEACON_CONTENT_RETENTION:-${5:-metadata}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${6:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${7:-4318}}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-${8:-}}"
BEACON_NO_START="${BEACON_NO_START:-${9:-0}}"

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

case "$BEACON_NO_START" in
  1|true|TRUE|yes|YES)
    set -- "$@" --no-start
    ;;
esac

exec "$BEACON_BIN" "$@"

