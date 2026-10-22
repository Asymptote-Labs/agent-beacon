#!/bin/sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
INSTALL_SCRIPT="$ROOT_DIR/packaging/macos/install-endpoint.sh"
UNINSTALL_SCRIPT="$ROOT_DIR/packaging/macos/uninstall-endpoint.sh"
PKG_BUILD_SCRIPT="$ROOT_DIR/packaging/macos/build-pkg.sh"
PKG_SIGN_NOTARIZE_SCRIPT="$ROOT_DIR/packaging/macos/build-signed-notarized-pkg.sh"
REPAIR_SCRIPT="$ROOT_DIR/packaging/macos/jamf/scripts/repair.sh"
FULL_CLEANUP_SCRIPT="$ROOT_DIR/packaging/macos/jamf/scripts/full-cleanup.sh"
FLEET_REPAIR_SCRIPT="$ROOT_DIR/packaging/macos/fleet/scripts/repair.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM
# The forwarder installers write their LaunchDaemon logs under /Library/Logs/Beacon; keep every
# invocation in this file inside the temp dir instead.
BEACON_FORWARDER_LOG_DIR="$TMP_DIR/forwarder-logs"
export BEACON_FORWARDER_LOG_DIR

sh -n "$INSTALL_SCRIPT"
sh -n "$UNINSTALL_SCRIPT"
sh -n "$PKG_BUILD_SCRIPT"
sh -n "$PKG_SIGN_NOTARIZE_SCRIPT"
for script in "$ROOT_DIR"/packaging/macos/scripts/* "$ROOT_DIR"/packaging/macos/jamf/scripts/*.sh "$ROOT_DIR"/packaging/macos/jamf/claude/*/*.sh "$ROOT_DIR"/packaging/macos/jamf/extension-attributes/*.sh "$ROOT_DIR"/packaging/macos/fleet/scripts/*.sh; do
  case "$script" in
    */repair-hooks.sh)
      bash -n "$script"
      ;;
    *)
      sh -n "$script"
      ;;
  esac
done
FULL_CLEANUP_BIN="$TMP_DIR/full-cleanup-bin"
FULL_CLEANUP_BEACON="$TMP_DIR/full-cleanup-beacon"
FULL_CLEANUP_ARGS="$TMP_DIR/full-cleanup-args"
FULL_CLEANUP_RECEIPT="$TMP_DIR/full-cleanup-receipt"
FULL_CLEANUP_RUNTIME="$TMP_DIR/full-cleanup-runtime"
FULL_CLEANUP_CONFIG="$TMP_DIR/full-cleanup-config"
FULL_CLEANUP_OPT="$TMP_DIR/full-cleanup-opt"
mkdir -p "$FULL_CLEANUP_BIN" "$FULL_CLEANUP_RUNTIME" "$FULL_CLEANUP_CONFIG" "$FULL_CLEANUP_OPT"
printf '%s\n' preserved >"$FULL_CLEANUP_RUNTIME/runtime.jsonl"
cat >"$FULL_CLEANUP_BEACON" <<'STUB'
#!/bin/sh
case "$1" in
  version) echo "beacon test" ;;
  endpoint) printf '%s\n' "$*" >"$FULL_CLEANUP_ARGS" ;;
  *) exit 1 ;;
esac
STUB
FULL_CLEANUP_LAUNCHCTL_LOG="$TMP_DIR/full-cleanup-launchctl"
cat >"$FULL_CLEANUP_BIN/launchctl" <<'STUB'
#!/bin/sh
[ -z "${FULL_CLEANUP_LAUNCHCTL_LOG:-}" ] || printf '%s\n' "$*" >>"$FULL_CLEANUP_LAUNCHCTL_LOG"
exit 1
STUB
cat >"$FULL_CLEANUP_BIN/stat" <<'STUB'
#!/bin/sh
case "$*" in
  *"/dev/console"*) echo loginwindow ;;
  *) /usr/bin/stat "$@" ;;
esac
STUB
cat >"$FULL_CLEANUP_BIN/pkgutil" <<'STUB'
#!/bin/sh
case "$1" in
  --pkg-info) [ "$2" = "ai.asymptote.beacon.endpoint" ] ;;
  --forget) printf '%s\n' "$2" >"$FULL_CLEANUP_RECEIPT" ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$FULL_CLEANUP_BEACON" "$FULL_CLEANUP_BIN/launchctl" "$FULL_CLEANUP_BIN/stat" "$FULL_CLEANUP_BIN/pkgutil"

PATH="$FULL_CLEANUP_BIN:$PATH" \
FULL_CLEANUP_ARGS="$FULL_CLEANUP_ARGS" \
FULL_CLEANUP_LAUNCHCTL_LOG="$FULL_CLEANUP_LAUNCHCTL_LOG" \
FULL_CLEANUP_RECEIPT="$FULL_CLEANUP_RECEIPT" \
BEACON_BIN="$FULL_CLEANUP_BEACON" \
BEACON_KEEP_LOGS=1 \
COLLECTOR_LABEL=com.test.beacon.collector \
UPDATER_LABEL=com.test.beacon.updater \
S3_FORWARDER_LABEL=com.test.beacon.s3 \
GCS_FORWARDER_LABEL=com.test.beacon.gcs \
FALCON_FORWARDER_LABEL=com.test.beacon.falcon \
RUNTIME_DIR="$FULL_CLEANUP_RUNTIME" \
SYSTEM_CONFIG_DIR="$FULL_CLEANUP_CONFIG" \
OPT_BEACON_DIR="$FULL_CLEANUP_OPT" \
"$FULL_CLEANUP_SCRIPT" >/dev/null 2>&1
if ! grep -q -- '--keep-logs' "$FULL_CLEANUP_ARGS"; then
  echo "full cleanup should pass --keep-logs to endpoint uninstall" >&2
  exit 1
fi
if [ ! -f "$FULL_CLEANUP_RUNTIME/runtime.jsonl" ]; then
  echo "full cleanup should preserve the runtime directory when requested" >&2
  exit 1
fi
if [ "$(cat "$FULL_CLEANUP_RECEIPT")" != "ai.asymptote.beacon.endpoint" ]; then
  echo "full cleanup should forget only the known Beacon package receipt" >&2
  exit 1
fi
for label in com.test.beacon.collector com.test.beacon.updater com.test.beacon.s3 com.test.beacon.gcs com.test.beacon.falcon; do
  if ! grep -qx "bootout system/$label" "$FULL_CLEANUP_LAUNCHCTL_LOG"; then
    echo "full cleanup should boot out $label" >&2
    exit 1
  fi
done

FULL_CLEANUP_USER_BIN="$TMP_DIR/full-cleanup-user-bin"
FULL_CLEANUP_HOME="$TMP_DIR/full-cleanup-home"
FULL_CLEANUP_USER_CONFIG="$TMP_DIR/full-cleanup-user-config"
FULL_CLEANUP_USER_OPT="$TMP_DIR/full-cleanup-user-opt"
FULL_CLEANUP_SENTINEL="$TMP_DIR/full-cleanup-symlink-target"
mkdir -p \
  "$FULL_CLEANUP_USER_BIN" \
  "$FULL_CLEANUP_HOME/.claude" \
  "$FULL_CLEANUP_HOME/.codex" \
  "$FULL_CLEANUP_HOME/.cursor" \
  "$FULL_CLEANUP_USER_CONFIG/Endpoint" \
  "$FULL_CLEANUP_USER_OPT"
printf '%s\n' '{"collector":{"grpc_port":54317,"http_port":54318}}' \
  >"$FULL_CLEANUP_USER_CONFIG/Endpoint/config.json"
printf '%s\n' '{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"https://otel.example.com"}}' \
  >"$FULL_CLEANUP_HOME/.claude/settings.json"
cat >"$FULL_CLEANUP_HOME/.codex/config.toml" <<'EOF'
[otel]
endpoint = "http://127.0.0.1:54317"

[otel.other]
endpoint = "http://127.0.0.1:59999"
EOF
printf '%s\n' do-not-touch >"$FULL_CLEANUP_SENTINEL"
ln -s "$FULL_CLEANUP_SENTINEL" "$FULL_CLEANUP_HOME/.cursor/hooks.json"
cat >"$FULL_CLEANUP_USER_BIN/stat" <<'STUB'
#!/bin/sh
case "$*" in
  *"/dev/console"*) echo cleanupuser ;;
  *) /usr/bin/stat "$@" ;;
esac
STUB
cat >"$FULL_CLEANUP_USER_BIN/dscl" <<'STUB'
#!/bin/sh
printf 'NFSHomeDirectory: %s\n' "$FULL_CLEANUP_HOME"
STUB
cat >"$FULL_CLEANUP_USER_BIN/id" <<'STUB'
#!/bin/sh
case "$1" in
  -gn) echo staff ;;
  *) exit 0 ;;
esac
STUB
chmod +x "$FULL_CLEANUP_USER_BIN/stat" "$FULL_CLEANUP_USER_BIN/dscl" "$FULL_CLEANUP_USER_BIN/id"

PATH="$FULL_CLEANUP_USER_BIN:$FULL_CLEANUP_BIN:$PATH" \
FULL_CLEANUP_HOME="$FULL_CLEANUP_HOME" \
FULL_CLEANUP_ARGS="$FULL_CLEANUP_ARGS" \
FULL_CLEANUP_RECEIPT="$FULL_CLEANUP_RECEIPT" \
BEACON_BIN="$FULL_CLEANUP_BEACON" \
COLLECTOR_LABEL=com.test.user.beacon.collector \
UPDATER_LABEL=com.test.user.beacon.updater \
S3_FORWARDER_LABEL=com.test.user.beacon.s3 \
GCS_FORWARDER_LABEL=com.test.user.beacon.gcs \
FALCON_FORWARDER_LABEL=com.test.user.beacon.falcon \
RUNTIME_DIR="$TMP_DIR/full-cleanup-user-runtime" \
SYSTEM_CONFIG_DIR="$FULL_CLEANUP_USER_CONFIG" \
OPT_BEACON_DIR="$FULL_CLEANUP_USER_OPT" \
"$FULL_CLEANUP_SCRIPT" >/dev/null 2>&1
if ! grep -q 'https://otel.example.com' "$FULL_CLEANUP_HOME/.claude/settings.json"; then
  echo "full cleanup should preserve non-Beacon Claude OTLP settings" >&2
  exit 1
fi
if [ "$(cat "$FULL_CLEANUP_SENTINEL")" != "do-not-touch" ]; then
  echo "full cleanup should never follow user-controlled config symlinks" >&2
  exit 1
fi
if grep -q '54317' "$FULL_CLEANUP_HOME/.codex/config.toml" ||
   ! grep -q '59999' "$FULL_CLEANUP_HOME/.codex/config.toml"; then
  echo "full cleanup should remove only Codex OTLP blocks matching installed Beacon ports" >&2
  exit 1
fi

# Without BEACON_KEEP_LOGS the runtime logs go too, and Claude settings that point at the
# installed Beacon collector are removed rather than left aimed at a port nothing listens on.
FULL_CLEANUP_LOCAL_HOME="$TMP_DIR/full-cleanup-local-home"
FULL_CLEANUP_LOCAL_CONFIG="$TMP_DIR/full-cleanup-local-config"
FULL_CLEANUP_LOCAL_RUNTIME="$TMP_DIR/full-cleanup-local-runtime"
mkdir -p "$FULL_CLEANUP_LOCAL_HOME/.claude" "$FULL_CLEANUP_LOCAL_CONFIG/Endpoint" "$FULL_CLEANUP_LOCAL_RUNTIME"
printf '%s\n' '{"collector":{"grpc_port":54317,"http_port":54318}}' \
  >"$FULL_CLEANUP_LOCAL_CONFIG/Endpoint/config.json"
printf '%s\n' '{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:54318/","OTEL_LOGS_EXPORTER":"otlp","UNRELATED":"kept"}}' \
  >"$FULL_CLEANUP_LOCAL_HOME/.claude/settings.json"
printf '%s\n' secret-prompt >"$FULL_CLEANUP_LOCAL_RUNTIME/runtime.jsonl"
PATH="$FULL_CLEANUP_USER_BIN:$FULL_CLEANUP_BIN:$PATH" \
FULL_CLEANUP_HOME="$FULL_CLEANUP_LOCAL_HOME" \
FULL_CLEANUP_ARGS="$FULL_CLEANUP_ARGS" \
FULL_CLEANUP_RECEIPT="$FULL_CLEANUP_RECEIPT" \
BEACON_BIN="$FULL_CLEANUP_BEACON" \
COLLECTOR_LABEL=com.test.local.beacon.collector \
UPDATER_LABEL=com.test.local.beacon.updater \
S3_FORWARDER_LABEL=com.test.local.beacon.s3 \
GCS_FORWARDER_LABEL=com.test.local.beacon.gcs \
FALCON_FORWARDER_LABEL=com.test.local.beacon.falcon \
RUNTIME_DIR="$FULL_CLEANUP_LOCAL_RUNTIME" \
SYSTEM_CONFIG_DIR="$FULL_CLEANUP_LOCAL_CONFIG" \
OPT_BEACON_DIR="$TMP_DIR/full-cleanup-local-opt" \
"$FULL_CLEANUP_SCRIPT" >/dev/null 2>&1
if grep -q -e OTEL_EXPORTER_OTLP_ENDPOINT -e CLAUDE_CODE_ENABLE_TELEMETRY -e OTEL_LOGS_EXPORTER "$FULL_CLEANUP_LOCAL_HOME/.claude/settings.json" ||
   ! grep -q '"UNRELATED": "kept"' "$FULL_CLEANUP_LOCAL_HOME/.claude/settings.json"; then
  echo "full cleanup should remove only the Claude OTLP settings that point at the installed Beacon collector" >&2
  exit 1
fi
if [ "$(cat "$FULL_CLEANUP_ARGS")" != "endpoint uninstall --system" ]; then
  echo "full cleanup without BEACON_KEEP_LOGS should run a plain endpoint uninstall, got: $(cat "$FULL_CLEANUP_ARGS")" >&2
  exit 1
fi
if [ -e "$FULL_CLEANUP_LOCAL_RUNTIME" ]; then
  echo "full cleanup without BEACON_KEEP_LOGS should remove the runtime logs" >&2
  exit 1
fi

PKG_TEST_BIN="$TMP_DIR/pkg-bin"
PKG_TEST_ROOT="$TMP_DIR/pkg-root"
PKG_TEST_OUT="$TMP_DIR/pkg-out"
PKG_TEST_LOG="$TMP_DIR/pkgbuild.log"
PKG_CODESIGN_LOG="$TMP_DIR/codesign.log"
mkdir -p "$PKG_TEST_BIN" "$PKG_TEST_ROOT/cli/beacon" "$PKG_TEST_ROOT/collector-builder/dist/beacon-otelcol/darwin_arm64" "$PKG_TEST_OUT"
cat >"$PKG_TEST_ROOT/cli/beacon/beacon" <<'STUB'
#!/bin/sh
echo beacon
STUB
cat >"$PKG_TEST_ROOT/collector-builder/dist/beacon-otelcol/darwin_arm64/beacon-otelcol" <<'STUB'
#!/bin/sh
echo beacon-otelcol
STUB
cat >"$PKG_TEST_ROOT/vector" <<'STUB'
#!/bin/sh
echo vector
STUB
chmod +x "$PKG_TEST_ROOT/cli/beacon/beacon" "$PKG_TEST_ROOT/collector-builder/dist/beacon-otelcol/darwin_arm64/beacon-otelcol" "$PKG_TEST_ROOT/vector"
cat >"$PKG_TEST_BIN/git" <<'STUB'
#!/bin/sh
echo test-version
STUB
cat >"$PKG_TEST_BIN/go" <<'STUB'
#!/bin/sh
case "$1 $2" in
  "env GOOS")
    echo darwin
    ;;
  "env GOARCH")
    echo arm64
    ;;
  *)
    exit 1
    ;;
esac
STUB
cat >"$PKG_TEST_BIN/pkgbuild" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" > "$PKG_TEST_LOG"
out=""
for arg in "$@"; do
  out="$arg"
done
mkdir -p "$(dirname "$out")"
printf 'pkg\n' > "$out"
STUB
cat >"$PKG_TEST_BIN/codesign" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$PKG_CODESIGN_LOG"
STUB
chmod +x "$PKG_TEST_BIN/git" "$PKG_TEST_BIN/go" "$PKG_TEST_BIN/pkgbuild" "$PKG_TEST_BIN/codesign"

PATH="$PKG_TEST_BIN:$PATH" \
OUT_DIR="$PKG_TEST_OUT" \
BEACON_BIN="$PKG_TEST_ROOT/cli/beacon/beacon" \
BEACON_COLLECTOR="$PKG_TEST_ROOT/collector-builder/dist/beacon-otelcol/darwin_arm64/beacon-otelcol" \
BEACON_VECTOR_BIN="$PKG_TEST_ROOT/vector" \
BEACON_APP_SIGN_IDENTITY="Developer ID Application: Example Corp (TEAMID)" \
PKG_SIGN_IDENTITY="Developer ID Installer: Example Corp (TEAMID)" \
PKG_TEST_LOG="$PKG_TEST_LOG" \
PKG_CODESIGN_LOG="$PKG_CODESIGN_LOG" \
"$PKG_BUILD_SCRIPT" >/dev/null

if [ "$(wc -l < "$PKG_CODESIGN_LOG" | tr -d ' ')" != "3" ]; then
  echo "expected codesign to sign beacon, beacon-otelcol, and vector" >&2
  exit 1
fi
if ! grep -q -- '--options runtime' "$PKG_CODESIGN_LOG"; then
  echo "codesign missing hardened runtime option" >&2
  exit 1
fi
if ! grep -q -- '--timestamp' "$PKG_CODESIGN_LOG"; then
  echo "codesign missing timestamp option" >&2
  exit 1
fi
if ! grep -q 'Developer ID Application: Example Corp (TEAMID)' "$PKG_CODESIGN_LOG"; then
  echo "codesign missing app signing identity" >&2
  exit 1
fi
if ! grep -q 'Developer ID Installer: Example Corp (TEAMID)' "$PKG_TEST_LOG"; then
  echo "pkgbuild missing installer signing identity" >&2
  exit 1
fi

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
BEACON_FALCON_HEC_ENDPOINT="https://cloud.us.humio.com/api/v1/ingest/hec" \
BEACON_FALCON_HEC_TOKEN="falcon-token" \
BEACON_FALCON_INDEX="beacon-repo" \
BEACON_FALCON_SOURCE="falcon-source" \
BEACON_FALCON_SOURCETYPE="json" \
BEACON_FALCON_INSECURE_SKIP_VERIFY="1" \
BEACON_FALCON_CA_FILE="/tmp/falcon-ca.pem" \
STUB_LOG="$STUB_LOG" \
"$INSTALL_SCRIPT"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --system --harness claude,codex,cursor --otlp-grpc-port 5317 --otlp-http-port 5318 --collector /tmp/beacon-otelcol --splunk-hec-endpoint https://splunk.example:8088/services/collector --splunk-hec-token hec-token --splunk-index beacon --splunk-source beacon-source --splunk-sourcetype beacon:sourcetype --splunk-insecure-skip-verify --splunk-ca-file /tmp/splunk-ca.pem --falcon-hec-endpoint https://cloud.us.humio.com/api/v1/ingest/hec --falcon-hec-token falcon-token --falcon-index beacon-repo --falcon-source falcon-source --falcon-sourcetype json --falcon-insecure-skip-verify --falcon-ca-file /tmp/falcon-ca.pem") ;;
  *)
    echo "unexpected install args: $INSTALL_ARGS" >&2
    exit 1
    ;;
esac

BEACON_BIN="$STUB_BIN" \
BEACON_COLLECTOR="/tmp/beacon-otelcol" \
BEACON_SPLUNK_HEC_ENDPOINT="https://splunk.example:8088/services/collector" \
BEACON_SPLUNK_HEC_TOKEN="hec-token" \
BEACON_FALCON_HEC_ENDPOINT="https://cloud.us.humio.com/api/v1/ingest/hec" \
BEACON_FALCON_HEC_TOKEN="falcon-token" \
STUB_LOG="$STUB_LOG" \
"$REPAIR_SCRIPT"

REPAIR_ARGS="$(cat "$STUB_LOG")"
case "$REPAIR_ARGS" in
  "endpoint repair --collector /tmp/beacon-otelcol --harness claude,codex --otlp-grpc-port 4317 --otlp-http-port 4318 --splunk-hec-endpoint https://splunk.example:8088/services/collector --splunk-hec-token hec-token --falcon-hec-endpoint https://cloud.us.humio.com/api/v1/ingest/hec --falcon-hec-token falcon-token") ;;
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
"$INSTALL_SCRIPT" _ _ _ "claude" "6317" "6318" "/tmp/jamf-otelcol" "1" "https://jamf-splunk.example:8088/services/collector" "jamf-token" "jamf-index" "jamf-source" "jamf:sourcetype" "true" "/tmp/jamf-ca.pem"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --system --harness claude --otlp-grpc-port 6317 --otlp-http-port 6318 --collector /tmp/jamf-otelcol --splunk-hec-endpoint https://jamf-splunk.example:8088/services/collector --splunk-hec-token jamf-token --splunk-index jamf-index --splunk-source jamf-source --splunk-sourcetype jamf:sourcetype --splunk-insecure-skip-verify --splunk-ca-file /tmp/jamf-ca.pem --no-start") ;;
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
"$ROOT_DIR/packaging/macos/fleet/scripts/install.sh" "cursor" "7317" "7318" "/tmp/fleet-otelcol" "1" "https://fleet-splunk.example:8088/services/collector" "fleet-token" "fleet-index" "fleet-source" "fleet:sourcetype" "1" "/tmp/fleet-ca.pem"

INSTALL_ARGS="$(cat "$STUB_LOG")"
case "$INSTALL_ARGS" in
  "endpoint install --system --harness cursor --otlp-grpc-port 7317 --otlp-http-port 7318 --collector /tmp/fleet-otelcol --splunk-hec-endpoint https://fleet-splunk.example:8088/services/collector --splunk-hec-token fleet-token --splunk-index fleet-index --splunk-source fleet-source --splunk-sourcetype fleet:sourcetype --splunk-insecure-skip-verify --splunk-ca-file /tmp/fleet-ca.pem --no-start") ;;
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
BEACON_FALCON_HEC_ENDPOINT="https://cloud.us.humio.com/api/v1/ingest/hec" \
BEACON_FALCON_HEC_TOKEN="falcon-token" \
BEACON_FALCON_INDEX="beacon-repo" \
STUB_LOG="$STUB_LOG" \
"$FLEET_REPAIR_SCRIPT" "claude,cursor" "8317" "8318"

REPAIR_ARGS="$(cat "$STUB_LOG")"
case "$REPAIR_ARGS" in
  "endpoint repair --collector /tmp/beacon-otelcol --harness claude,cursor --otlp-grpc-port 8317 --otlp-http-port 8318 --splunk-hec-endpoint https://splunk.example:8088/services/collector --splunk-hec-token hec-token --splunk-index beacon --falcon-hec-endpoint https://cloud.us.humio.com/api/v1/ingest/hec --falcon-hec-token falcon-token --falcon-index beacon-repo") ;;
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
  "destinations": {
    "splunk_hec": {
      "enabled": true,
      "endpoint": "https://splunk.example:8088/services/collector",
      "token": "redacted"
    }
  }
}
JSON

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

FAKE_VECTOR="$TMP_DIR/vector"
cat >"$FAKE_VECTOR" <<'STUB'
#!/bin/sh
case "$1" in
  --version)
    echo "vector 0.56.0"
    ;;
  validate)
    exit 0
    ;;
  *)
    echo "unexpected vector args: $*" >&2
    exit 1
    ;;
esac
STUB
chmod +x "$FAKE_VECTOR"

FORWARDER_BASE="$TMP_DIR/forwarders"
LAUNCHDAEMONS_DIR="$TMP_DIR/launchdaemons"
mkdir -p "$LAUNCHDAEMONS_DIR"
BEACON_VECTOR_BIN="$FAKE_VECTOR" \
BEACON_FALCON_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/falcon/run-forwarder.sh" \
BEACON_FORWARDER_BASE_DIR="$FORWARDER_BASE" \
BEACON_LAUNCHDAEMONS_DIR="$LAUNCHDAEMONS_DIR" \
BEACON_NO_START="1" \
"$ROOT_DIR/packaging/macos/jamf/claude/falcon/install-forwarder.sh" _ _ _ "https://falcon.example/services/collector" "falcon-token" "beacon-source" "json" "beacon-index" "$TMP_DIR/runtime.jsonl,/Users/*/.beacon/endpoint/logs/runtime.jsonl" "end" >/dev/null

if ! grep -q 'https://falcon.example/services/collector' "$FORWARDER_BASE/falcon-vector.env"; then
  echo "Falcon Vector env missing endpoint" >&2
  exit 1
fi
if grep -q 'falcon-token' "$FORWARDER_BASE/falcon-vector.toml"; then
  echo "Falcon Vector config should not contain token" >&2
  exit 1
fi
if ! grep -q 'sinks.falcon_hec' "$FORWARDER_BASE/falcon-vector.toml"; then
  echo "Falcon Vector config missing sink" >&2
  exit 1
fi
if ! grep -q 'RunAtLoad' "$LAUNCHDAEMONS_DIR/com.beacon.endpoint.falcon-forwarder.plist"; then
  echo "Falcon Vector plist missing RunAtLoad" >&2
  exit 1
fi
if ! grep -q "<string>$BEACON_FORWARDER_LOG_DIR/com.beacon.endpoint.falcon-forwarder.err</string>" "$LAUNCHDAEMONS_DIR/com.beacon.endpoint.falcon-forwarder.plist" \
  || grep -q '/tmp/com.beacon' "$LAUNCHDAEMONS_DIR/com.beacon.endpoint.falcon-forwarder.plist"; then
  echo "Falcon Vector plist should log under the forwarder log dir, not /tmp" >&2
  exit 1
fi
case "$(ls -ld "$BEACON_FORWARDER_LOG_DIR" | awk '{print $1}')" in
  drwxr-xr-x*) ;;
  *)
    echo "forwarder log dir should be 0755" >&2
    exit 1
    ;;
esac

FALCON_VECTOR_STATE="$(BEACON_FORWARDER_BASE_DIR="$FORWARDER_BASE" BEACON_LAUNCHDAEMONS_DIR="$LAUNCHDAEMONS_DIR" "$ROOT_DIR/packaging/macos/jamf/extension-attributes/falcon-vector-forwarding-configured.sh")"
case "$FALCON_VECTOR_STATE" in
  "<result>configured</result>") ;;
  *)
    echo "unexpected Falcon Vector configured extension attribute result: $FALCON_VECTOR_STATE" >&2
    exit 1
    ;;
esac

FAKE_REPAIR="$TMP_DIR/fake-repair-fails.sh"
FAKE_FORWARDER="$TMP_DIR/fake-forwarder-runs.sh"
FORWARDER_MARKER="$TMP_DIR/forwarder-ran"
cat >"$FAKE_REPAIR" <<'STUB'
#!/bin/sh
exit 7
STUB
cat >"$FAKE_FORWARDER" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" > "$FORWARDER_MARKER"
STUB
chmod +x "$FAKE_REPAIR" "$FAKE_FORWARDER"

BEACON_REPAIR_HOOKS_SCRIPT="$FAKE_REPAIR" \
BEACON_FALCON_VECTOR_SCRIPT="$FAKE_FORWARDER" \
BEACON_FALCON_HEC_ENDPOINT="https://falcon.example/services/collector" \
BEACON_FALCON_HEC_TOKEN="falcon-token" \
FORWARDER_MARKER="$FORWARDER_MARKER" \
"$ROOT_DIR/packaging/macos/jamf/claude/falcon/repair-hooks-and-forwarder.sh" >/dev/null 2>&1 && {
  echo "combined Falcon Vector repair should return the failing repair status" >&2
  exit 1
}
if [ ! -f "$FORWARDER_MARKER" ]; then
  echo "combined Falcon Vector repair should run forwarder before failing repair" >&2
  exit 1
fi

S3_FORWARDER_BASE="$TMP_DIR/s3-forwarders"
S3_LAUNCHDAEMONS_DIR="$TMP_DIR/s3-launchdaemons"
mkdir -p "$S3_LAUNCHDAEMONS_DIR"
# A symlink planted where the daemon's log goes must be removed, never followed.
S3_LOG_VICTIM="$TMP_DIR/s3-log-victim"
printf '%s\n' keep >"$S3_LOG_VICTIM"
mkdir -p "$BEACON_FORWARDER_LOG_DIR"
ln -sf "$S3_LOG_VICTIM" "$BEACON_FORWARDER_LOG_DIR/com.beacon.endpoint.s3-forwarder.err"
BEACON_VECTOR_BIN="$FAKE_VECTOR" \
BEACON_S3_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/s3/run-forwarder.sh" \
BEACON_FORWARDER_BASE_DIR="$S3_FORWARDER_BASE" \
BEACON_LAUNCHDAEMONS_DIR="$S3_LAUNCHDAEMONS_DIR" \
BEACON_RUNTIME_LOG_PATHS="$TMP_DIR/s3-runtime.jsonl" \
BEACON_NO_START="1" \
AWS_ACCESS_KEY_ID="test-access-key" \
AWS_SECRET_ACCESS_KEY="test-secret-key" \
AWS_SESSION_TOKEN="test-session-token" \
AWS_PROFILE="test-profile" \
AWS_SHARED_CREDENTIALS_FILE="/tmp/test-aws-credentials" \
AWS_CONFIG_FILE="/tmp/test-aws-config" \
AWS_WEB_IDENTITY_TOKEN_FILE="/tmp/test-web-identity-token" \
AWS_ROLE_ARN="arn:aws:iam::123456789012:role/beacon-demo" \
"$ROOT_DIR/packaging/macos/jamf/claude/s3/install-forwarder.sh" _ _ _ "beacon-test-bucket" "us-west-2" "beacon/claude/runtime/" "STANDARD_IA" "end" >/dev/null

if [ -L "$BEACON_FORWARDER_LOG_DIR/com.beacon.endpoint.s3-forwarder.err" ]; then
  echo "S3 forwarder installer should remove a symlink planted at its log path" >&2
  exit 1
fi
if [ "$(cat "$S3_LOG_VICTIM")" != "keep" ]; then
  echo "S3 forwarder installer touched the planted symlink's target" >&2
  exit 1
fi
if ! grep -q "<string>$BEACON_FORWARDER_LOG_DIR/com.beacon.endpoint.s3-forwarder.out</string>" "$S3_LAUNCHDAEMONS_DIR/com.beacon.endpoint.s3-forwarder.plist"; then
  echo "S3 Vector plist should log under the forwarder log dir" >&2
  exit 1
fi
if ! grep -q 'sinks.beacon_runtime_s3' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing sink" >&2
  exit 1
fi
if ! grep -q 'sinks.beacon_inventory_s3' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing inventory sink" >&2
  exit 1
fi
if ! grep -q "$TMP_DIR/s3-runtime.jsonl" "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing runtime log path" >&2
  exit 1
fi
if ! grep -q "$TMP_DIR/inventory_state.jsonl" "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing derived inventory log path" >&2
  exit 1
fi
if ! grep -q 'key_prefix = "${BEACON_S3_PREFIX:-beacon}/runtime/date=%F/"' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing runtime prefix" >&2
  exit 1
fi
if ! grep -q 'key_prefix = "${BEACON_S3_PREFIX:-beacon}/inventory/date=%F/"' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing inventory prefix" >&2
  exit 1
fi
if ! grep -q 'read_from = "beginning"' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config should read inventory log from beginning" >&2
  exit 1
fi
if ! grep -q 'compression = "gzip"' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing gzip compression" >&2
  exit 1
fi
if ! grep -q 'filename_append_uuid = true' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config missing non-overwriting object keys" >&2
  exit 1
fi
if ! grep -q 'bucket = "${BEACON_S3_BUCKET}"' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config should reference bucket env var" >&2
  exit 1
fi
if grep -q 'beacon-test-bucket\|test-access-key\|test-secret-key\|test-session-token' "$S3_FORWARDER_BASE/s3-vector.toml"; then
  echo "S3 Vector config should not contain bucket values or AWS credentials" >&2
  exit 1
fi
if ! grep -q "beacon-test-bucket" "$S3_FORWARDER_BASE/s3-vector.env"; then
  echo "S3 Vector env missing bucket" >&2
  exit 1
fi
if ! grep -q "us-west-2" "$S3_FORWARDER_BASE/s3-vector.env"; then
  echo "S3 Vector env missing region" >&2
  exit 1
fi
if ! grep -q "BEACON_S3_PREFIX='beacon/claude'" "$S3_FORWARDER_BASE/s3-vector.env"; then
  echo "S3 Vector env should normalize old runtime suffix from prefix" >&2
  exit 1
fi
for expected in \
  "AWS_ACCESS_KEY_ID='test-access-key'" \
  "AWS_SECRET_ACCESS_KEY='test-secret-key'" \
  "AWS_SESSION_TOKEN='test-session-token'" \
  "AWS_PROFILE='test-profile'" \
  "AWS_SHARED_CREDENTIALS_FILE='/tmp/test-aws-credentials'" \
  "AWS_CONFIG_FILE='/tmp/test-aws-config'" \
  "AWS_WEB_IDENTITY_TOKEN_FILE='/tmp/test-web-identity-token'" \
  "AWS_ROLE_ARN='arn:aws:iam::123456789012:role/beacon-demo'"
do
  if ! grep -q "$expected" "$S3_FORWARDER_BASE/s3-vector.env"; then
    echo "S3 Vector env missing AWS provider setting: $expected" >&2
    exit 1
  fi
done
case "$(ls -l "$S3_FORWARDER_BASE/s3-vector.env" | awk '{print $1}')" in
  -rw-------*) ;;
  *)
    echo "S3 Vector env should be 0600" >&2
    exit 1
    ;;
esac
if ! grep -q 'RunAtLoad' "$S3_LAUNCHDAEMONS_DIR/com.beacon.endpoint.s3-forwarder.plist"; then
  echo "S3 Vector plist missing RunAtLoad" >&2
  exit 1
fi
# Hooks append to the runtime log as the console user, so an installer that has to create it
# creates it 0666, the mode Beacon's writer uses.
case "$(ls -l "$TMP_DIR/s3-runtime.jsonl" | awk '{print $1}')" in
  -rw-rw-rw-*) ;;
  *)
    echo "S3 forwarder installer should create a missing runtime log as 0666" >&2
    exit 1
    ;;
esac

S3_FAKE_REPAIR="$TMP_DIR/fake-s3-repair-fails.sh"
S3_FAKE_FORWARDER="$TMP_DIR/fake-s3-forwarder-runs.sh"
S3_FORWARDER_MARKER="$TMP_DIR/s3-forwarder-ran"
cat >"$S3_FAKE_REPAIR" <<'STUB'
#!/bin/sh
exit 9
STUB
cat >"$S3_FAKE_FORWARDER" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" > "$S3_FORWARDER_MARKER"
STUB
chmod +x "$S3_FAKE_REPAIR" "$S3_FAKE_FORWARDER"

BEACON_REPAIR_HOOKS_SCRIPT="$S3_FAKE_REPAIR" \
BEACON_S3_VECTOR_SCRIPT="$S3_FAKE_FORWARDER" \
BEACON_S3_BUCKET="beacon-test-bucket" \
AWS_REGION="us-west-2" \
S3_FORWARDER_MARKER="$S3_FORWARDER_MARKER" \
"$ROOT_DIR/packaging/macos/jamf/claude/s3/repair-hooks-and-forwarder.sh" >/dev/null 2>&1 && {
  echo "combined S3 Vector repair should return the failing repair status" >&2
  exit 1
}
if [ ! -f "$S3_FORWARDER_MARKER" ]; then
  echo "combined S3 Vector repair should run forwarder before failing repair" >&2
  exit 1
fi

GCS_FORWARDER_BASE="$TMP_DIR/gcs-forwarders"
GCS_LAUNCHDAEMONS_DIR="$TMP_DIR/gcs-launchdaemons"
GCS_CREDENTIALS="$TMP_DIR/gcs-service-account.json"
mkdir -p "$GCS_LAUNCHDAEMONS_DIR"
printf '%s\n' '{"type":"service_account","project_id":"beacon-test"}' >"$GCS_CREDENTIALS"
chmod 0600 "$GCS_CREDENTIALS"
# The collector already created this log 0666. The installer used to chmod it to 0644, which
# locked out hooks running as the console user until the next root write restored the mode.
: >"$TMP_DIR/gcs-runtime.jsonl"
chmod 0666 "$TMP_DIR/gcs-runtime.jsonl"

BEACON_VECTOR_BIN="$FAKE_VECTOR" \
BEACON_GCS_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/gcs/run-forwarder.sh" \
BEACON_FORWARDER_BASE_DIR="$GCS_FORWARDER_BASE" \
BEACON_LAUNCHDAEMONS_DIR="$GCS_LAUNCHDAEMONS_DIR" \
BEACON_RUNTIME_LOG_PATHS="$TMP_DIR/gcs-runtime.jsonl" \
BEACON_NO_START="1" \
GOOGLE_APPLICATION_CREDENTIALS="$GCS_CREDENTIALS" \
GOOGLE_CLOUD_PROJECT="beacon-test" \
"$ROOT_DIR/packaging/macos/jamf/claude/gcs/install-forwarder.sh" _ _ _ "beacon-gcs-test-bucket" "beacon/claude/runtime/" "NEARLINE" "end" >/dev/null
case "$(ls -l "$TMP_DIR/gcs-runtime.jsonl" | awk '{print $1}')" in
  -rw-rw-rw-*) ;;
  *)
    echo "GCS forwarder installer should leave an existing runtime log's mode alone" >&2
    exit 1
    ;;
esac

for expected in \
  'sinks.beacon_runtime_gcs' \
  'sinks.beacon_inventory_gcs' \
  "$TMP_DIR/gcs-runtime.jsonl" \
  "$TMP_DIR/inventory_state.jsonl" \
  'key_prefix = "${BEACON_GCS_PREFIX:-beacon}/runtime/date=%F/"' \
  'key_prefix = "${BEACON_GCS_PREFIX:-beacon}/inventory/date=%F/"' \
  'read_from = "beginning"' \
  'filename_extension = "jsonl"' \
  'filename_append_uuid = true' \
  '[sinks.beacon_runtime_gcs.healthcheck]' \
  '[sinks.beacon_inventory_gcs.healthcheck]' \
  'enabled = false'
do
  if ! grep -Fq "$expected" "$GCS_FORWARDER_BASE/gcs-vector.toml"; then
    echo "GCS Vector config missing: $expected" >&2
    exit 1
  fi
done

if grep -q 'beacon-gcs-test-bucket\|service_account\|private_key\|content_encoding\|compression = "gzip"\|\.input_tokens =\|\.output_tokens =\|\.cost_usd =' "$GCS_FORWARDER_BASE/gcs-vector.toml"; then
  echo "GCS Vector config should not contain destination values, credentials, gzip metadata, or parallel usage fields" >&2
  exit 1
fi
for expected in \
  "BEACON_GCS_BUCKET='beacon-gcs-test-bucket'" \
  "BEACON_GCS_PREFIX='beacon/claude'" \
  "BEACON_GCS_STORAGE_CLASS='NEARLINE'" \
  "GOOGLE_APPLICATION_CREDENTIALS='$GCS_CREDENTIALS'" \
  "GOOGLE_CLOUD_PROJECT='beacon-test'"
do
  if ! grep -Fq "$expected" "$GCS_FORWARDER_BASE/gcs-vector.env"; then
    echo "GCS Vector env missing: $expected" >&2
    exit 1
  fi
done
case "$(ls -l "$GCS_FORWARDER_BASE/gcs-vector.env" | awk '{print $1}')" in
  -rw-------*) ;;
  *)
    echo "GCS Vector env should be 0600" >&2
    exit 1
    ;;
esac
if ! grep -q 'RunAtLoad' "$GCS_LAUNCHDAEMONS_DIR/com.beacon.endpoint.gcs-forwarder.plist"; then
  echo "GCS Vector plist missing RunAtLoad" >&2
  exit 1
fi

RELATIVE_CREDENTIALS_OUTPUT="$TMP_DIR/gcs-relative-credentials.out"
if BEACON_VECTOR_BIN="$FAKE_VECTOR" \
  BEACON_GCS_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/gcs/run-forwarder.sh" \
  BEACON_FORWARDER_BASE_DIR="$TMP_DIR/gcs-relative-forwarders" \
  BEACON_LAUNCHDAEMONS_DIR="$TMP_DIR/gcs-relative-launchdaemons" \
  BEACON_NO_START="1" \
  GOOGLE_APPLICATION_CREDENTIALS="relative-service-account.json" \
  "$ROOT_DIR/packaging/macos/jamf/claude/gcs/install-forwarder.sh" _ _ _ "beacon-gcs-test-bucket" >"$RELATIVE_CREDENTIALS_OUTPUT" 2>&1
then
  echo "GCS installer should reject a relative credential path" >&2
  exit 1
fi
if ! grep -q 'must be an absolute path' "$RELATIVE_CREDENTIALS_OUTPUT"; then
  echo "GCS installer should explain launchd credential path requirements" >&2
  exit 1
fi

GCS_ATOMIC_BASE="$TMP_DIR/gcs-atomic-forwarders"
GCS_ATOMIC_PLISTS="$TMP_DIR/gcs-atomic-launchdaemons"
GCS_FAIL_VECTOR="$TMP_DIR/gcs-vector-validation-fails"
mkdir -p "$GCS_ATOMIC_BASE" "$GCS_ATOMIC_PLISTS"
printf '%s\n' original-config >"$GCS_ATOMIC_BASE/gcs-vector.toml"
printf '%s\n' original-env >"$GCS_ATOMIC_BASE/gcs-vector.env"
printf '%s\n' original-plist >"$GCS_ATOMIC_PLISTS/com.beacon.endpoint.gcs-forwarder.plist"
# Vector reports a rejected config on stdout; this is what it prints for a service-account key
# Google does not accept yet.
cat >"$GCS_FAIL_VECTOR" <<'STUB'
#!/bin/sh
case "$1" in
  --version) exit 0 ;;
  validate)
    echo 'x Sink "beacon_runtime_gcs": Failed to get OAuth token: TokenErr: Invalid JWT Signature.'
    exit 78
    ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$GCS_FAIL_VECTOR"
GCS_FAIL_STDERR="$TMP_DIR/gcs-validation-fails.err"
set +e
BEACON_VECTOR_BIN="$GCS_FAIL_VECTOR" \
  BEACON_GCS_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/gcs/run-forwarder.sh" \
  BEACON_FORWARDER_BASE_DIR="$GCS_ATOMIC_BASE" \
  BEACON_LAUNCHDAEMONS_DIR="$GCS_ATOMIC_PLISTS" \
  BEACON_RUNTIME_LOG_PATHS="$TMP_DIR/gcs-atomic-runtime.jsonl" \
  BEACON_NO_START="1" \
  GOOGLE_APPLICATION_CREDENTIALS="$GCS_CREDENTIALS" \
  "$ROOT_DIR/packaging/macos/jamf/claude/gcs/install-forwarder.sh" _ _ _ "beacon-gcs-test-bucket" >/dev/null 2>"$GCS_FAIL_STDERR"
GCS_FAIL_STATUS=$?
set -e
if [ "$GCS_FAIL_STATUS" -ne 78 ]; then
  echo "GCS installer should fail with Vector's exit code when Vector rejects generated config, got $GCS_FAIL_STATUS" >&2
  exit 1
fi
for expected in \
  'Failed to get OAuth token: TokenErr: Invalid JWT Signature.' \
  'A new key takes up to a minute to start working' \
  'the installed forwarder was left unchanged'; do
  if ! grep -Fq "$expected" "$GCS_FAIL_STDERR"; then
    echo "GCS installer should explain a rejected config on stderr; missing: $expected" >&2
    cat "$GCS_FAIL_STDERR" >&2
    exit 1
  fi
done
if [ "$(cat "$GCS_ATOMIC_BASE/gcs-vector.toml")" != "original-config" ] ||
   [ "$(cat "$GCS_ATOMIC_BASE/gcs-vector.env")" != "original-env" ] ||
   [ "$(cat "$GCS_ATOMIC_PLISTS/com.beacon.endpoint.gcs-forwarder.plist")" != "original-plist" ]; then
  echo "GCS validation failure should preserve the last installed configuration" >&2
  exit 1
fi
for path in "$GCS_ATOMIC_BASE"/*.tmp.* "$GCS_ATOMIC_PLISTS"/*.tmp.*; do
  if [ -e "$path" ]; then
    echo "GCS validation failure should remove temporary file: $path" >&2
    exit 1
  fi
done

GCS_RUNNER="$TMP_DIR/gcs-vector-runner"
GCS_RUN_MARKER="$TMP_DIR/gcs-vector-run-args"
cat >"$GCS_RUNNER" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" > "$GCS_RUN_MARKER"
STUB
chmod +x "$GCS_RUNNER"
BEACON_VECTOR_BIN="$GCS_RUNNER" \
BEACON_GCS_VECTOR_CONFIG="$GCS_FORWARDER_BASE/gcs-vector.toml" \
BEACON_GCS_VECTOR_ENV="$GCS_FORWARDER_BASE/gcs-vector.env" \
GCS_RUN_MARKER="$GCS_RUN_MARKER" \
"$ROOT_DIR/packaging/macos/jamf/claude/gcs/run-forwarder.sh"
if ! grep -Fq -- "--config $GCS_FORWARDER_BASE/gcs-vector.toml" "$GCS_RUN_MARKER"; then
  echo "GCS wrapper should exec Vector with the generated config" >&2
  exit 1
fi

FAKE_LAUNCHCTL_BIN="$TMP_DIR/fake-launchctl-bin"
FAKE_LAUNCHCTL_STATE="$TMP_DIR/fake-launchctl-state"
GCS_LIFECYCLE_BASE="$TMP_DIR/gcs-lifecycle-forwarders"
GCS_LIFECYCLE_PLISTS="$TMP_DIR/gcs-lifecycle-launchdaemons"
mkdir -p "$FAKE_LAUNCHCTL_BIN" "$GCS_LIFECYCLE_PLISTS"
printf '%s\n' old >"$FAKE_LAUNCHCTL_STATE"
cat >"$FAKE_LAUNCHCTL_BIN/launchctl" <<'STUB'
#!/bin/sh
case "$1" in
  bootout)
    printf '%s\n' stopping >"$FAKE_LAUNCHCTL_STATE"
    ;;
  bootstrap)
    # launchd refuses to load a job whose previous instance has not finished stopping.
    if [ "$(cat "$FAKE_LAUNCHCTL_STATE" 2>/dev/null)" = stopping ]; then
      echo "Bootstrap failed: 5: Input/output error" >&2
      exit 5
    fi
    printf '%s\n' running >"$FAKE_LAUNCHCTL_STATE"
    ;;
  print)
    [ -f "$FAKE_LAUNCHCTL_STATE" ] || exit 113
    state="$(cat "$FAKE_LAUNCHCTL_STATE")"
    if [ "$state" = "stopping" ]; then
      echo "state = stopping"
      rm -f "$FAKE_LAUNCHCTL_STATE"
      exit 0
    fi
    if [ "${FAKE_LAUNCHCTL_CRASH_LOOP:-0}" = "1" ]; then
      count_file="$FAKE_LAUNCHCTL_STATE.count"
      count=0
      [ ! -f "$count_file" ] || count="$(cat "$count_file")"
      count=$((count + 1))
      printf '%s\n' "$count" >"$count_file"
      if [ $((count % 2)) -eq 0 ]; then
        echo "state = exited"
        exit 0
      fi
    fi
    echo "state = running"
    echo "pid = 1234"
    ;;
  *)
    echo "unexpected launchctl args: $*" >&2
    exit 1
    ;;
esac
STUB
chmod +x "$FAKE_LAUNCHCTL_BIN/launchctl"

PATH="$FAKE_LAUNCHCTL_BIN:$PATH" \
FAKE_LAUNCHCTL_STATE="$FAKE_LAUNCHCTL_STATE" \
BEACON_FORWARDER_POLL_SECONDS=0 \
BEACON_VECTOR_BIN="$FAKE_VECTOR" \
BEACON_GCS_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/gcs/run-forwarder.sh" \
BEACON_FORWARDER_BASE_DIR="$GCS_LIFECYCLE_BASE" \
BEACON_LAUNCHDAEMONS_DIR="$GCS_LIFECYCLE_PLISTS" \
BEACON_RUNTIME_LOG_PATHS="$TMP_DIR/gcs-lifecycle-runtime.jsonl" \
GOOGLE_APPLICATION_CREDENTIALS="$GCS_CREDENTIALS" \
"$ROOT_DIR/packaging/macos/jamf/claude/gcs/install-forwarder.sh" _ _ _ "beacon-gcs-test-bucket" "beacon" "STANDARD" "end" >/dev/null
if [ "$(cat "$FAKE_LAUNCHCTL_STATE")" != "running" ]; then
  echo "GCS installer should wait for bootout and bootstrap a running service" >&2
  exit 1
fi

GCS_CRASH_BASE="$TMP_DIR/gcs-crash-forwarders"
GCS_CRASH_PLISTS="$TMP_DIR/gcs-crash-launchdaemons"
mkdir -p "$GCS_CRASH_PLISTS"
printf '%s\n' old >"$FAKE_LAUNCHCTL_STATE"
rm -f "$FAKE_LAUNCHCTL_STATE.count"
if PATH="$FAKE_LAUNCHCTL_BIN:$PATH" \
  FAKE_LAUNCHCTL_STATE="$FAKE_LAUNCHCTL_STATE" \
  FAKE_LAUNCHCTL_CRASH_LOOP=1 \
  BEACON_FORWARDER_START_ATTEMPTS=4 \
  BEACON_FORWARDER_POLL_SECONDS=0 \
  BEACON_VECTOR_BIN="$FAKE_VECTOR" \
  BEACON_GCS_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/gcs/run-forwarder.sh" \
  BEACON_FORWARDER_BASE_DIR="$GCS_CRASH_BASE" \
  BEACON_LAUNCHDAEMONS_DIR="$GCS_CRASH_PLISTS" \
  BEACON_RUNTIME_LOG_PATHS="$TMP_DIR/gcs-crash-runtime.jsonl" \
  GOOGLE_APPLICATION_CREDENTIALS="$GCS_CREDENTIALS" \
  "$ROOT_DIR/packaging/macos/jamf/claude/gcs/install-forwarder.sh" _ _ _ "beacon-gcs-test-bucket" "beacon" "STANDARD" "end" >/dev/null 2>&1
then
  echo "GCS installer should reject a crash-looping launchd job" >&2
  exit 1
fi

# The S3 installer has its own copy of the bootout wait and sustained-start check. It has no poll
# override, so `sleep` is stubbed out.
FAKE_SLEEP_BIN="$TMP_DIR/fake-sleep-bin"
mkdir -p "$FAKE_SLEEP_BIN"
printf '#!/bin/sh\nexit 0\n' >"$FAKE_SLEEP_BIN/sleep"
chmod +x "$FAKE_SLEEP_BIN/sleep"
for case_name in lifecycle crash; do
  mkdir -p "$TMP_DIR/s3-$case_name-launchdaemons"
  printf '%s\n' old >"$FAKE_LAUNCHCTL_STATE"
  rm -f "$FAKE_LAUNCHCTL_STATE.count"
  crash_loop=0
  [ "$case_name" = lifecycle ] || crash_loop=1
  if PATH="$FAKE_SLEEP_BIN:$FAKE_LAUNCHCTL_BIN:$PATH" \
    FAKE_LAUNCHCTL_STATE="$FAKE_LAUNCHCTL_STATE" \
    FAKE_LAUNCHCTL_CRASH_LOOP="$crash_loop" \
    BEACON_FORWARDER_START_ATTEMPTS=4 \
    BEACON_VECTOR_BIN="$FAKE_VECTOR" \
    BEACON_S3_FORWARDER_WRAPPER="$ROOT_DIR/packaging/macos/jamf/claude/s3/run-forwarder.sh" \
    BEACON_FORWARDER_BASE_DIR="$TMP_DIR/s3-$case_name-forwarders" \
    BEACON_LAUNCHDAEMONS_DIR="$TMP_DIR/s3-$case_name-launchdaemons" \
    BEACON_RUNTIME_LOG_PATHS="$TMP_DIR/s3-$case_name-runtime.jsonl" \
    AWS_ACCESS_KEY_ID="test-access-key" \
    AWS_SECRET_ACCESS_KEY="test-secret-key" \
    "$ROOT_DIR/packaging/macos/jamf/claude/s3/install-forwarder.sh" _ _ _ "beacon-test-bucket" "us-west-2" "beacon" "STANDARD" "end" >/dev/null 2>&1
  then
    s3_status=0
  else
    s3_status=$?
  fi
  if [ "$case_name" = lifecycle ]; then
    if [ "$s3_status" -ne 0 ] || [ "$(cat "$FAKE_LAUNCHCTL_STATE")" != "running" ]; then
      echo "S3 installer should wait for bootout and bootstrap a running service" >&2
      exit 1
    fi
  elif [ "$s3_status" -eq 0 ]; then
    echo "S3 installer should reject a crash-looping launchd job" >&2
    exit 1
  fi
done

GCS_FAKE_REPAIR="$TMP_DIR/fake-gcs-repair-fails.sh"
GCS_FAKE_FORWARDER="$TMP_DIR/fake-gcs-forwarder-runs.sh"
GCS_FORWARDER_MARKER="$TMP_DIR/gcs-forwarder-ran"
cat >"$GCS_FAKE_REPAIR" <<'STUB'
#!/bin/sh
exit 11
STUB
cat >"$GCS_FAKE_FORWARDER" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" > "$GCS_FORWARDER_MARKER"
STUB
chmod +x "$GCS_FAKE_REPAIR" "$GCS_FAKE_FORWARDER"

BEACON_REPAIR_HOOKS_SCRIPT="$GCS_FAKE_REPAIR" \
BEACON_GCS_VECTOR_SCRIPT="$GCS_FAKE_FORWARDER" \
BEACON_GCS_BUCKET="beacon-gcs-test-bucket" \
GOOGLE_APPLICATION_CREDENTIALS="$GCS_CREDENTIALS" \
GCS_FORWARDER_MARKER="$GCS_FORWARDER_MARKER" \
"$ROOT_DIR/packaging/macos/jamf/claude/gcs/repair-hooks-and-forwarder.sh" >/dev/null 2>&1 && {
  echo "combined GCS Vector repair should return the failing repair status" >&2
  exit 1
}
if [ ! -f "$GCS_FORWARDER_MARKER" ]; then
  echo "combined GCS Vector repair should run forwarder before failing repair" >&2
  exit 1
fi

# repair-hooks.sh runs as root against a home the console user controls. Run it with root-only
# tools stubbed and check what it did. Regression for 96b42be4 (#809): no recursive chown of
# ~/.beacon, no grant letting the user add entries to the root log directory, stale grants revoked.
REPAIR_HOOKS_BIN="$TMP_DIR/repair-hooks-bin"
REPAIR_HOOKS_HOME="$TMP_DIR/repair-hooks-home"
REPAIR_HOOKS_LOGS="$TMP_DIR/repair-hooks-logs"
REPAIR_HOOKS_CALLS="$TMP_DIR/repair-hooks-calls"
mkdir -p "$REPAIR_HOOKS_BIN" "$REPAIR_HOOKS_HOME"
cat >"$REPAIR_HOOKS_BIN/beacon" <<'STUB'
#!/bin/sh
printf 'beacon %s\n' "$*" >>"$REPAIR_HOOKS_CALLS"
case "$*" in
  "endpoint hooks install"*)
    mkdir -p "$HOME/.beacon/endpoint/hooks"
    printf '#!/bin/sh\nexit 0\n' >"$HOME/.beacon/endpoint/hooks/beacon-hooks"
    /bin/chmod +x "$HOME/.beacon/endpoint/hooks/beacon-hooks"
    ;;
esac
STUB
printf '#!/bin/sh\nexit 0\n' >"$REPAIR_HOOKS_BIN/beacon-otelcol"
cat >"$REPAIR_HOOKS_BIN/chown" <<'STUB'
#!/bin/sh
printf 'chown %s\n' "$*" >>"$REPAIR_HOOKS_CALLS"
STUB
cat >"$REPAIR_HOOKS_BIN/chmod" <<'STUB'
#!/bin/sh
case "$1" in
  +a|-a#) printf 'acl %s\n' "$*" >>"$REPAIR_HOOKS_CALLS" ;;
  *) exec /bin/chmod "$@" ;;
esac
STUB
# The log directory carries grants an earlier version of the script (and the Jamf guide) left.
cat >"$REPAIR_HOOKS_BIN/ls" <<'STUB'
#!/bin/sh
if [ "$1" = "-led" ]; then
  printf 'drwxr-xr-x+ 2 root wheel 64 Jan  1 00:00 %s\n' "$2"
  printf ' 0: user:olduser allow add_file,add_subdirectory\n'
  printf ' 1: user:hookuser allow list,search,readattr\n'
  printf ' 2: group:staff allow add_file,delete_child\n'
  exit 0
fi
exec /bin/ls "$@"
STUB
cat >"$REPAIR_HOOKS_BIN/stat" <<'STUB'
#!/bin/sh
echo hookuser
STUB
cat >"$REPAIR_HOOKS_BIN/dscl" <<'STUB'
#!/bin/sh
printf 'NFSHomeDirectory: %s\n' "$REPAIR_HOOKS_HOME"
STUB
cat >"$REPAIR_HOOKS_BIN/sudo" <<'STUB'
#!/bin/sh
printf 'sudo %s\n' "$*" >>"$REPAIR_HOOKS_CALLS"
[ "$1" = "-u" ] || exit 64
shift 2
exec env "$@"
STUB
printf '#!/bin/sh\nexit 0\n' >"$REPAIR_HOOKS_BIN/launchctl"
chmod +x "$REPAIR_HOOKS_BIN"/*
run_repair_hooks() {
  PATH="$REPAIR_HOOKS_BIN:$PATH" \
  REPAIR_HOOKS_CALLS="$REPAIR_HOOKS_CALLS" \
  REPAIR_HOOKS_HOME="$REPAIR_HOOKS_HOME" \
  BEACON_BIN="$REPAIR_HOOKS_BIN/beacon" \
  BEACON_COLLECTOR="$REPAIR_HOOKS_BIN/beacon-otelcol" \
  BEACON_RUNTIME_LOG="$REPAIR_HOOKS_LOGS/runtime.jsonl" \
  bash "$ROOT_DIR/packaging/macos/jamf/claude/common/repair-hooks.sh" >/dev/null 2>&1
}
run_repair_hooks || {
  echo "repair-hooks should succeed with a console user" >&2
  exit 1
}
for entry in runtime.jsonl:666 runtime.jsonl.lock:666 inventory_state.jsonl:644 inventory_state.jsonl.lock:644 inventory-state.json:644 inventory-state.json.lock:644; do
  file="$REPAIR_HOOKS_LOGS/${entry%%:*}"
  if [ ! -f "$file" ] || [ "$(stat -f %Lp "$file" 2>/dev/null || stat -c %a "$file")" != "${entry##*:}" ]; then
    echo "repair-hooks should prepare $file with mode ${entry##*:}" >&2
    exit 1
  fi
done
if grep -q -e '^chown -R' -e "^chown .*$REPAIR_HOOKS_HOME" "$REPAIR_HOOKS_CALLS"; then
  echo "repair-hooks must not chown anything in the user's home as root" >&2
  exit 1
fi
if [ "$(grep '^acl -a#' "$REPAIR_HOOKS_CALLS")" != "$(printf 'acl -a# 2 %s\nacl -a# 0 %s' "$REPAIR_HOOKS_LOGS" "$REPAIR_HOOKS_LOGS")" ]; then
  echo "repair-hooks should revoke every entry-adding grant on the log directory, highest index first" >&2
  exit 1
fi
if grep '^acl +a' "$REPAIR_HOOKS_CALLS" | grep -q -e "$REPAIR_HOOKS_LOGS\$" -e add_file -e delete_child -e add_subdirectory; then
  echo "repair-hooks must not let the console user add entries to the root log directory" >&2
  exit 1
fi
if ! grep -qx "sudo -u hookuser mkdir -p $REPAIR_HOOKS_HOME/.beacon/endpoint/hooks" "$REPAIR_HOOKS_CALLS" ||
   [ ! -x "$REPAIR_HOOKS_HOME/.beacon/endpoint/hooks/beacon-hooks" ]; then
  echo "repair-hooks should create the hook directory as the console user and install hooks there" >&2
  exit 1
fi
# A ~/.beacon the user turned into a symlink is refused, not followed as root.
REPAIR_HOOKS_VICTIM="$TMP_DIR/repair-hooks-victim"
mkdir -p "$REPAIR_HOOKS_VICTIM"
rm -rf "$REPAIR_HOOKS_HOME/.beacon"
ln -s "$REPAIR_HOOKS_VICTIM" "$REPAIR_HOOKS_HOME/.beacon"
if run_repair_hooks; then
  echo "repair-hooks should refuse a symlinked ~/.beacon" >&2
  exit 1
fi
if [ -n "$(/bin/ls -A "$REPAIR_HOOKS_VICTIM")" ]; then
  echo "repair-hooks wrote through a symlinked ~/.beacon" >&2
  exit 1
fi

# postinstall restores the optional forwarders a customer already runs (74c24bb5). It names fixed
# system paths, so a copy with those paths redirected into the temp dir runs against a fake launchd.
POSTINSTALL_ROOT="$TMP_DIR/postinstall"
POSTINSTALL_BIN="$POSTINSTALL_ROOT/bin"
POSTINSTALL_DAEMONS="$POSTINSTALL_ROOT/LaunchDaemons"
POSTINSTALL_STATE="$POSTINSTALL_ROOT/state"
POSTINSTALL_LAUNCHCTL_LOG="$POSTINSTALL_ROOT/launchctl.log"
POSTINSTALL_BEACON_LOG="$POSTINSTALL_ROOT/beacon.log"
mkdir -p "$POSTINSTALL_BIN" "$POSTINSTALL_DAEMONS"
sed -e "s#/opt/beacon/scripts/install-endpoint.sh#$POSTINSTALL_BIN/install-endpoint.sh#g" \
  -e "s#/Library/LaunchDaemons/#$POSTINSTALL_DAEMONS/#g" \
  "$ROOT_DIR/packaging/macos/scripts/postinstall" >"$POSTINSTALL_ROOT/postinstall"
if grep -q -e '/opt/beacon/scripts/' -e '/Library/LaunchDaemons/' "$POSTINSTALL_ROOT/postinstall"; then
  echo "the postinstall test no longer redirects every system path postinstall uses" >&2
  exit 1
fi
cat >"$POSTINSTALL_BIN/install-endpoint.sh" <<'STUB'
#!/bin/sh
printf 'install-endpoint no_start=%s\n' "$BEACON_NO_START" >>"$POSTINSTALL_BEACON_LOG"
STUB
cat >"$POSTINSTALL_BIN/beacon" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"$POSTINSTALL_BEACON_LOG"
STUB
# State per label: running, or waiting (loaded but not running). No file means not loaded.
cat >"$POSTINSTALL_BIN/launchctl" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"$POSTINSTALL_LAUNCHCTL_LOG"
label_of() { basename "${1#system/}" .plist; }
case "$1" in
  print)
    state="$POSTINSTALL_STATE/$(label_of "$2")"
    [ -f "$state" ] || exit 113
    if [ "$(cat "$state")" = running ]; then
      echo "state = running"
      echo "pid = 4242"
    else
      echo "state = waiting"
    fi
    ;;
  kickstart) ;;
  bootout) rm -f "$POSTINSTALL_STATE/$(label_of "$2")" ;;
  bootstrap)
    label="$(label_of "$3")"
    if [ -f "$POSTINSTALL_STATE/$label" ]; then
      echo "Bootstrap failed: 5: Input/output error" >&2
      exit 5
    fi
    case " ${POSTINSTALL_NEVER_RUNS:-} " in
      *" $label "*) echo waiting >"$POSTINSTALL_STATE/$label" ;;
      *) echo running >"$POSTINSTALL_STATE/$label" ;;
    esac
    ;;
  *) exit 1 ;;
esac
STUB
printf '#!/bin/sh\nexit 0\n' >"$POSTINSTALL_BIN/sleep"
chmod +x "$POSTINSTALL_BIN"/* "$POSTINSTALL_ROOT/postinstall"
run_postinstall() {
  PATH="$POSTINSTALL_BIN:$PATH" \
  POSTINSTALL_STATE="$POSTINSTALL_STATE" \
  POSTINSTALL_NEVER_RUNS="${1:-}" \
  POSTINSTALL_LAUNCHCTL_LOG="$POSTINSTALL_LAUNCHCTL_LOG" \
  POSTINSTALL_BEACON_LOG="$POSTINSTALL_BEACON_LOG" \
  BEACON_BIN="$POSTINSTALL_BIN/beacon" \
  "$POSTINSTALL_ROOT/postinstall" >/dev/null 2>"$POSTINSTALL_ROOT/stderr"
}

# An S3 forwarder that is running gets restarted in place; a GCS forwarder launchd still has loaded
# but not running is booted out and bootstrapped again; Falcon, never installed, is left alone.
mkdir -p "$POSTINSTALL_STATE"
touch "$POSTINSTALL_DAEMONS/com.beacon.endpoint.s3-forwarder.plist" "$POSTINSTALL_DAEMONS/com.beacon.endpoint.gcs-forwarder.plist"
echo running >"$POSTINSTALL_STATE/com.beacon.endpoint.s3-forwarder"
echo waiting >"$POSTINSTALL_STATE/com.beacon.endpoint.gcs-forwarder"
run_postinstall || {
  echo "postinstall should succeed when every installed forwarder comes back: $(cat "$POSTINSTALL_ROOT/stderr")" >&2
  exit 1
}
if ! grep -qx 'kickstart -k system/com.beacon.endpoint.s3-forwarder' "$POSTINSTALL_LAUNCHCTL_LOG"; then
  echo "postinstall should kickstart a running forwarder" >&2
  exit 1
fi
if ! grep -qx 'bootout system/com.beacon.endpoint.gcs-forwarder' "$POSTINSTALL_LAUNCHCTL_LOG" ||
   [ "$(cat "$POSTINSTALL_STATE/com.beacon.endpoint.gcs-forwarder")" != running ]; then
  echo "postinstall should boot out and restart a loaded-but-stuck forwarder" >&2
  exit 1
fi
if ! grep -q 'launchctl bootstrap failed for com.beacon.endpoint.gcs-forwarder: Bootstrap failed: 5' "$POSTINSTALL_ROOT/stderr"; then
  echo "postinstall should report launchctl's bootstrap error" >&2
  exit 1
fi
if grep -q falcon-forwarder "$POSTINSTALL_LAUNCHCTL_LOG"; then
  echo "postinstall should not touch a forwarder that was never installed" >&2
  exit 1
fi
for expected in \
  'install-endpoint no_start=1' \
  'endpoint user-config repair-installed --system --harness claude,codex' \
  'endpoint update install-daemon' \
  'endpoint inventory install-daemon --system' \
  'endpoint asymptote reconcile --system'
do
  if ! grep -qxF "$expected" "$POSTINSTALL_BEACON_LOG"; then
    echo "postinstall should run: $expected" >&2
    exit 1
  fi
done

# A forwarder that never comes back fails the install loudly instead of leaving it stopped.
rm -rf "$POSTINSTALL_STATE" "$POSTINSTALL_DAEMONS"/*.plist
mkdir -p "$POSTINSTALL_STATE"
touch "$POSTINSTALL_DAEMONS/com.beacon.endpoint.falcon-forwarder.plist"
if run_postinstall com.beacon.endpoint.falcon-forwarder; then
  echo "postinstall should fail when an installed forwarder cannot be restored" >&2
  exit 1
fi
if ! grep -q 'could not restore existing forwarder com.beacon.endpoint.falcon-forwarder' "$POSTINSTALL_ROOT/stderr"; then
  echo "postinstall should name the forwarder it could not restore" >&2
  exit 1
fi

# Safari app-wrapper helper and MDM declaration (packaging/macos/safari). Runs on
# Linux too: the packager and uname are faked.
sh "$ROOT_DIR/packaging/macos/safari/test-safari-packaging.sh"

# Fleet osquery files (packaging/macos/fleet) and the Fleet admin helper (examples/fleet).
sh "$ROOT_DIR/examples/fleet/test/test-fleet-sql.sh"
sh "$ROOT_DIR/examples/fleet/test/test-configure-beacon-macos-s3.sh"
