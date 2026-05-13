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
  --otlp-http-port 55318 \
  --content-retention metadata

test -f "$HOME_DIR/.beacon/endpoint/config.json"
test -f "$HOME_DIR/.beacon/endpoint/otelcol.yaml"
test -f "$HOME_DIR/Library/LaunchAgents/com.beacon.endpoint.collector.user.plist"
test -f "$LOG_PATH"

echo "Checking endpoint status..."
run_beacon endpoint status --user --log-path "$LOG_PATH" >/dev/null

echo "Writing Wazuh validation event..."
run_beacon endpoint wazuh validate --user --log-path "$LOG_PATH" >/dev/null

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

echo "Uninstalling endpoint config..."
run_beacon endpoint uninstall --user --log-path "$LOG_PATH" --keep-logs >/dev/null

if [ -f "$HOME_DIR/.beacon/endpoint/config.json" ]; then
  echo "endpoint config was not removed by uninstall" >&2
  exit 1
fi

test -f "$LOG_PATH"

echo "Beacon endpoint smoke test passed."
