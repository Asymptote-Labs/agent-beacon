#!/bin/sh
set -eu

LABEL="${BEACON_S3_FORWARDER_LABEL:-com.beacon.endpoint.s3-forwarder}"
VECTOR_BIN="${BEACON_VECTOR_BIN:-/opt/beacon/bin/vector}"
WRAPPER="${BEACON_S3_FORWARDER_WRAPPER:-/opt/beacon/jamf/claude/s3/run-forwarder.sh}"
BASE_DIR="${BEACON_FORWARDER_BASE_DIR:-/Library/Application Support/Beacon/Forwarders}"
LAUNCHDAEMONS_DIR="${BEACON_LAUNCHDAEMONS_DIR:-/Library/LaunchDaemons}"
CONFIG_PATH="${BEACON_S3_VECTOR_CONFIG:-$BASE_DIR/s3-vector.toml}"
ENV_PATH="${BEACON_S3_VECTOR_ENV:-$BASE_DIR/s3-vector.env}"
DATA_DIR="${BEACON_S3_VECTOR_DATA_DIR:-$BASE_DIR/vector-data/s3}"
PLIST_PATH="$LAUNCHDAEMONS_DIR/$LABEL.plist"

S3_BUCKET="${BEACON_S3_BUCKET:-${4:-}}"
AWS_REGION_VALUE="${AWS_REGION:-${5:-}}"
S3_PREFIX="${BEACON_S3_PREFIX:-${6:-beacon}}"
S3_STORAGE_CLASS="${BEACON_S3_STORAGE_CLASS:-${7:-STANDARD}}"
VECTOR_READ_FROM="${BEACON_VECTOR_READ_FROM:-${8:-end}}"
RUNTIME_LOG_PATHS="${BEACON_RUNTIME_LOG_PATHS:-/var/log/beacon-agent/runtime.jsonl}"
NO_START="${BEACON_NO_START:-0}"

case "$S3_PREFIX" in
  */runtime)
    S3_PREFIX="${S3_PREFIX%/runtime}"
    ;;
  */inventory)
    S3_PREFIX="${S3_PREFIX%/inventory}"
    ;;
esac
S3_PREFIX="${S3_PREFIX%/}"
if [ -z "$S3_PREFIX" ]; then
  S3_PREFIX="beacon"
fi

if [ -z "$S3_BUCKET" ]; then
  echo "S3 bucket is required (BEACON_S3_BUCKET or Jamf parameter 4)" >&2
  exit 1
fi

if [ -z "$AWS_REGION_VALUE" ]; then
  echo "AWS region is required (AWS_REGION or Jamf parameter 5)" >&2
  exit 1
fi

if [ ! -x "$VECTOR_BIN" ]; then
  echo "Vector binary not found or not executable at $VECTOR_BIN" >&2
  exit 1
fi

if [ ! -x "$WRAPPER" ]; then
  echo "S3 forwarder wrapper not found or not executable at $WRAPPER" >&2
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
    dir="$(dirname "$path")"
    if [ "$first" -eq 0 ]; then
      printf ','
    fi
    printf '%s/inventory_state.jsonl' "$dir"
    first=0
  done
  IFS="$old_ifs"
}

mkdir -p "$BASE_DIR" "$DATA_DIR" "$LAUNCHDAEMONS_DIR"
chmod 0755 "$BASE_DIR" "$DATA_DIR" "$LAUNCHDAEMONS_DIR" 2>/dev/null || true

RUNTIME_INCLUDES="$(include_array "$RUNTIME_LOG_PATHS")"
INVENTORY_LOG_PATHS="$(inventory_paths)"
INVENTORY_INCLUDES="$(include_array "$INVENTORY_LOG_PATHS")"

cat >"$CONFIG_PATH" <<EOF
# Managed by Beacon. Do not edit by hand.
# AWS credentials are resolved by Vector through the standard AWS provider chain.
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

[sinks.beacon_runtime_s3]
type = "aws_s3"
inputs = ["beacon_runtime_json"]
bucket = "\${BEACON_S3_BUCKET}"
region = "\${AWS_REGION}"
key_prefix = "\${BEACON_S3_PREFIX:-beacon}/runtime/date=%F/"
filename_time_format = "%s"
filename_append_uuid = true
filename_extension = "jsonl.gz"
compression = "gzip"
content_encoding = "gzip"
content_type = "application/x-ndjson"
storage_class = "\${BEACON_S3_STORAGE_CLASS:-STANDARD}"

[sinks.beacon_runtime_s3.encoding]
codec = "json"

[sinks.beacon_runtime_s3.framing]
method = "newline_delimited"

[sinks.beacon_runtime_s3.batch]
max_bytes = 10000000
timeout_secs = 300

[sinks.beacon_runtime_s3.request]
retry_attempts = 10
retry_initial_backoff_secs = 1
retry_max_duration_secs = 300

[sinks.beacon_inventory_s3]
type = "aws_s3"
inputs = ["beacon_inventory_json"]
bucket = "\${BEACON_S3_BUCKET}"
region = "\${AWS_REGION}"
key_prefix = "\${BEACON_S3_PREFIX:-beacon}/inventory/date=%F/"
filename_time_format = "%s"
filename_append_uuid = true
filename_extension = "jsonl.gz"
compression = "gzip"
content_encoding = "gzip"
content_type = "application/x-ndjson"
storage_class = "\${BEACON_S3_STORAGE_CLASS:-STANDARD}"

[sinks.beacon_inventory_s3.encoding]
codec = "json"

[sinks.beacon_inventory_s3.framing]
method = "newline_delimited"

[sinks.beacon_inventory_s3.batch]
max_bytes = 10000000
timeout_secs = 300

[sinks.beacon_inventory_s3.request]
retry_attempts = 10
retry_initial_backoff_secs = 1
retry_max_duration_secs = 300
EOF

chmod 0644 "$CONFIG_PATH"

{
  printf 'export BEACON_S3_BUCKET=%s\n' "$(shell_quote "$S3_BUCKET")"
  printf 'export AWS_REGION=%s\n' "$(shell_quote "$AWS_REGION_VALUE")"
  printf 'export BEACON_S3_PREFIX=%s\n' "$(shell_quote "$S3_PREFIX")"
  printf 'export BEACON_S3_STORAGE_CLASS=%s\n' "$(shell_quote "$S3_STORAGE_CLASS")"
  printf 'export BEACON_VECTOR_READ_FROM=%s\n' "$(shell_quote "$VECTOR_READ_FROM")"
  write_env_if_set AWS_ACCESS_KEY_ID
  write_env_if_set AWS_SECRET_ACCESS_KEY
  write_env_if_set AWS_SESSION_TOKEN
  write_env_if_set AWS_PROFILE
  write_env_if_set AWS_SHARED_CREDENTIALS_FILE
  write_env_if_set AWS_CONFIG_FILE
  write_env_if_set AWS_WEB_IDENTITY_TOKEN_FILE
  write_env_if_set AWS_ROLE_ARN
} >"$ENV_PATH"
chmod 0600 "$ENV_PATH"

cat >"$PLIST_PATH" <<EOF
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
chmod 0644 "$PLIST_PATH"

echo "S3 Vector config: $CONFIG_PATH"
echo "S3 Vector env: $ENV_PATH"
echo "S3 Vector LaunchDaemon: $PLIST_PATH"

case "$NO_START" in
  1|true|TRUE|yes|YES)
    echo "BEACON_NO_START is set; not loading $LABEL"
    exit 0
    ;;
esac

if command -v launchctl >/dev/null 2>&1; then
  launchctl bootout "system/$LABEL" >/dev/null 2>&1 || true
  launchctl bootstrap system "$PLIST_PATH" >/dev/null 2>&1 || {
    launchctl print "system/$LABEL" >/dev/null 2>&1 || exit 1
  }
  launchctl print "system/$LABEL" || true
else
  echo "launchctl unavailable; wrote config but did not start $LABEL" >&2
fi

if [ -f "/tmp/$LABEL.err" ]; then
  echo "Last S3 Vector stderr lines:"
  tail -n 20 "/tmp/$LABEL.err" || true
fi
