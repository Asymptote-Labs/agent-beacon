#!/bin/sh
set -eu

LABEL="${BEACON_SERVICE_LABEL:-com.beacon.endpoint.collector}"

if ! command -v launchctl >/dev/null 2>&1; then
  echo "<result>launchctl_unavailable</result>"
  exit 0
fi

STATUS="$(launchctl print "system/$LABEL" 2>&1 || true)"
case "$STATUS" in
  *"state = running"*|*"pid ="*)
    echo "<result>running</result>"
    ;;
  *"Could not find service"*|*"No such process"*)
    echo "<result>not_loaded</result>"
    ;;
  *)
    echo "<result>loaded_not_running</result>"
    ;;
esac
