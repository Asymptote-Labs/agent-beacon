#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
builder="${OCB:-$(go env GOPATH)/bin/builder}"
targets_dir="${BEACON_COLLECTOR_TARGETS_DIR:-/tmp/beacon-otelcol-targets}"

cd "$script_dir"

rm -rf "$targets_dir"
mkdir -p "$targets_dir"

while read -r goos goarch; do
  target="${goos}_${goarch}"
  echo "Building collector for ${goos}/${goarch}"
  rm -rf dist/beacon-otelcol
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$builder" --config builder.yaml
  test -x dist/beacon-otelcol/beacon-otelcol
  mkdir -p "$targets_dir/$target"
  cp dist/beacon-otelcol/beacon-otelcol "$targets_dir/$target/beacon-otelcol"
done <<'TARGETS'
darwin amd64
darwin arm64
linux amd64
linux arm64
TARGETS

rm -rf dist/beacon-otelcol
mkdir -p dist/beacon-otelcol
cp -R "$targets_dir"/. dist/beacon-otelcol/
