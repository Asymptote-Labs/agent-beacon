#!/bin/sh
# Generate the package update manifest (update-manifest.json) for a release.
#
# Usage:
#   gen-update-manifest.sh VERSION TAG REPO TEAM_ID \
#     darwin_arm64=/path/BeaconEndpointAgent-1.0.6-arm64.pkg \
#     linux_amd64=/path/beacon_1.0.6_linux_amd64.deb \
#     linux_arm64=/path/beacon_1.0.6_linux_arm64.deb
#
# Keys are GOOS_GOARCH, matching updatecheck.RuntimeArchKey() on the consumer side. A bare arch
# (arm64=...) is still accepted and treated as darwin, so existing release tooling keeps working.
#
# Lives under packaging/release/ rather than packaging/macos/ because it now describes artifacts
# for both platforms.
#
# team_id and pkg_identifier are Apple concepts with no Linux analogue. They stay in the manifest
# for the macOS notarization check; a Linux artifact is verified by SHA-256, which is enforced on
# every platform regardless.
#
# Emits JSON to stdout. Download URLs point at the GitHub release assets for TAG.
set -eu

if [ "$#" -lt 5 ]; then
  echo "usage: $0 VERSION TAG REPO TEAM_ID goos_arch=pkgpath [goos_arch=pkgpath ...]" >&2
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
  # shasum is macOS; sha256sum is Linux. The release job may run on either.
  if command -v shasum >/dev/null 2>&1; then
    sha="$(shasum -a 256 "$pkg" | awk '{print $1}')"
  else
    sha="$(sha256sum "$pkg" | awk '{print $1}')"
  fi
  base="$(basename "$pkg")"
  url="https://github.com/$REPO/releases/download/$TAG/$base"
  # A key without a platform means darwin, for compatibility with the original contract.
  case "$arch" in
    *_*) key="$arch" ;;
    *)   key="darwin_$arch" ;;
  esac
  entry="\"$key\":{\"url\":\"$url\",\"sha256\":\"$sha\"}"
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
