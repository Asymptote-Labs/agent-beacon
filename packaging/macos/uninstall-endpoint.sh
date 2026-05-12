#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-beacon}"
KEEP_LOGS_FLAG=""

if [ "${BEACON_KEEP_LOGS:-0}" = "1" ]; then
  KEEP_LOGS_FLAG="--keep-logs"
fi

exec "$BEACON_BIN" endpoint uninstall $KEEP_LOGS_FLAG

