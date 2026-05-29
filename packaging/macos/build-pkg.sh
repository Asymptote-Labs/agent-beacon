#!/bin/sh
set -eu
export COPYFILE_DISABLE=1

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/dist/macos}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT INT TERM

VERSION="${BEACON_VERSION:-$(cd "$ROOT_DIR" && git describe --tags --always --dirty 2>/dev/null || echo dev)}"
PKG_IDENTIFIER="${PKG_IDENTIFIER:-ai.asymptote.beacon.endpoint}"
PKG_NAME="${PKG_NAME:-BeaconEndpointAgent}"
BEACON_BIN="${BEACON_BIN:-$ROOT_DIR/cli/beacon/beacon}"
COLLECTOR_TARGET="${BEACON_COLLECTOR_TARGET:-$(go env GOOS)_$(go env GOARCH)}"
COLLECTOR_BIN="${BEACON_COLLECTOR:-$ROOT_DIR/collector-builder/dist/beacon-otelcol/$COLLECTOR_TARGET/beacon-otelcol}"
PKG_ROOT="$WORK_DIR/pkgroot"
PKG_SCRIPTS="$WORK_DIR/scripts"
PKG_PATH="$OUT_DIR/$PKG_NAME-$VERSION.pkg"

copy_file() {
  if cp -X "$1" "$2" 2>/dev/null; then
    return 0
  fi
  cp "$1" "$2"
}

if [ ! -x "$BEACON_BIN" ]; then
  echo "beacon binary not found or not executable: $BEACON_BIN" >&2
  echo "Build it first, for example: (cd cli/beacon && make build)" >&2
  exit 1
fi

if [ ! -x "$COLLECTOR_BIN" ]; then
  echo "beacon-otelcol binary not found or not executable: $COLLECTOR_BIN" >&2
  echo "Build it first with the OpenTelemetry Collector Builder using collector-builder/builder.yaml." >&2
  exit 1
fi

mkdir -p "$OUT_DIR" "$PKG_ROOT/opt/beacon/bin" "$PKG_ROOT/opt/beacon/scripts" \
  "$PKG_ROOT/opt/beacon/jamf" "$PKG_ROOT/opt/beacon/fleet" "$PKG_SCRIPTS"

copy_file "$BEACON_BIN" "$PKG_ROOT/opt/beacon/bin/beacon"
copy_file "$COLLECTOR_BIN" "$PKG_ROOT/opt/beacon/bin/beacon-otelcol"
copy_file "$ROOT_DIR/packaging/macos/install-endpoint.sh" "$PKG_ROOT/opt/beacon/scripts/install-endpoint.sh"
copy_file "$ROOT_DIR/packaging/macos/uninstall-endpoint.sh" "$PKG_ROOT/opt/beacon/scripts/uninstall-endpoint.sh"
copy_file "$ROOT_DIR/packaging/macos/scripts/postinstall" "$PKG_SCRIPTS/postinstall"
copy_file "$ROOT_DIR/packaging/macos/scripts/preinstall" "$PKG_SCRIPTS/preinstall"

if command -v ditto >/dev/null 2>&1; then
  ditto --norsrc --noextattr --noqtn "$ROOT_DIR/packaging/macos/jamf" "$PKG_ROOT/opt/beacon/jamf"
  ditto --norsrc --noextattr --noqtn "$ROOT_DIR/packaging/macos/fleet" "$PKG_ROOT/opt/beacon/fleet"
else
  cp -R "$ROOT_DIR/packaging/macos/jamf/." "$PKG_ROOT/opt/beacon/jamf/"
  cp -R "$ROOT_DIR/packaging/macos/fleet/." "$PKG_ROOT/opt/beacon/fleet/"
fi

if [ -d "$ROOT_DIR/cli/beacon/internal/endpoint/wazuh/pack" ]; then
  mkdir -p "$PKG_ROOT/opt/beacon/wazuh"
  if command -v ditto >/dev/null 2>&1; then
    ditto --norsrc --noextattr --noqtn "$ROOT_DIR/cli/beacon/internal/endpoint/wazuh/pack" "$PKG_ROOT/opt/beacon/wazuh"
  else
    cp -R "$ROOT_DIR/cli/beacon/internal/endpoint/wazuh/pack/." "$PKG_ROOT/opt/beacon/wazuh/"
  fi
fi

find "$PKG_ROOT" -name '._*' -delete

chmod 755 "$PKG_ROOT/opt/beacon/bin/beacon" "$PKG_ROOT/opt/beacon/bin/beacon-otelcol"
chmod 755 "$PKG_ROOT/opt/beacon/scripts/install-endpoint.sh" "$PKG_ROOT/opt/beacon/scripts/uninstall-endpoint.sh"
find "$PKG_ROOT/opt/beacon/jamf" -type f -name '*.sh' -exec chmod 755 {} \;
find "$PKG_ROOT/opt/beacon/fleet" -type f -name '*.sh' -exec chmod 755 {} \;
chmod 755 "$PKG_SCRIPTS/postinstall" "$PKG_SCRIPTS/preinstall"
find "$PKG_ROOT" -name '._*' -delete
if command -v xattr >/dev/null 2>&1; then
  xattr -cr "$PKG_ROOT" "$PKG_SCRIPTS" 2>/dev/null || true
fi
if command -v dot_clean >/dev/null 2>&1; then
  dot_clean -m "$PKG_ROOT" "$PKG_SCRIPTS" 2>/dev/null || true
fi
find "$PKG_ROOT" -name '._*' -delete

if [ -n "${PKG_SIGN_IDENTITY:-}" ]; then
  pkgbuild \
    --root "$PKG_ROOT" \
    --scripts "$PKG_SCRIPTS" \
    --identifier "$PKG_IDENTIFIER" \
    --version "$VERSION" \
    --install-location / \
    --filter '(^|/)\._[^/]*$' \
    --filter '.*\._.*' \
    --filter '/\.DS_Store$' \
    --filter '(^|/)CVS($|/)' \
    --filter '(^|/)\.svn($|/)' \
    --sign "$PKG_SIGN_IDENTITY" \
    "$PKG_PATH"
else
  pkgbuild \
    --root "$PKG_ROOT" \
    --scripts "$PKG_SCRIPTS" \
    --identifier "$PKG_IDENTIFIER" \
    --version "$VERSION" \
    --install-location / \
    --filter '(^|/)\._[^/]*$' \
    --filter '.*\._.*' \
    --filter '/\.DS_Store$' \
    --filter '(^|/)CVS($|/)' \
    --filter '(^|/)\.svn($|/)' \
    "$PKG_PATH"
fi

if [ -n "${NOTARYTOOL_PROFILE:-}" ]; then
  xcrun notarytool submit "$PKG_PATH" --keychain-profile "$NOTARYTOOL_PROFILE" --wait
  xcrun stapler staple "$PKG_PATH"
fi

shasum -a 256 "$PKG_PATH" > "$PKG_PATH.sha256"
echo "$PKG_PATH"
