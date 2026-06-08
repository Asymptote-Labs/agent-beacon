#!/bin/bash
set -euo pipefail

BEACON_BIN="/opt/beacon/bin/beacon"
BEACON_COLLECTOR="/opt/beacon/bin/beacon-otelcol"

RUNTIME_LOG="/var/log/beacon-agent/runtime.jsonl"
RUNTIME_DIR="$(dirname "$RUNTIME_LOG")"
RUNTIME_LOCK="${RUNTIME_LOG}.lock"

FALCON_HEC_ENDPOINT="${4:-}"
FALCON_HEC_TOKEN="${5:-}"
FALCON_SOURCE="${6:-beacon-endpoint-agent}"
FALCON_SOURCETYPE="${7:-json}"
OTLP_GRPC_PORT="${8:-4317}"
OTLP_HTTP_PORT="${9:-4318}"
FALCON_INDEX="${10:-}"

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

if [ -n "$FALCON_HEC_ENDPOINT" ]; then
  ARGS+=(--falcon-hec-endpoint "$FALCON_HEC_ENDPOINT")
fi

if [ -n "$FALCON_HEC_TOKEN" ]; then
  ARGS+=(--falcon-hec-token "$FALCON_HEC_TOKEN")
fi

ARGS+=(
  --falcon-source "$FALCON_SOURCE"
  --falcon-sourcetype "$FALCON_SOURCETYPE"
)

if [ -n "$FALCON_INDEX" ]; then
  ARGS+=(--falcon-index "$FALCON_INDEX")
fi

echo "Repairing Beacon system endpoint..."
"$BEACON_BIN" "${ARGS[@]}"

echo "Preparing runtime log path for user-run hooks..."
mkdir -p "$RUNTIME_DIR"
touch "$RUNTIME_LOG" "$RUNTIME_LOCK"
chown root:wheel "$RUNTIME_DIR" "$RUNTIME_LOG" "$RUNTIME_LOCK"
chmod 755 "$RUNTIME_DIR"
chmod 644 "$RUNTIME_LOG" "$RUNTIME_LOCK"

CONSOLE_USER="$(stat -f %Su /dev/console 2>/dev/null || true)"

if [ -z "$CONSOLE_USER" ] || [ "$CONSOLE_USER" = "root" ] || [ "$CONSOLE_USER" = "loginwindow" ]; then
  echo "No interactive console user found; skipping Claude hook install." >&2
  "$BEACON_BIN" endpoint config validate --system
  "$BEACON_BIN" endpoint status --system --json
  launchctl print system/com.beacon.endpoint.collector || true
  exit 0
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

echo "Granting $CONSOLE_USER append access to Beacon runtime log and lock..."
chmod +a "$CONSOLE_USER allow list,search,add_file,delete_child,readattr,writeattr" "$RUNTIME_DIR" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$RUNTIME_LOG" || true
chmod +a "$CONSOLE_USER allow read,write,append,readattr,writeattr,readextattr,writeextattr" "$RUNTIME_LOCK" || true

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

echo "Running manual Claude hook smoke test..."
sudo -u "$CONSOLE_USER" HOME="$HOME_DIR" sh -lc '
printf "{\"session_id\":\"jamf-smoke\"}" | \
BEACON_ENDPOINT_MODE=1 \
BEACON_ENDPOINT_LOG="/var/log/beacon-agent/runtime.jsonl" \
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

echo "Done. Fully restart Claude Code and start a new session to test real hook events."
