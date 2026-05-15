#!/bin/sh
set -eu

CONFIG_PATH="${BEACON_ENDPOINT_CONFIG:-/Library/Application Support/Beacon/Endpoint/config.json}"

if [ ! -f "$CONFIG_PATH" ]; then
  echo "<result>missing</result>"
  exit 0
fi

STATE="$(awk '
  /"splunk_hec"[[:space:]]*:/ { in_splunk = 1 }
  in_splunk && /"enabled"[[:space:]]*:[[:space:]]*true/ { enabled = 1 }
  in_splunk && /"endpoint"[[:space:]]*:[[:space:]]*"/ { endpoint = 1 }
  END {
    if (enabled || endpoint) {
      print "configured"
    } else {
      print "not_configured"
    }
  }
' "$CONFIG_PATH")"

echo "<result>$STATE</result>"
