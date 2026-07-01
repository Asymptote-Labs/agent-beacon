#!/bin/sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
INSTALL_SCRIPT="$ROOT_DIR/packaging/macos/install-endpoint.sh"
UNINSTALL_SCRIPT="$ROOT_DIR/packaging/macos/uninstall-endpoint.sh"
PKG_BUILD_SCRIPT="$ROOT_DIR/packaging/macos/build-pkg.sh"
PKG_SIGN_NOTARIZE_SCRIPT="$ROOT_DIR/packaging/macos/build-signed-notarized-pkg.sh"
REPAIR_SCRIPT="$ROOT_DIR/packaging/macos/jamf/scripts/repair.sh"
FLEET_REPAIR_SCRIPT="$ROOT_DIR/packaging/macos/fleet/scripts/repair.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

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
if ! grep -q 'INVENTORY_STATE' "$ROOT_DIR/packaging/macos/jamf/claude/common/repair-hooks.sh"; then
  echo "repair-hooks should prepare inventory heartbeat state files" >&2
  exit 1
fi
if ! grep -q 'restore_existing_forwarder' "$ROOT_DIR/packaging/macos/scripts/postinstall"; then
  echo "postinstall should restore existing optional forwarders" >&2
  exit 1
fi
if ! grep -q 'endpoint user-config repair-installed' "$ROOT_DIR/packaging/macos/scripts/postinstall"; then
  echo "postinstall should refresh installed user endpoint configuration after package install" >&2
  exit 1
fi
if ! grep -q 'launchctl kickstart -k' "$ROOT_DIR/packaging/macos/scripts/postinstall"; then
  echo "postinstall should kickstart already-loaded forwarders" >&2
  exit 1
fi
if ! grep -q 'launchctl bootout "$target"' "$ROOT_DIR/packaging/macos/scripts/postinstall"; then
  echo "postinstall should recover loaded-but-stuck forwarders with bootout" >&2
  exit 1
fi
if ! grep -q 'could not restore existing forwarder' "$ROOT_DIR/packaging/macos/scripts/postinstall"; then
  echo "postinstall should fail loudly when an existing forwarder cannot be restored" >&2
  exit 1
fi
if ! grep -q 'launchctl bootstrap failed' "$ROOT_DIR/packaging/macos/scripts/postinstall"; then
  echo "postinstall should preserve launchctl bootstrap diagnostics" >&2
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

if [ ! -f "$FORWARDER_BASE/falcon-vector.toml" ]; then
  echo "Falcon Vector config was not written" >&2
  exit 1
fi
if [ ! -f "$FORWARDER_BASE/falcon-vector.env" ]; then
  echo "Falcon Vector env was not written" >&2
  exit 1
fi
if [ ! -f "$LAUNCHDAEMONS_DIR/com.beacon.endpoint.falcon-forwarder.plist" ]; then
  echo "Falcon Vector plist was not written" >&2
  exit 1
fi
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
"$ROOT_DIR/packaging/macos/jamf/claude/s3/install-forwarder.sh" _ _ _ "beacon-test-bucket" "us-west-2" "beacon/claude/runtime" "STANDARD_IA" "end" >/dev/null

if [ ! -f "$S3_FORWARDER_BASE/s3-vector.toml" ]; then
  echo "S3 Vector config was not written" >&2
  exit 1
fi
if [ ! -f "$S3_FORWARDER_BASE/s3-vector.env" ]; then
  echo "S3 Vector env was not written" >&2
  exit 1
fi
if [ ! -f "$S3_LAUNCHDAEMONS_DIR/com.beacon.endpoint.s3-forwarder.plist" ]; then
  echo "S3 Vector plist was not written" >&2
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
