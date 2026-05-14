#!/bin/sh
set -eu

CONFIG_PATH="${BEACON_ENDPOINT_CONFIG:-/Library/Application Support/Beacon/Endpoint/config.json}"

if [ ! -f "$CONFIG_PATH" ]; then
  echo "<result>missing</result>"
  exit 0
fi

RETENTION="$(awk -F'"' '/"content_retention"[[:space:]]*:/ {print $4; exit}' "$CONFIG_PATH")"
if [ -z "$RETENTION" ]; then
  RETENTION="unknown"
fi

echo "<result>$RETENTION</result>"
