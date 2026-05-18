#!/bin/sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
INSTALL_SCRIPT="$ROOT_DIR/packaging/macos/install-endpoint.sh"
UNINSTALL_SCRIPT="$ROOT_DIR/packaging/macos/uninstall-endpoint.sh"
PKG_BUILD_SCRIPT="$ROOT_DIR/packaging/macos/build-pkg.sh"
REPAIR_SCRIPT="$ROOT_DIR/packaging/macos/jamf/scripts/repair.sh"
FLEET_REPAIR_SCRIPT="$ROOT_DIR/packaging/macos/fleet/scripts/repair.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

sh -n "$INSTALL_SCRIPT"
sh -n "$UNINSTALL_SCRIPT"
sh -n "$PKG_BUILD_SCRIPT"
for script in "$ROOT_DIR"/packaging/macos/scripts/* "$ROOT_DIR"/packaging/macos/jamf/scripts/*.sh "$ROOT_DIR"/packaging/macos/jamf/extension-attributes/*.sh "$ROOT_DIR"/packaging/macos/fleet/scripts/*.sh; do
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
BEACON_OTLP_GRPC_PORT="5317" \
BEACON_OTLP_HTTP_PORT="5318" \
BEACON_COLLECTOR="/tmp/beacon-otelcol" \
BEACON_SPLUNK_HEC_ENDPOINT="https://splunk.example:8088/services/collector" \
BEACON_SPLUNK_HEC_TOKEN="hec-token" \
BEACON_SPLUNK_INDEX="beacon" \
BEACON_SPLUNK_SOURCE="beacon-source" \
BEACON_SPLUNK_SOURCETYPE="beacon:sourcetype" \
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="1" \
BEACON_SPLUNK_CA_FILE="/tmp/splunk-ca.pem" \
STUB_LOG="$STUB_LOG" \
"$INSTALL_SCRIPT"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --system --harness claude,codex,cursor --content-retention full --otlp-grpc-port 5317 --otlp-http-port 5318 --collector /tmp/beacon-otelcol --splunk-hec-endpoint https://splunk.example:8088/services/collector --splunk-hec-token hec-token --splunk-index beacon --splunk-source beacon-source --splunk-sourcetype beacon:sourcetype --splunk-insecure-skip-verify --splunk-ca-file /tmp/splunk-ca.pem") ;;
  *)
    echo "unexpected install args: $INSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
BEACON_COLLECTOR="/tmp/beacon-otelcol" \
BEACON_SPLUNK_HEC_ENDPOINT="https://splunk.example:8088/services/collector" \
BEACON_SPLUNK_HEC_TOKEN="hec-token" \
STUB_LOG="$STUB_LOG" \
"$REPAIR_SCRIPT"

REPAIR_ARGS="$(cat "$STUB_LOG")"
case "$REPAIR_ARGS" in
  "endpoint repair --collector /tmp/beacon-otelcol --harness claude,codex --content-retention full --otlp-grpc-port 4317 --otlp-http-port 4318 --splunk-hec-endpoint https://splunk.example:8088/services/collector --splunk-hec-token hec-token") ;;
  *)
    echo "unexpected repair args: $REPAIR_ARGS" >&2
    exit 1
    ;;
esac

FAKE_BIN="$TMP_DIR/fake-bin"
FAKE_HOME="$TMP_DIR/fake-home"
mkdir -p "$FAKE_BIN" "$FAKE_HOME"
cat >"$FAKE_BIN/stat" <<'STUB'
#!/bin/sh
printf 'alice\n'
STUB
cat >"$FAKE_BIN/dscl" <<'STUB'
#!/bin/sh
printf 'NFSHomeDirectory: %s\n' "$FAKE_HOME"
STUB
cat >"$FAKE_BIN/sudo" <<'STUB'
#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    -u)
      shift 2
      ;;
    *=*)
      shift
      ;;
    *)
      exec "$@"
      ;;
  esac
done
STUB
chmod +x "$FAKE_BIN/stat" "$FAKE_BIN/dscl" "$FAKE_BIN/sudo"

BEACON_BIN="$STUB_BIN" \
PATH="$FAKE_BIN:$PATH" \
FAKE_HOME="$FAKE_HOME" \
BEACON_HOOK_HARNESSES="cursor,factory" \
BEACON_HOOK_LEVEL="user" \
STUB_LOG="$STUB_LOG" \
"$ROOT_DIR/packaging/macos/jamf/scripts/install-cursor-hooks.sh"

HOOK_ARGS="$(cat "$STUB_LOG")"
case "$HOOK_ARGS" in
  "endpoint hooks install --harness cursor,factory --level user --log-path /var/log/beacon-agent/runtime.jsonl") ;;
  *)
    echo "unexpected hook install args: $HOOK_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
STUB_LOG="$STUB_LOG" \
"$INSTALL_SCRIPT" _ _ _ "claude" "metadata" "6317" "6318" "/tmp/jamf-otelcol" "1" "https://jamf-splunk.example:8088/services/collector" "jamf-token" "jamf-index" "jamf-source" "jamf:sourcetype" "true" "/tmp/jamf-ca.pem"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --system --harness claude --content-retention metadata --otlp-grpc-port 6317 --otlp-http-port 6318 --collector /tmp/jamf-otelcol --splunk-hec-endpoint https://jamf-splunk.example:8088/services/collector --splunk-hec-token jamf-token --splunk-index jamf-index --splunk-source jamf-source --splunk-sourcetype jamf:sourcetype --splunk-insecure-skip-verify --splunk-ca-file /tmp/jamf-ca.pem --no-start") ;;
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
  "endpoint uninstall --system --keep-logs --keep-config") ;;
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
  "endpoint uninstall --system --keep-logs --keep-config") ;;
  *)
    echo "unexpected Jamf positional uninstall args: $UNINSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
BEACON_INSTALL_SCRIPT="$INSTALL_SCRIPT" \
STUB_LOG="$STUB_LOG" \
"$ROOT_DIR/packaging/macos/fleet/scripts/install.sh" "cursor" "redacted" "7317" "7318" "/tmp/fleet-otelcol" "1" "https://fleet-splunk.example:8088/services/collector" "fleet-token" "fleet-index" "fleet-source" "fleet:sourcetype" "1" "/tmp/fleet-ca.pem"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --system --harness cursor --content-retention redacted --otlp-grpc-port 7317 --otlp-http-port 7318 --collector /tmp/fleet-otelcol --splunk-hec-endpoint https://fleet-splunk.example:8088/services/collector --splunk-hec-token fleet-token --splunk-index fleet-index --splunk-source fleet-source --splunk-sourcetype fleet:sourcetype --splunk-insecure-skip-verify --splunk-ca-file /tmp/fleet-ca.pem --no-start") ;;
  *)
    echo "unexpected Fleet positional install args: $INSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
BEACON_COLLECTOR="/tmp/beacon-otelcol" \
BEACON_SPLUNK_HEC_ENDPOINT="https://splunk.example:8088/services/collector" \
BEACON_SPLUNK_HEC_TOKEN="hec-token" \
BEACON_SPLUNK_INDEX="beacon" \
STUB_LOG="$STUB_LOG" \
"$FLEET_REPAIR_SCRIPT" "claude,cursor" "metadata" "8317" "8318"

REPAIR_ARGS="$(cat "$STUB_LOG")"
case "$REPAIR_ARGS" in
  "endpoint repair --collector /tmp/beacon-otelcol --harness claude,cursor --content-retention metadata --otlp-grpc-port 8317 --otlp-http-port 8318 --splunk-hec-endpoint https://splunk.example:8088/services/collector --splunk-hec-token hec-token --splunk-index beacon") ;;
  *)
    echo "unexpected Fleet positional repair args: $REPAIR_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
BEACON_KEEP_LOGS="1" \
BEACON_KEEP_CONFIG="1" \
BEACON_UNINSTALL_SCRIPT="$UNINSTALL_SCRIPT" \
STUB_LOG="$STUB_LOG" \
"$ROOT_DIR/packaging/macos/fleet/scripts/uninstall.sh"

UNINSTALL_ARGS="$(cat "$STUB_LOG")"
case "$UNINSTALL_ARGS" in
  "endpoint uninstall --system --keep-logs --keep-config") ;;
  *)
    echo "unexpected Fleet uninstall args with keep logs: $UNINSTALL_ARGS" >&2
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
  "content_retention": "full",
  "destinations": {
    "splunk_hec": {
      "enabled": true,
      "endpoint": "https://splunk.example:8088/services/collector",
      "token": "redacted"
    }
  }
}
JSON

RETENTION="$(BEACON_ENDPOINT_CONFIG="$CONFIG_PATH" "$ROOT_DIR/packaging/macos/jamf/extension-attributes/content-retention.sh")"
case "$RETENTION" in
  "<result>full</result>") ;;
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

SPLUNK_STATE="$(BEACON_ENDPOINT_CONFIG="$CONFIG_PATH" "$ROOT_DIR/packaging/macos/jamf/extension-attributes/splunk-hec-forwarding.sh")"
case "$SPLUNK_STATE" in
  "<result>configured</result>") ;;
  *)
    echo "unexpected Splunk HEC extension attribute result: $SPLUNK_STATE" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
STUB_LOG="$STUB_LOG" \
"$UNINSTALL_SCRIPT"

UNINSTALL_ARGS="$(cat "$STUB_LOG")"
case "$UNINSTALL_ARGS" in
  "endpoint uninstall --system") ;;
  *)
    echo "unexpected uninstall args without keep logs: $UNINSTALL_ARGS" >&2
    exit 1
    ;;
esac
