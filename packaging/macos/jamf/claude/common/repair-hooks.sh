#!/bin/bash
set -euo pipefail

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-/opt/beacon/bin/beacon-otelcol}"

RUNTIME_LOG="${BEACON_RUNTIME_LOG:-/var/log/beacon-agent/runtime.jsonl}"
RUNTIME_DIR="$(dirname "$RUNTIME_LOG")"
RUNTIME_LOCK="${RUNTIME_LOG}.lock"
INVENTORY_LOG="${BEACON_INVENTORY_LOG:-$RUNTIME_DIR/inventory_state.jsonl}"
INVENTORY_LOCK="${INVENTORY_LOG}.lock"
INVENTORY_STATE="${BEACON_INVENTORY_STATE:-$RUNTIME_DIR/inventory-state.json}"
INVENTORY_STATE_LOCK="${INVENTORY_STATE}.lock"

OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${4:-4317}}"
OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${5:-4318}}"

if [ ! -x "$BEACON_BIN" ]; then
  echo "Beacon binary not found or not executable at $BEACON_BIN" >&2
  exit 1
fi

if [ ! -x "$BEACON_COLLECTOR" ]; then
  echo "Beacon collector not found or not executable at $BEACON_COLLECTOR" >&2
  exit 1
fi

ARGS=(
  endpoint repair
  --system
  --collector "$BEACON_COLLECTOR"
  --harness "claude,codex"
  --content-retention "full"
  --otlp-grpc-port "$OTLP_GRPC_PORT"
  --otlp-http-port "$OTLP_HTTP_PORT"
)

echo "Repairing Beacon system endpoint..."
"$BEACON_BIN" "${ARGS[@]}"

echo "Preparing runtime log path for user-run hooks..."
mkdir -p "$RUNTIME_DIR"
touch "$RUNTIME_LOG" "$RUNTIME_LOCK" "$INVENTORY_LOG" "$INVENTORY_LOCK" "$INVENTORY_STATE" "$INVENTORY_STATE_LOCK"
chown root:wheel "$RUNTIME_DIR" "$RUNTIME_LOG" "$RUNTIME_LOCK" "$INVENTORY_LOG" "$INVENTORY_LOCK" "$INVENTORY_STATE" "$INVENTORY_STATE_LOCK"
chmod 755 "$RUNTIME_DIR"
# Runtime log and lock stay 666 so user-run hooks can append even when a
# root-owned rotation or collector recreates them; Beacon's writers use the
# same shared mode.
chmod 666 "$RUNTIME_LOG" "$RUNTIME_LOCK"
chmod 644 "$INVENTORY_LOG" "$INVENTORY_LOCK" "$INVENTORY_STATE" "$INVENTORY_STATE_LOCK"

CONSOLE_USER="$(stat -f %Su /dev/console 2>/dev/null || true)"

if [ -z "$CONSOLE_USER" ] || [ "$CONSOLE_USER" = "root" ] || [ "$CONSOLE_USER" = "loginwindow" ]; then
  echo "No interactive console user found; skipping Claude hook install." >&2
  "$BEACON_BIN" endpoint config validate --system
  "$BEACON_BIN" endpoint status --system --json
  launchctl print system/com.beacon.endpoint.collector || true
  exit 1
fi

HOME_DIR="$(dscl . -read "/Users/$CONSOLE_USER" NFSHomeDirectory 2>/dev/null | awk '{print $2}')"

if [ -z "$HOME_DIR" ] || [ ! -d "$HOME_DIR" ]; then
  echo "Could not resolve home directory for console user: $CONSOLE_USER" >&2
  exit 1
fi

echo "Console user: $CONSOLE_USER"
echo "Home directory: $HOME_DIR"

echo "Resetting Beacon hook binary ownership for $CONSOLE_USER..."
HOOK_DIR="$HOME_DIR/.beacon/endpoint/hooks"
HOOK_BIN="$HOOK_DIR/beacon-hooks"
# Everything under the user's home is created and changed as the user, never as
# root: the user controls these paths, so a root chown/chmod/rm there follows any
# symlink or hard link they planted (a hard link to /opt/beacon/bin/beacon would be
# handed to them by a recursive chown). An entry the user does not own is renamed
# aside, as the user too: the destination is a name in a directory the user
# controls, and mv follows a symlink planted there, so as root it would move the
# entry wherever the user pointed it. Renaming within the same directory needs no
# permission on the entry itself, so the user can always do it.
for dir in "$HOME_DIR/.beacon" "$HOME_DIR/.beacon/endpoint" "$HOOK_DIR"; do
  if [ -L "$dir" ]; then
    echo "Refusing to repair hooks: $dir is a symbolic link" >&2
    exit 1
  fi
  if [ -e "$dir" ] && [ "$(stat -f %Su "$dir")" != "$CONSOLE_USER" ]; then
    aside="$dir.not-owned.$(date +%Y%m%d%H%M%S)"
    echo "Moving $dir (not owned by $CONSOLE_USER) aside to $aside" >&2
    sudo -u "$CONSOLE_USER" mv "$dir" "$aside"
  fi
done
sudo -u "$CONSOLE_USER" mkdir -p "$HOOK_DIR"
sudo -u "$CONSOLE_USER" chmod 755 "$HOME_DIR/.beacon" "$HOME_DIR/.beacon/endpoint" "$HOOK_DIR"
sudo -u "$CONSOLE_USER" rm -f "$HOOK_BIN"

echo "Granting $CONSOLE_USER append access to Beacon runtime and inventory logs..."
# The directory stays root-owned 0755 with no grant to add or remove entries: hooks only append
# to the files below. Such a grant would let a user swap a log for a link that the root collector
# and inventory job then open. Earlier versions of this script, and the Jamf guide's snippet,
# granted add_file (with or without delete_child) to whoever was the console user then, so every
# such allow entry is removed, whoever it names, highest index first so the indexes still to
# remove do not shift. Read-only grants stay.
revoke_entry_grants() {
  ls -led "$1" |
    awk '$1 ~ /^[0-9]+:$/ && / allow / && /add_file|add_subdirectory|delete_child/ { sub(":", "", $1); print $1 }' |
    sort -rn |
    while read -r index; do
      chmod -a# "$index" "$1"
    done
}
revoke_entry_grants "$RUNTIME_DIR"
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$RUNTIME_LOG" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$RUNTIME_LOCK" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$INVENTORY_LOG" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$INVENTORY_LOCK" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$INVENTORY_STATE" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$INVENTORY_STATE_LOCK" || true

echo "Installing Claude Code hooks as $CONSOLE_USER..."
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" "$BEACON_BIN" endpoint hooks install \
  --harness claude \
  --level user \
  --log-path "$RUNTIME_LOG"

HOOK_BIN="$HOME_DIR/.beacon/endpoint/hooks/beacon-hooks"

if [ ! -x "$HOOK_BIN" ]; then
  echo "Hook binary not found or not executable at $HOOK_BIN" >&2
  exit 1
fi

echo "Validating hook write permissions..."
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" test -w "$RUNTIME_LOG"
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" sh -c ": >> '$RUNTIME_LOCK'"
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" test -w "$INVENTORY_LOG"
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" sh -c ": >> '$INVENTORY_LOCK'"
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" test -w "$INVENTORY_STATE"
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" sh -c ": >> '$INVENTORY_STATE_LOCK'"

echo "Running manual Claude hook smoke test..."
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" BEACON_SMOKE_RUNTIME_LOG="$RUNTIME_LOG" sh -lc '
printf "{\"session_id\":\"jamf-claude-smoke\"}" | \
BEACON_ENDPOINT_MODE=1 \
BEACON_ENDPOINT_LOG="$BEACON_SMOKE_RUNTIME_LOG" \
BEACON_ENDPOINT_CONFIG="/Library/Application Support/Beacon/Endpoint/config.json" \
"$HOME/.beacon/endpoint/hooks/beacon-hooks" --platform claude session-start
'

echo "Validating system endpoint config..."
"$BEACON_BIN" endpoint config validate --system

echo "System endpoint status..."
"$BEACON_BIN" endpoint status --system --json

echo "LaunchDaemon status..."
launchctl print system/com.beacon.endpoint.collector || true

echo "Last Beacon runtime events..."
tail -n 10 "$RUNTIME_LOG" || true

echo "Last Beacon inventory events..."
tail -n 10 "$INVENTORY_LOG" || true

echo "Done. Fully restart Claude Code and start a new session to test real hook events."
