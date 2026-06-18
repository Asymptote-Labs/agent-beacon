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
chmod 644 "$RUNTIME_LOG" "$RUNTIME_LOCK" "$INVENTORY_LOG" "$INVENTORY_LOCK" "$INVENTORY_STATE" "$INVENTORY_STATE_LOCK"

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
USER_GROUP="$(id -gn "$CONSOLE_USER")"
HOOK_DIR="$HOME_DIR/.beacon/endpoint/hooks"
HOOK_BIN="$HOOK_DIR/beacon-hooks"
mkdir -p "$HOOK_DIR"
chown -R "$CONSOLE_USER:$USER_GROUP" "$HOME_DIR/.beacon"
chmod 755 "$HOME_DIR/.beacon" "$HOME_DIR/.beacon/endpoint" "$HOOK_DIR"
rm -f "$HOOK_BIN"

echo "Granting $CONSOLE_USER append access to Beacon runtime and inventory logs..."
chmod +a "$CONSOLE_USER allow list,search,add_file,delete_child,readattr,writeattr" "$RUNTIME_DIR" || true
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
