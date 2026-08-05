#!/bin/sh
# Removes the Beacon endpoint installed in system mode.
#
# Mirrors packaging/macos/uninstall-endpoint.sh. Called by the .deb/.rpm removal scripts, where
# the defaults matter: a package *removal* keeps logs and config so an operator does not lose
# collected telemetry by accident, while a purge removes everything.
set -eu

if [ -z "${BEACON_BIN:-}" ]; then
  if [ -x "/opt/beacon/bin/beacon" ]; then
    BEACON_BIN="/opt/beacon/bin/beacon"
  else
    BEACON_BIN="beacon"
  fi
fi

BEACON_KEEP_LOGS="${BEACON_KEEP_LOGS:-${1:-0}}"
BEACON_KEEP_CONFIG="${BEACON_KEEP_CONFIG:-${2:-0}}"

set -- endpoint uninstall --system
[ "$BEACON_KEEP_LOGS" = "1" ] && set -- "$@" --keep-logs
[ "$BEACON_KEEP_CONFIG" = "1" ] && set -- "$@" --keep-config

exec "$BEACON_BIN" "$@"
