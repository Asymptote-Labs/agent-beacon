#!/bin/sh
set -eu

LABEL="${BEACON_FALCON_FORWARDER_LABEL:-com.beacon.endpoint.falcon-forwarder}"
VECTOR_BIN="${BEACON_VECTOR_BIN:-/opt/beacon/bin/vector}"
WRAPPER="${BEACON_FALCON_FORWARDER_WRAPPER:-/opt/beacon/scripts/run-falcon-forwarder.sh}"
BASE_DIR="${BEACON_FORWARDER_BASE_DIR:-/Library/Application Support/Beacon/Forwarders}"
LAUNCHDAEMONS_DIR="${BEACON_LAUNCHDAEMONS_DIR:-/Library/LaunchDaemons}"
CONFIG_PATH="${BEACON_FALCON_VECTOR_CONFIG:-$BASE_DIR/falcon-vector.toml}"
ENV_PATH="${BEACON_FALCON_VECTOR_ENV:-$BASE_DIR/falcon-vector.env}"
DATA_DIR="${BEACON_FALCON_VECTOR_DATA_DIR:-$BASE_DIR/vector-data/falcon}"
PLIST_PATH="$LAUNCHDAEMONS_DIR/$LABEL.plist"

FALCON_HEC_ENDPOINT="${BEACON_FALCON_HEC_ENDPOINT:-${4:-}}"
FALCON_HEC_TOKEN="${BEACON_FALCON_HEC_TOKEN:-${5:-}}"
FALCON_SOURCE="${BEACON_FALCON_SOURCE:-${6:-beacon-endpoint-agent}}"
FALCON_SOURCETYPE="${BEACON_FALCON_SOURCETYPE:-${7:-json}}"
FALCON_INDEX="${BEACON_FALCON_INDEX:-${8:-}}"
RUNTIME_LOG_PATHS="${BEACON_RUNTIME_LOG_PATHS:-${9:-/var/log/beacon-agent/runtime.jsonl,/Users/*/.beacon/endpoint/logs/runtime.jsonl}}"
VECTOR_READ_FROM="${BEACON_VECTOR_READ_FROM:-${10:-end}}"
NO_START="${BEACON_NO_START:-0}"

if [ -z "$FALCON_HEC_ENDPOINT" ]; then
  echo "Falcon HEC endpoint is required (BEACON_FALCON_HEC_ENDPOINT or Jamf parameter 4)" >&2
  exit 1
fi

if [ -z "$FALCON_HEC_TOKEN" ]; then
  echo "Falcon HEC token is required (BEACON_FALCON_HEC_TOKEN or Jamf parameter 5)" >&2
  exit 1
fi

if [ ! -x "$VECTOR_BIN" ]; then
  echo "Vector binary not found or not executable at $VECTOR_BIN" >&2
  exit 1
fi

if [ ! -x "$WRAPPER" ]; then
  echo "Falcon forwarder wrapper not found or not executable at $WRAPPER" >&2
  exit 1
fi

"$VECTOR_BIN" --version >/dev/null

shell_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

toml_quote() {
  printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"
}

xml_escape() {
  printf '%s' "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'
}

include_array() {
  old_ifs="$IFS"
  IFS=,
  first=1
  printf '['
  for raw in $RUNTIME_LOG_PATHS; do
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

mkdir -p "$BASE_DIR" "$DATA_DIR" "$LAUNCHDAEMONS_DIR"
chmod 0755 "$BASE_DIR" "$DATA_DIR" "$LAUNCHDAEMONS_DIR" 2>/dev/null || true

INCLUDES="$(include_array)"

cat >"$CONFIG_PATH" <<EOF
# Managed by Beacon. Do not edit by hand.
data_dir = "$(printf '%s' "$DATA_DIR" | sed 's/\\/\\\\/g; s/"/\\"/g')"

[sources.beacon_runtime]
type = "file"
include = $INCLUDES
read_from = "\${BEACON_VECTOR_READ_FROM:-end}"

[transforms.beacon_json]
type = "remap"
inputs = ["beacon_runtime"]
source = '''
event = parse_json!(.message)
ts = parse_timestamp(event.timestamp, format: "%+") ?? now()
event."@timestamp" = format_timestamp!(ts, format: "%+")

payload = {
  "time": to_unix_timestamp(ts),
  "event": event,
  "source": get_env_var("BEACON_FALCON_SOURCE") ?? "beacon-endpoint-agent",
  "sourcetype": get_env_var("BEACON_FALCON_SOURCETYPE") ?? "json",
}

index = get_env_var("BEACON_FALCON_INDEX") ?? ""
if index != "" {
  payload.index = index
}

. = payload
'''

[sinks.falcon_hec]
type = "http"
inputs = ["beacon_json"]
uri = "\${BEACON_FALCON_HEC_ENDPOINT}"
method = "post"

[sinks.falcon_hec.encoding]
codec = "json"

[sinks.falcon_hec.framing]
method = "newline_delimited"

[sinks.falcon_hec.batch]
max_events = 500
timeout_secs = 5

[sinks.falcon_hec.request]
retry_attempts = 10
retry_initial_backoff_secs = 1
retry_max_duration_secs = 300

[sinks.falcon_hec.request.headers]
Authorization = "Bearer \${BEACON_FALCON_HEC_TOKEN}"
Content-Type = "text/plain; charset=utf-8"
EOF

chmod 0644 "$CONFIG_PATH"

{
  printf 'export BEACON_FALCON_HEC_ENDPOINT=%s\n' "$(shell_quote "$FALCON_HEC_ENDPOINT")"
  printf 'export BEACON_FALCON_HEC_TOKEN=%s\n' "$(shell_quote "$FALCON_HEC_TOKEN")"
  printf 'export BEACON_FALCON_SOURCE=%s\n' "$(shell_quote "$FALCON_SOURCE")"
  printf 'export BEACON_FALCON_SOURCETYPE=%s\n' "$(shell_quote "$FALCON_SOURCETYPE")"
  printf 'export BEACON_FALCON_INDEX=%s\n' "$(shell_quote "$FALCON_INDEX")"
  printf 'export BEACON_VECTOR_READ_FROM=%s\n' "$(shell_quote "$VECTOR_READ_FROM")"
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

echo "Falcon Vector config: $CONFIG_PATH"
echo "Falcon Vector env: $ENV_PATH"
echo "Falcon Vector LaunchDaemon: $PLIST_PATH"

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
  echo "Last Falcon Vector stderr lines:"
  tail -n 20 "/tmp/$LABEL.err" || true
fi
