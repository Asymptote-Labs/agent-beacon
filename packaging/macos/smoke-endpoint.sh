#!/bin/sh
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
  echo "Beacon endpoint smoke test is macOS-only; skipping on $(uname -s)."
  exit 0
fi

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
ORIGINAL_HOOKS="$TMP_DIR/hooks.bin.original"
HOOKS_BIN="$ROOT_DIR/cli/beacon/internal/embedded/hooks.bin"

cleanup() {
  if [ -f "$ORIGINAL_HOOKS" ]; then
    cp "$ORIGINAL_HOOKS" "$HOOKS_BIN"
    chmod 644 "$HOOKS_BIN"
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

cp "$HOOKS_BIN" "$ORIGINAL_HOOKS"

BIN_DIR="$TMP_DIR/bin"
HOME_DIR="$TMP_DIR/home"
LOG_PATH="$TMP_DIR/runtime.jsonl"
COLLECTOR_BIN="$BIN_DIR/beacon-otelcol"
BEACON_BIN="$BIN_DIR/beacon"

mkdir -p "$BIN_DIR" "$HOME_DIR" "$HOME_DIR/Library/LaunchAgents"

cat >"$COLLECTOR_BIN" <<'EOF'
#!/bin/sh
echo "fake beacon-otelcol for smoke test"
EOF
chmod +x "$COLLECTOR_BIN"

echo "Building temporary beacon-hooks..."
(
  cd "$ROOT_DIR/cli/beacon-hooks"
  go build -o "$HOOKS_BIN" .
)

echo "Building temporary beacon..."
(
  cd "$ROOT_DIR/cli/beacon"
  go build -o "$BEACON_BIN" .
)

run_beacon() {
  HOME="$HOME_DIR" "$BEACON_BIN" "$@"
}

echo "Installing endpoint config in temporary HOME..."
run_beacon endpoint install \
  --user \
  --no-start \
  --collector "$COLLECTOR_BIN" \
  --log-path "$LOG_PATH" \
  --harness claude,codex \
  --otlp-grpc-port 55317 \
  --otlp-http-port 55318

test -f "$HOME_DIR/.beacon/endpoint/config.json"
test -f "$HOME_DIR/.beacon/endpoint/otelcol.yaml"
test -f "$HOME_DIR/Library/LaunchAgents/com.beacon.endpoint.collector.user.plist"
test -f "$LOG_PATH"

echo "Checking endpoint status..."
run_beacon endpoint status --user --log-path "$LOG_PATH" >/dev/null

echo "Writing Wazuh validation event..."
run_beacon endpoint wazuh validate --user --log-path "$LOG_PATH" >/dev/null

echo "Checking Elastic Filebeat config generation..."
run_beacon endpoint elastic print-config --user --log-path "$LOG_PATH" >/dev/null

if ! grep -q '"action":"telemetry.enabled"' "$LOG_PATH"; then
  echo "expected telemetry.enabled event in runtime log" >&2
  exit 1
fi

if ! grep -q '"category":"validation"' "$LOG_PATH"; then
  echo "expected validation event in runtime log" >&2
  exit 1
fi

echo "Installing Cursor hooks in temporary HOME..."
run_beacon endpoint hooks install --harness cursor --user --log-path "$LOG_PATH" >/dev/null
run_beacon endpoint hooks status --harness cursor --user --log-path "$LOG_PATH" >/dev/null
test -f "$HOME_DIR/.cursor/hooks.json"

if ! grep -q 'BEACON_ENDPOINT_MODE=1' "$HOME_DIR/.cursor/hooks.json"; then
  echo "expected Beacon hook command in Cursor hooks.json" >&2
  exit 1
fi

echo "Installing and exercising OpenCode plugin in temporary HOME..."
run_beacon endpoint hooks install --harness opencode --user --log-path "$LOG_PATH" >/dev/null
run_beacon endpoint hooks status --harness opencode --user --log-path "$LOG_PATH" >/dev/null
OPENCODE_PLUGIN="$HOME_DIR/.config/opencode/plugins/beacon.ts"
OPENCODE_HOOK="$HOME_DIR/.beacon/endpoint/hooks/beacon-hooks"
test -f "$OPENCODE_PLUGIN"
test -x "$OPENCODE_HOOK"

if grep -q '__BEACON_' "$OPENCODE_PLUGIN"; then
  echo "OpenCode plugin contains unresolved Beacon placeholders" >&2
  exit 1
fi

emit_opencode() {
  printf '%s\n' "$1" | \
    HOME="$HOME_DIR" BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG="$LOG_PATH" \
    "$OPENCODE_HOOK" --platform opencode opencode-event >/dev/null
}

emit_opencode '{"type":"chat.message","session_id":"ses_smoke","directory":"/tmp/project","model":"test/model","output":{"parts":[{"type":"text","text":"summarize"}]}}'
emit_opencode '{"type":"tool.execute.after","session_id":"ses_smoke","directory":"/tmp/project","tool_name":"bash","call_id":"call_bash","duration_ms":5,"tool_input":{"command":"git status --short"},"tool_response":{"output":"","metadata":{"exitCode":0}}}'
emit_opencode '{"type":"tool.execute.after","session_id":"ses_smoke","directory":"/tmp/project","tool_name":"write","call_id":"call_write","tool_input":{"filePath":"/tmp/project/smoke.txt","content":"value"},"tool_response":{"output":"ok","metadata":{}}}'
emit_opencode '{"type":"permission.replied","session_id":"ses_smoke","properties":{"sessionID":"ses_smoke","requestID":"per_smoke","reply":"reject"}}'
emit_opencode '{"type":"session.diff","session_id":"ses_smoke","properties":{"sessionID":"ses_smoke","diff":[]}}'

for action in prompt.submitted command.executed file.modified approval.denied; do
  if ! grep -q "\"action\":\"$action\"" "$LOG_PATH"; then
    echo "expected OpenCode $action event in runtime log" >&2
    exit 1
  fi
done

if grep '"session":{"id":"ses_smoke"' "$LOG_PATH" | grep -q 'opencode session diff observed'; then
  echo "empty OpenCode session diff produced a file event" >&2
  exit 1
fi

run_beacon endpoint hooks uninstall --harness opencode --user --log-path "$LOG_PATH" >/dev/null
test ! -f "$OPENCODE_PLUGIN"

echo "Uninstalling endpoint config..."
run_beacon endpoint uninstall --user --log-path "$LOG_PATH" --keep-logs >/dev/null

if [ -f "$HOME_DIR/.beacon/endpoint/config.json" ]; then
  echo "endpoint config was not removed by uninstall" >&2
  exit 1
fi

test -f "$LOG_PATH"

echo "Beacon endpoint smoke test passed."
