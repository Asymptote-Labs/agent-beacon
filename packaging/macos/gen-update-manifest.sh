#!/bin/sh
# Generate the package update manifest (update-manifest.json) for a release.
#
# Usage:
#   gen-update-manifest.sh VERSION TAG REPO TEAM_ID \
#     arm64=/path/Beacon-...-arm64.pkg amd64=/path/Beacon-...-amd64.pkg
#
# Emits JSON to stdout. Download URLs point at the GitHub release assets for TAG.
set -eu

if [ "$#" -lt 5 ]; then
  echo "usage: $0 VERSION TAG REPO TEAM_ID arch=pkgpath [arch=pkgpath ...]" >&2
  exit 2
fi

VERSION="$1"; TAG="$2"; REPO="$3"; TEAM_ID="$4"
shift 4

MIN_SUPPORTED="${BEACON_MIN_SUPPORTED_VERSION:-}"
PKG_IDENTIFIER="${PKG_IDENTIFIER:-ai.asymptote.beacon.endpoint}"

artifacts=""
for pair in "$@"; do
  arch="${pair%%=*}"
  pkg="${pair#*=}"
  if [ ! -f "$pkg" ]; then
    echo "package not found: $pkg" >&2
    exit 1
  fi
  sha="$(shasum -a 256 "$pkg" | awk '{print $1}')"
  base="$(basename "$pkg")"
  url="https://github.com/$REPO/releases/download/$TAG/$base"
  entry="\"darwin_$arch\":{\"url\":\"$url\",\"sha256\":\"$sha\"}"
  if [ -z "$artifacts" ]; then
    artifacts="$entry"
  else
    artifacts="$artifacts,$entry"
  fi
done

min_field=""
if [ -n "$MIN_SUPPORTED" ]; then
  min_field="\"min_supported_version\":\"$MIN_SUPPORTED\","
fi

printf '{"schema":1,"version":"%s",%s"team_id":"%s","pkg_identifier":"%s","artifacts":{%s}}\n' \
  "$VERSION" "$min_field" "$TEAM_ID" "$PKG_IDENTIFIER" "$artifacts"
