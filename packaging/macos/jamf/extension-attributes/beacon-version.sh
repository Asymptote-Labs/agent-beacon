#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"

if [ ! -x "$BEACON_BIN" ]; then
  echo "<result>not_installed</result>"
  exit 0
fi

VERSION="$("$BEACON_BIN" version 2>/dev/null || true)"
if [ -z "$VERSION" ]; then
  VERSION="unknown"
fi

echo "<result>$VERSION</result>"
