#!/bin/sh
set -eu

if [ -z "${BEACON_BIN:-}" ]; then
  if [ -x "/opt/beacon/bin/beacon" ]; then
    BEACON_BIN="/opt/beacon/bin/beacon"
  else
    BEACON_BIN="beacon"
  fi
fi

BEACON_KEEP_LOGS="${BEACON_KEEP_LOGS:-${4:-0}}"
BEACON_KEEP_CONFIG="${BEACON_KEEP_CONFIG:-${5:-0}}"

set -- endpoint uninstall --system

case "$BEACON_KEEP_LOGS" in
  1|true|TRUE|yes|YES)
    set -- "$@" --keep-logs
    ;;
esac

case "$BEACON_KEEP_CONFIG" in
  1|true|TRUE|yes|YES)
    set -- "$@" --keep-config
    ;;
esac

exec "$BEACON_BIN" "$@"

