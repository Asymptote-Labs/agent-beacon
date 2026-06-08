#!/bin/sh
set -eu

BASE_DIR="${BEACON_FORWARDER_BASE_DIR:-/Library/Application Support/Beacon/Forwarders}"
LAUNCHDAEMONS_DIR="${BEACON_LAUNCHDAEMONS_DIR:-/Library/LaunchDaemons}"
LABEL="${BEACON_FALCON_FORWARDER_LABEL:-com.beacon.endpoint.falcon-forwarder}"
CONFIG_PATH="${BEACON_FALCON_VECTOR_CONFIG:-$BASE_DIR/falcon-vector.toml}"
ENV_PATH="${BEACON_FALCON_VECTOR_ENV:-$BASE_DIR/falcon-vector.env}"
PLIST_PATH="$LAUNCHDAEMONS_DIR/$LABEL.plist"

if [ ! -f "$CONFIG_PATH" ]; then
  echo "<result>missing_config</result>"
  exit 0
fi

if [ ! -f "$ENV_PATH" ]; then
  echo "<result>missing_env</result>"
  exit 0
fi

if [ ! -f "$PLIST_PATH" ]; then
  echo "<result>missing_plist</result>"
  exit 0
fi

if ! grep -q '^export BEACON_FALCON_HEC_ENDPOINT=' "$ENV_PATH"; then
  echo "<result>missing_endpoint</result>"
  exit 0
fi

if ! grep -q '^export BEACON_FALCON_HEC_TOKEN=' "$ENV_PATH"; then
  echo "<result>missing_token</result>"
  exit 0
fi

if ! grep -q 'sinks.falcon_hec' "$CONFIG_PATH"; then
  echo "<result>missing_sink</result>"
  exit 0
fi

echo "<result>configured</result>"
