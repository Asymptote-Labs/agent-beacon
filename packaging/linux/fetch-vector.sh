#!/bin/sh
# Downloads the Vector build that Beacon's Linux packages and archives ship, checks it, and stages
# it where goreleaser picks it up.
#
#   packaging/linux/fetch-vector.sh [out-dir] [arch...]
#
# out-dir defaults to cli/beacon/release-vector and arch to "amd64 arm64". Each binary lands at
# <out-dir>/linux_<arch>/beacon-vector. The .deb and .rpm install it as /opt/beacon/bin/vector;
# the release archive keeps the beacon-vector name so unpacking it into a directory on PATH never
# shadows a Vector the user already has.
#
# The musl build, not the gnu one. The gnu build links glibc dynamically, and every Linux binary
# Beacon ships is static so it runs on stripped and custom-built hosts without caring which libc
# they carry. The script fails unless the binary has no program interpreter.
#
# Set VECTOR_VERSION to change the version. Keep it in step with the tap's beacon-vector formula.
set -eu

VECTOR_VERSION="${VECTOR_VERSION:-0.58.0}"
ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
OUT_DIR="${1:-$ROOT_DIR/cli/beacon/release-vector}"
[ "$#" -gt 0 ] && shift
ARCHES="${*:-amd64 arm64}"
BASE_URL="https://packages.timber.io/vector/${VECTOR_VERSION}"

for tool in curl tar; do
  command -v "$tool" >/dev/null 2>&1 || { echo "fetch-vector: $tool is required" >&2; exit 1; }
done
command -v readelf >/dev/null 2>&1 || command -v file >/dev/null 2>&1 || {
  echo "fetch-vector: readelf or file is required to check the binary is static" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  sha256_check() { sha256sum -c -; }
else
  sha256_check() { shasum -a 256 -c -; }
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
sums="vector-${VECTOR_VERSION}-SHA256SUMS"
curl -fsSL --retry 3 -o "$work/$sums" "$BASE_URL/$sums"

for arch in $ARCHES; do
  case "$arch" in
    amd64) triple=x86_64-unknown-linux-musl ;;
    arm64) triple=aarch64-unknown-linux-musl ;;
    *) echo "fetch-vector: unsupported arch $arch" >&2; exit 1 ;;
  esac
  archive="vector-${VECTOR_VERSION}-${triple}.tar.gz"
  curl -fsSL --retry 3 -o "$work/$archive" "$BASE_URL/$archive"
  line="$(awk -v a="$archive" '$2 == a { print }' "$work/$sums")"
  [ -n "$line" ] || { echo "fetch-vector: no checksum for $archive in $sums" >&2; exit 1; }
  (cd "$work" && printf '%s\n' "$line" | sha256_check)

  mkdir -p "$work/$arch"
  tar -xzf "$work/$archive" -C "$work/$arch"
  bin="$(find "$work/$arch" -path '*/bin/vector' -type f | head -n 1)"
  [ -n "$bin" ] || { echo "fetch-vector: no bin/vector in $archive" >&2; exit 1; }

  # A dynamic binary has an INTERP program header and a static one does not. The headers must be
  # read successfully and include a LOAD segment first, or a file readelf cannot parse would pass.
  # Without readelf (macOS), file(1) has to say static outright; anything else fails.
  if command -v readelf >/dev/null 2>&1; then
    headers="$(readelf -lW "$bin")" || { echo "fetch-vector: readelf could not read $bin" >&2; exit 1; }
    case "$headers" in
      *LOAD*) ;;
      *) echo "fetch-vector: no program headers in $bin" >&2; exit 1 ;;
    esac
    case "$headers" in
      *INTERP*)
        echo "fetch-vector: $archive is dynamically linked; Beacon ships only static Linux binaries" >&2
        exit 1 ;;
    esac
  else
    kind="$(file -b "$bin")"
    case "$kind" in
      ELF*"static-pie linked"*|ELF*"statically linked"*) ;;
      *) echo "fetch-vector: $archive is not a static ELF binary: $kind" >&2; exit 1 ;;
    esac
  fi

  mkdir -p "$OUT_DIR/linux_$arch"
  cp "$bin" "$OUT_DIR/linux_$arch/beacon-vector"
  chmod 755 "$OUT_DIR/linux_$arch/beacon-vector"
  echo "staged $OUT_DIR/linux_$arch/beacon-vector (Vector $VECTOR_VERSION, $triple, static)"
done
