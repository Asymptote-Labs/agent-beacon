#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
builder="${OCB:-$(go env GOPATH)/bin/builder}"
targets_dir="${BEACON_COLLECTOR_TARGETS_DIR:-/tmp/beacon-otelcol-targets}"

cd "$script_dir"

rm -rf "$targets_dir"
mkdir -p "$targets_dir" dist

while read -r goos goarch; do
  target="${goos}_${goarch}"
  echo "Building collector for ${goos}/${goarch}"
  rm -rf dist/beacon-otelcol
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$builder" --config builder.yaml

  # Detected rather than assumed. The builder names its output from dist.name verbatim and does
  # not append .exe for windows, even though what it produces there is a PE binary -- an earlier
  # version of this script asserted the .exe name and failed the build immediately after a
  # successful compile. Both spellings are accepted so a future builder version that does append
  # it keeps working.
  produced="dist/beacon-otelcol/beacon-otelcol"
  if [ ! -f "$produced" ] && [ -f "${produced}.exe" ]; then
    produced="${produced}.exe"
  fi
  if [ ! -f "$produced" ]; then
    echo "collector build for ${goos}/${goarch} produced no binary at dist/beacon-otelcol/" >&2
    ls -la dist/beacon-otelcol/ >&2 || true
    exit 1
  fi

  # Staged with the extension the *target* needs, which is not the same question as what the
  # builder emitted: Windows requires .exe to execute the file, so the release artifact carries it
  # regardless of how it arrived.
  staged="beacon-otelcol"
  if [ "$goos" = "windows" ]; then
    staged="beacon-otelcol.exe"
  fi
  mkdir -p "$targets_dir/$target"
  cp "$produced" "$targets_dir/$target/$staged"
done <<'TARGETS'
darwin amd64
darwin arm64
linux amd64
linux arm64
windows amd64
TARGETS

rm -rf dist/beacon-otelcol
mkdir -p dist/beacon-otelcol
cp -R "$targets_dir"/. dist/beacon-otelcol/
