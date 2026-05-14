#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}" \
BEACON_COLLECTOR="${BEACON_COLLECTOR:-/opt/beacon/bin/beacon-otelcol}" \
  /opt/beacon/scripts/install-endpoint.sh "$@"
