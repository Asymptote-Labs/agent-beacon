#!/bin/sh
set -eu
export COPYFILE_DISABLE=1

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
PKG_BUILD_SCRIPT="$ROOT_DIR/packaging/macos/build-pkg.sh"
OUT_DIR="${OUT_DIR:-}"
SKIP_VERIFY=0
BUILD_LOG="$(mktemp)"
trap 'rm -f "$BUILD_LOG"' EXIT INT TERM

usage() {
  cat <<'USAGE'
Usage:
  BEACON_APP_SIGN_IDENTITY="Developer ID Application: Example Corp (TEAMID)" \
  PKG_SIGN_IDENTITY="Developer ID Installer: Example Corp (TEAMID)" \
  NOTARYTOOL_PROFILE="beacon-notary-profile" \
    sh packaging/macos/build-signed-notarized-pkg.sh [options]

Options:
  --out-dir DIR       Write the package to DIR instead of dist/macos.
  --version VERSION   Override the package version.
  --skip-verify       Skip pkgutil, stapler, and spctl verification.
  -h, --help          Show this help.

The script builds a fresh package so payload binaries can be signed before they
are sealed into the installer. Configure notarytool once with:

  xcrun notarytool store-credentials beacon-notary-profile \
    --apple-id you@example.com --team-id TEAMID --password "$APP_SPECIFIC_PASSWORD"
USAGE
}

die() {
  echo "error: $*" >&2
  exit 1
}

require_env() {
  name="$1"
  eval "value=\${$name:-}"
  if [ -z "$value" ]; then
    die "$name is required"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --out-dir)
      [ "$#" -ge 2 ] || die "--out-dir requires a value"
      OUT_DIR="$2"
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      BEACON_VERSION="$2"
      export BEACON_VERSION
      shift 2
      ;;
    --skip-verify)
      SKIP_VERIFY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

if [ "$(uname -s)" != "Darwin" ]; then
  die "Apple signing and notarization must run on macOS"
fi

require_env BEACON_APP_SIGN_IDENTITY
require_env PKG_SIGN_IDENTITY
require_env NOTARYTOOL_PROFILE

require_command codesign
require_command pkgbuild
require_command pkgutil
require_command shasum
require_command spctl
require_command xcrun
xcrun --find notarytool >/dev/null 2>&1 || die "xcrun notarytool is required"
xcrun --find stapler >/dev/null 2>&1 || die "xcrun stapler is required"

echo "building, signing, notarizing, and stapling macOS package..."
if [ -n "$OUT_DIR" ]; then
  export OUT_DIR
fi

if ! sh "$PKG_BUILD_SCRIPT" >"$BUILD_LOG" 2>&1; then
  sed -n '1,$p' "$BUILD_LOG" >&2
  die "package build failed"
fi
PKG_OUTPUT="$(sed -n '1,$p' "$BUILD_LOG")"
printf '%s\n' "$PKG_OUTPUT"
PKG_PATH="$(printf '%s\n' "$PKG_OUTPUT" | sed -n '$p')"
[ -n "$PKG_PATH" ] || die "package build did not report an output path"
[ -f "$PKG_PATH" ] || die "package not found: $PKG_PATH"

if [ "$SKIP_VERIFY" -eq 0 ]; then
  echo "verifying package signature..."
  pkgutil --check-signature "$PKG_PATH"

  echo "validating stapled notarization ticket..."
  xcrun stapler validate "$PKG_PATH"

  echo "checking Gatekeeper install assessment..."
  spctl -a -vv -t install "$PKG_PATH"
fi

shasum -a 256 "$PKG_PATH" > "$PKG_PATH.sha256"
echo "signed and notarized package: $PKG_PATH"
echo "checksum: $PKG_PATH.sha256"
