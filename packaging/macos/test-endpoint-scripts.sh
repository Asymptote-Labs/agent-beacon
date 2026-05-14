#!/bin/sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
INSTALL_SCRIPT="$ROOT_DIR/packaging/macos/install-endpoint.sh"
UNINSTALL_SCRIPT="$ROOT_DIR/packaging/macos/uninstall-endpoint.sh"
PKG_BUILD_SCRIPT="$ROOT_DIR/packaging/macos/build-pkg.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

sh -n "$INSTALL_SCRIPT"
sh -n "$UNINSTALL_SCRIPT"
sh -n "$PKG_BUILD_SCRIPT"
for script in "$ROOT_DIR"/packaging/macos/scripts/* "$ROOT_DIR"/packaging/macos/jamf/scripts/*.sh "$ROOT_DIR"/packaging/macos/jamf/extension-attributes/*.sh; do
  sh -n "$script"
done

STUB_BIN="$TMP_DIR/beacon-stub"
STUB_LOG="$TMP_DIR/argv.log"
cat >"$STUB_BIN" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" > "$STUB_LOG"
STUB
chmod +x "$STUB_BIN"

BEACON_BIN="$STUB_BIN" \
BEACON_ENDPOINT_HARNESSES="claude,codex,cursor" \
BEACON_CONTENT_RETENTION="redacted" \
BEACON_OTLP_GRPC_PORT="5317" \
BEACON_OTLP_HTTP_PORT="5318" \
BEACON_COLLECTOR="/tmp/beacon-otelcol" \
STUB_LOG="$STUB_LOG" \
"$INSTALL_SCRIPT"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --harness claude,codex,cursor --content-retention redacted --otlp-grpc-port 5317 --otlp-http-port 5318 --collector /tmp/beacon-otelcol") ;;
  *)
    echo "unexpected install args: $INSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
STUB_LOG="$STUB_LOG" \
"$INSTALL_SCRIPT" _ _ _ "claude" "metadata" "6317" "6318" "/tmp/jamf-otelcol" "1"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --harness claude --content-retention metadata --otlp-grpc-port 6317 --otlp-http-port 6318 --collector /tmp/jamf-otelcol --no-start") ;;
  *)
    echo "unexpected Jamf positional install args: $INSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
BEACON_KEEP_LOGS="1" \
BEACON_KEEP_CONFIG="1" \
STUB_LOG="$STUB_LOG" \
"$UNINSTALL_SCRIPT"

UNINSTALL_ARGS="$(cat "$STUB_LOG")"
case "$UNINSTALL_ARGS" in
  "endpoint uninstall --keep-logs --keep-config") ;;
  *)
    echo "unexpected uninstall args with keep logs: $UNINSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
STUB_LOG="$STUB_LOG" \
"$UNINSTALL_SCRIPT" _ _ _ "true" "true"

UNINSTALL_ARGS="$(cat "$STUB_LOG")"
case "$UNINSTALL_ARGS" in
  "endpoint uninstall --keep-logs --keep-config") ;;
  *)
    echo "unexpected Jamf positional uninstall args: $UNINSTALL_ARGS" >&2
    exit 1
    ;;
esac

CONFIG_PATH="$TMP_DIR/config.json"
cat >"$CONFIG_PATH" <<'JSON'
{
  "harnesses": [
    "claude",
    "codex"
  ],
  "content_retention": "metadata"
}
JSON

RETENTION="$(BEACON_ENDPOINT_CONFIG="$CONFIG_PATH" "$ROOT_DIR/packaging/macos/jamf/extension-attributes/content-retention.sh")"
case "$RETENTION" in
  "<result>metadata</result>") ;;
  *)
    echo "unexpected retention extension attribute result: $RETENTION" >&2
    exit 1
    ;;
esac

HARNESSES="$(BEACON_ENDPOINT_CONFIG="$CONFIG_PATH" "$ROOT_DIR/packaging/macos/jamf/extension-attributes/configured-harnesses.sh")"
case "$HARNESSES" in
  "<result>claude,codex</result>") ;;
  *)
    echo "unexpected harness extension attribute result: $HARNESSES" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
STUB_LOG="$STUB_LOG" \
"$UNINSTALL_SCRIPT"

UNINSTALL_ARGS="$(cat "$STUB_LOG")"
case "$UNINSTALL_ARGS" in
  "endpoint uninstall") ;;
  *)
    echo "unexpected uninstall args without keep logs: $UNINSTALL_ARGS" >&2
    exit 1
    ;;
esac
