#!/bin/sh
set -eu

CONFIG_PATH="${BEACON_ENDPOINT_CONFIG:-/Library/Application Support/Beacon/Endpoint/config.json}"

if [ ! -f "$CONFIG_PATH" ]; then
  echo "<result>missing</result>"
  exit 0
fi

HARNESSES="$(awk '
  /"harnesses"[[:space:]]*:/ { in_array = 1; next }
  in_array && /\]/ { exit }
  in_array {
    gsub(/[",]/, "")
    gsub(/^[[:space:]]+|[[:space:]]+$/, "")
    if ($0 != "") {
      if (out != "") {
        out = out "," $0
      } else {
        out = $0
      }
    }
  }
  END { print out }
' "$CONFIG_PATH")"

if [ -z "$HARNESSES" ]; then
  HARNESSES="unknown"
fi

echo "<result>$HARNESSES</result>"
