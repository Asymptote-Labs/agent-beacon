#!/bin/sh
set -eu

VECTOR_BIN="${BEACON_VECTOR_BIN:-/opt/beacon/bin/vector}"
CONFIG_PATH="${BEACON_S3_VECTOR_CONFIG:-/Library/Application Support/Beacon/Forwarders/s3-vector.toml}"
ENV_PATH="${BEACON_S3_VECTOR_ENV:-/Library/Application Support/Beacon/Forwarders/s3-vector.env}"

if [ -f "$ENV_PATH" ]; then
  # shellcheck disable=SC1090
  . "$ENV_PATH"
fi

if [ ! -x "$VECTOR_BIN" ]; then
  echo "Vector binary not found or not executable at $VECTOR_BIN" >&2
  exit 1
fi

if [ ! -f "$CONFIG_PATH" ]; then
  echo "Vector config not found at $CONFIG_PATH" >&2
  exit 1
fi

exec "$VECTOR_BIN" --config "$CONFIG_PATH"
