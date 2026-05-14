#!/bin/sh
set -eu

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"
HOOK_HARNESSES="${BEACON_HOOK_HARNESSES:-${4:-cursor}}"
HOOK_LEVEL="${BEACON_HOOK_LEVEL:-${5:-user}}"
CONSOLE_USER="$(stat -f %Su /dev/console 2>/dev/null || echo "")"

if [ -z "$CONSOLE_USER" ] || [ "$CONSOLE_USER" = "root" ] || [ "$CONSOLE_USER" = "loginwindow" ]; then
  echo "No interactive console user found for Cursor hook installation." >&2
  exit 1
fi

HOME_DIR="$(dscl . -read "/Users/$CONSOLE_USER" NFSHomeDirectory 2>/dev/null | awk '{print $2}')"
if [ -z "$HOME_DIR" ] || [ ! -d "$HOME_DIR" ]; then
  echo "Unable to resolve home directory for $CONSOLE_USER." >&2
  exit 1
fi

exec sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" "$BEACON_BIN" endpoint hooks install \
  --harness "$HOOK_HARNESSES" \
  --level "$HOOK_LEVEL"
