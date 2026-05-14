#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"

"$BEACON_BIN" endpoint status --json
"$BEACON_BIN" endpoint wazuh validate
