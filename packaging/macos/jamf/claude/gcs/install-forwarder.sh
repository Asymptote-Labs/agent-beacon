#!/bin/sh
set -eu

LABEL="${BEACON_GCS_FORWARDER_LABEL:-com.beacon.endpoint.gcs-forwarder}"
VECTOR_BIN="${BEACON_VECTOR_BIN:-/opt/beacon/bin/vector}"
WRAPPER="${BEACON_GCS_FORWARDER_WRAPPER:-/opt/beacon/jamf/claude/gcs/run-forwarder.sh}"
BASE_DIR="${BEACON_FORWARDER_BASE_DIR:-/Library/Application Support/Beacon/Forwarders}"
LAUNCHDAEMONS_DIR="${BEACON_LAUNCHDAEMONS_DIR:-/Library/LaunchDaemons}"
CONFIG_PATH="${BEACON_GCS_VECTOR_CONFIG:-$BASE_DIR/gcs-vector.toml}"
ENV_PATH="${BEACON_GCS_VECTOR_ENV:-$BASE_DIR/gcs-vector.env}"
DATA_DIR="${BEACON_GCS_VECTOR_DATA_DIR:-$BASE_DIR/vector-data/gcs}"
PLIST_PATH="$LAUNCHDAEMONS_DIR/$LABEL.plist"
CONFIG_TMP="${CONFIG_PATH}.tmp.$$.toml"
ENV_TMP="${ENV_PATH}.tmp.$$"
PLIST_TMP="${PLIST_PATH}.tmp.$$"

GCS_BUCKET="${BEACON_GCS_BUCKET:-${4:-}}"
GCS_PREFIX="${BEACON_GCS_PREFIX:-${5:-beacon}}"
GCS_STORAGE_CLASS="${BEACON_GCS_STORAGE_CLASS:-${6:-STANDARD}}"
VECTOR_READ_FROM="${BEACON_VECTOR_READ_FROM:-${7:-end}}"
RUNTIME_LOG_PATHS="${BEACON_RUNTIME_LOG_PATHS:-/var/log/beacon-agent/runtime.jsonl}"
CREDENTIALS_PATH="${GOOGLE_APPLICATION_CREDENTIALS:-}"
NO_START="${BEACON_NO_START:-0}"
STOP_ATTEMPTS="${BEACON_FORWARDER_STOP_ATTEMPTS:-65}"
START_ATTEMPTS="${BEACON_FORWARDER_START_ATTEMPTS:-15}"

GCS_PREFIX="${GCS_PREFIX%/}"
case "$GCS_PREFIX" in
  */runtime)
    GCS_PREFIX="${GCS_PREFIX%/runtime}"
    ;;
  */inventory)
    GCS_PREFIX="${GCS_PREFIX%/inventory}"
    ;;
esac
GCS_PREFIX="${GCS_PREFIX%/}"
if [ -z "$GCS_PREFIX" ]; then
  GCS_PREFIX="beacon"
fi

if [ -z "$GCS_BUCKET" ]; then
  echo "GCS bucket is required (BEACON_GCS_BUCKET or Jamf parameter 4)" >&2
  exit 1
fi

if [ -z "$CREDENTIALS_PATH" ]; then
  echo "GOOGLE_APPLICATION_CREDENTIALS must point to a service-account JSON file" >&2
  exit 1
fi
case "$CREDENTIALS_PATH" in
  /*) ;;
  *)
    echo "GOOGLE_APPLICATION_CREDENTIALS must be an absolute path for launchd" >&2
    exit 1
    ;;
esac
if [ ! -r "$CREDENTIALS_PATH" ]; then
  echo "Google credentials file is not readable at $CREDENTIALS_PATH" >&2
  exit 1
fi
if [ "$(uname -s)" = "Darwin" ] && [ "$(id -u)" -eq 0 ]; then
  credentials_owner="$(stat -f '%Su' "$CREDENTIALS_PATH")"
  credentials_mode="$(stat -f '%Lp' "$CREDENTIALS_PATH")"
  if [ "$credentials_owner" != "root" ]; then
    echo "Google credentials file must be owned by root: $CREDENTIALS_PATH" >&2
    exit 1
  fi
  case "$credentials_mode" in
    400|600) ;;
    *)
      echo "Google credentials file must have mode 0400 or 0600: $CREDENTIALS_PATH" >&2
      exit 1
      ;;
  esac
fi

if [ ! -x "$VECTOR_BIN" ]; then
  echo "Vector binary not found or not executable at $VECTOR_BIN" >&2
  exit 1
fi
if [ ! -x "$WRAPPER" ]; then
  echo "GCS forwarder wrapper not found or not executable at $WRAPPER" >&2
  exit 1
fi

"$VECTOR_BIN" --version >/dev/null

shell_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

write_env_if_set() {
  name="$1"
  eval "value=\${$name:-}"
  if [ -n "$value" ]; then
    printf 'export %s=%s\n' "$name" "$(shell_quote "$value")"
  fi
}

toml_quote() {
  printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"
}

xml_escape() {
  printf '%s' "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'
}

include_array() {
  paths="$1"
  old_ifs="$IFS"
  IFS=,
  first=1
  printf '['
  for raw in $paths; do
    path="$(printf '%s' "$raw" | sed 's/^ *//; s/ *$//')"
    [ -n "$path" ] || continue
    if [ "$first" -eq 0 ]; then
      printf ', '
    fi
    toml_quote "$path"
    first=0
    case "$path" in
      *'*'*|*'?'*|*'['*) ;;
      *)
        mkdir -p "$(dirname "$path")"
        touch "$path"
        chmod 0644 "$path" || true
        ;;
    esac
  done
  printf ']'
  IFS="$old_ifs"
}

inventory_paths() {
  old_ifs="$IFS"
  IFS=,
  first=1
  for raw in $RUNTIME_LOG_PATHS; do
    path="$(printf '%s' "$raw" | sed 's/^ *//; s/ *$//')"
    [ -n "$path" ] || continue
    if [ "$first" -eq 0 ]; then
      printf ','
    fi
    printf '%s/inventory_state.jsonl' "$(dirname "$path")"
    first=0
  done
  IFS="$old_ifs"
}

cleanup_temp_files() {
  rm -f "$CONFIG_TMP" "$ENV_TMP" "$PLIST_TMP"
}
trap cleanup_temp_files EXIT
trap 'exit 1' INT TERM

mkdir -p "$BASE_DIR" "$DATA_DIR" "$LAUNCHDAEMONS_DIR"
chmod 0755 "$BASE_DIR" "$DATA_DIR" "$LAUNCHDAEMONS_DIR" 2>/dev/null || true

RUNTIME_INCLUDES="$(include_array "$RUNTIME_LOG_PATHS")"
INVENTORY_LOG_PATHS="$(inventory_paths)"
INVENTORY_INCLUDES="$(include_array "$INVENTORY_LOG_PATHS")"

cat >"$CONFIG_TMP" <<EOF
# Managed by Beacon. Do not edit by hand.
# Google credentials are resolved from GOOGLE_APPLICATION_CREDENTIALS.
data_dir = "$(printf '%s' "$DATA_DIR" | sed 's/\\/\\\\/g; s/"/\\"/g')"

[sources.beacon_runtime]
type = "file"
include = $RUNTIME_INCLUDES
read_from = "\${BEACON_VECTOR_READ_FROM:-end}"

[sources.beacon_inventory]
type = "file"
include = $INVENTORY_INCLUDES
read_from = "beginning"

[transforms.beacon_runtime_json]
type = "remap"
inputs = ["beacon_runtime"]
source = '''
. = parse_json!(.message)
'''

[transforms.beacon_inventory_json]
type = "remap"
inputs = ["beacon_inventory"]
source = '''
. = parse_json!(.message)
'''

[sinks.beacon_runtime_gcs]
type = "gcp_cloud_storage"
inputs = ["beacon_runtime_json"]
bucket = "\${BEACON_GCS_BUCKET}"
key_prefix = "\${BEACON_GCS_PREFIX:-beacon}/runtime/date=%F/"
filename_time_format = "%s"
filename_append_uuid = true
filename_extension = "jsonl"
content_type = "application/x-ndjson"
storage_class = "\${BEACON_GCS_STORAGE_CLASS:-STANDARD}"

[sinks.beacon_runtime_gcs.encoding]
codec = "json"

[sinks.beacon_runtime_gcs.framing]
method = "newline_delimited"

[sinks.beacon_runtime_gcs.batch]
max_bytes = 10000000
timeout_secs = 300

[sinks.beacon_runtime_gcs.request]
retry_attempts = 10
retry_initial_backoff_secs = 1
retry_max_duration_secs = 300

[sinks.beacon_runtime_gcs.healthcheck]
enabled = false

[sinks.beacon_inventory_gcs]
type = "gcp_cloud_storage"
inputs = ["beacon_inventory_json"]
bucket = "\${BEACON_GCS_BUCKET}"
key_prefix = "\${BEACON_GCS_PREFIX:-beacon}/inventory/date=%F/"
filename_time_format = "%s"
filename_append_uuid = true
filename_extension = "jsonl"
content_type = "application/x-ndjson"
storage_class = "\${BEACON_GCS_STORAGE_CLASS:-STANDARD}"

[sinks.beacon_inventory_gcs.encoding]
codec = "json"

[sinks.beacon_inventory_gcs.framing]
method = "newline_delimited"

[sinks.beacon_inventory_gcs.batch]
max_bytes = 10000000
timeout_secs = 300

[sinks.beacon_inventory_gcs.request]
retry_attempts = 10
retry_initial_backoff_secs = 1
retry_max_duration_secs = 300

[sinks.beacon_inventory_gcs.healthcheck]
enabled = false
EOF
chmod 0644 "$CONFIG_TMP"

{
  printf 'export BEACON_GCS_BUCKET=%s\n' "$(shell_quote "$GCS_BUCKET")"
  printf 'export BEACON_GCS_PREFIX=%s\n' "$(shell_quote "$GCS_PREFIX")"
  printf 'export BEACON_GCS_STORAGE_CLASS=%s\n' "$(shell_quote "$GCS_STORAGE_CLASS")"
  printf 'export BEACON_VECTOR_READ_FROM=%s\n' "$(shell_quote "$VECTOR_READ_FROM")"
  printf 'export GOOGLE_APPLICATION_CREDENTIALS=%s\n' "$(shell_quote "$CREDENTIALS_PATH")"
  write_env_if_set GOOGLE_CLOUD_PROJECT
  write_env_if_set CLOUDSDK_CORE_PROJECT
} >"$ENV_TMP"
chmod 0600 "$ENV_TMP"

# Validate syntax with the same environment the launch daemon will receive.
BEACON_GCS_BUCKET="$GCS_BUCKET" \
BEACON_GCS_PREFIX="$GCS_PREFIX" \
BEACON_GCS_STORAGE_CLASS="$GCS_STORAGE_CLASS" \
BEACON_VECTOR_READ_FROM="$VECTOR_READ_FROM" \
GOOGLE_APPLICATION_CREDENTIALS="$CREDENTIALS_PATH" \
  "$VECTOR_BIN" validate --skip-healthchecks "$CONFIG_TMP" >/dev/null

cat >"$PLIST_TMP" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$(xml_escape "$LABEL")</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$WRAPPER")</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/$LABEL.out</string>
  <key>StandardErrorPath</key>
  <string>/tmp/$LABEL.err</string>
</dict>
</plist>
EOF
chmod 0644 "$PLIST_TMP"

mv -f "$CONFIG_TMP" "$CONFIG_PATH"
mv -f "$ENV_TMP" "$ENV_PATH"
mv -f "$PLIST_TMP" "$PLIST_PATH"
trap - EXIT INT TERM

echo "GCS Vector config: $CONFIG_PATH"
echo "GCS Vector env: $ENV_PATH"
echo "GCS Vector LaunchDaemon: $PLIST_PATH"

case "$NO_START" in
  1|true|TRUE|yes|YES)
    echo "BEACON_NO_START is set; not loading $LABEL"
    exit 0
    ;;
esac

if command -v launchctl >/dev/null 2>&1; then
  target="system/$LABEL"
  launchctl bootout "$target" >/dev/null 2>&1 || true
  attempts=0
  while launchctl print "$target" >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge "$STOP_ATTEMPTS" ]; then
      echo "Timed out waiting for $LABEL to stop" >&2
      exit 1
    fi
    sleep 1
  done
  launchctl bootstrap system "$PLIST_PATH" >/dev/null 2>&1 || {
    echo "Could not bootstrap $LABEL" >&2
    exit 1
  }
  attempts=0
  consecutive_running=0
  while [ "$attempts" -lt "$START_ATTEMPTS" ] && [ "$consecutive_running" -lt 3 ]; do
    attempts=$((attempts + 1))
    if launchctl print "$target" 2>/dev/null | grep -Eq 'state = running|pid ='; then
      consecutive_running=$((consecutive_running + 1))
    else
      consecutive_running=0
    fi
    if [ "$consecutive_running" -lt 3 ]; then
      sleep 1
    fi
  done
  if [ "$consecutive_running" -lt 3 ]; then
    launchctl print "$target" 2>&1 || true
    echo "$LABEL did not remain running" >&2
    exit 1
  fi
  launchctl print "$target" || true
else
  echo "launchctl unavailable; wrote config but did not start $LABEL" >&2
fi

if [ -f "/tmp/$LABEL.err" ]; then
  echo "Last GCS Vector stderr lines:"
  tail -n 20 "/tmp/$LABEL.err" || true
fi
