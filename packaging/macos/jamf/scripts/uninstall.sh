#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}" \
  /opt/beacon/scripts/uninstall-endpoint.sh "$@"
