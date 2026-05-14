#!/bin/sh
set -eu

LOG_PATH="${BEACON_ENDPOINT_LOG:-/var/log/beacon-agent/runtime.jsonl}"

if [ ! -s "$LOG_PATH" ]; then
  echo "<result>no_events</result>"
  exit 0
fi

MODIFIED_AT="$(stat -f %m "$LOG_PATH" 2>/dev/null || echo 0)"
NOW="$(date +%s)"

if [ "$MODIFIED_AT" -le 0 ]; then
  echo "<result>unknown</result>"
  exit 0
fi

echo "<result>$((NOW - MODIFIED_AT))</result>"
