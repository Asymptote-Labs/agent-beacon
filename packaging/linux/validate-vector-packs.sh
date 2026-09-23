#!/bin/sh
# Renders every Vector content pack Beacon generates and runs `vector validate` on each, with the
# Vector binary Beacon ships. Fails if any pack does not load.
#
#   packaging/linux/validate-vector-packs.sh <vector-binary> [beacon command...]
#
# Without a beacon command, the CLI is built from this checkout. Destinations and credentials are dummy but
# well-formed: validate needs URIs it can parse, not endpoints it can reach, and health checks are
# skipped. Exists because Vector releases have changed how ${VAR} in a config expands, which broke
# packs without any Beacon change.
set -eu

VECTOR="${1:?usage: validate-vector-packs.sh <vector-binary> [beacon command...]}"
shift
ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

if [ "$#" -eq 0 ]; then
  (cd "$ROOT_DIR/cli/beacon" && go build -o "$work/beacon" .)
  set -- "$work/beacon"
fi

export AWS_REGION=us-east-1 \
  BEACON_ASYMPTOTE_DATA_DIR="$work/data" \
  BEACON_ASYMPTOTE_INGEST_URL=https://ingest.invalid \
  BEACON_ASYMPTOTE_SECRETS_FILE="$work/secrets.json" \
  BEACON_CLOUDWATCH_LOG_GROUP=beacon \
  BEACON_FALCON_HEC_ENDPOINT=https://falcon.invalid/services/collector \
  BEACON_FALCON_HEC_TOKEN=placeholder \
  BEACON_GCS_BUCKET=bucket BEACON_S3_BUCKET=bucket \
  RAPID7_WEBHOOK_URL=https://rapid7.invalid/hook \
  SUMO_URL=https://sumo.invalid/receiver SUMO_TOKEN=placeholder \
  BEACON_SENTINEL_DCE_ENDPOINT=https://dce.eastus-1.ingest.monitor.azure.com \
  BEACON_SENTINEL_DCR_IMMUTABLE_ID=dcr-00000000000000000000000000000000 \
  AZURE_TENANT_ID=00000000-0000-0000-0000-000000000000 \
  AZURE_CLIENT_ID=00000000-0000-0000-0000-000000000000 \
  AZURE_CLIENT_SECRET=placeholder
mkdir -p "$work/data"
printf '{"device_key":"bcn_device_placeholder"}\n' > "$work/secrets.json"

"$VECTOR" --version
failed=0
checked=0
for pack in asymptote cloudwatch falcon gcs rapid7 s3 sentinel sumo; do
  "$@" endpoint "$pack" install-pack --output "$work/$pack" --log-path "$work/runtime.jsonl" >/dev/null
  [ -f "$work/$pack/vector.toml" ] || continue
  checked=$((checked + 1))
  if out="$("$VECTOR" validate --no-environment --skip-healthchecks "$work/$pack/vector.toml" 2>&1)"; then
    echo "ok: $pack"
  else
    echo "FAIL: $pack" >&2
    printf '%s\n' "$out" | grep -E '^x ' >&2 || printf '%s\n' "$out" >&2
    failed=1
  fi
done
# A pack list that silently matched nothing would pass. Six packs carried a vector.toml when this
# was written.
[ "$checked" -ge 6 ] || { echo "only $checked packs had a vector.toml; expected at least 6" >&2; exit 1; }
exit "$failed"
