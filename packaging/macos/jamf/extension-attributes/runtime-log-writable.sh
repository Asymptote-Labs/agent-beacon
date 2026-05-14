#!/bin/sh
set -eu

LOG_PATH="${BEACON_ENDPOINT_LOG:-/var/log/beacon-agent/runtime.jsonl}"
LOG_DIR="$(dirname "$LOG_PATH")"

if [ -f "$LOG_PATH" ] && [ -w "$LOG_PATH" ]; then
  echo "<result>writable</result>"
  exit 0
fi

if [ ! -f "$LOG_PATH" ] && [ -d "$LOG_DIR" ] && [ -w "$LOG_DIR" ]; then
  echo "<result>creatable</result>"
  exit 0
fi

echo "<result>not_writable</result>"
