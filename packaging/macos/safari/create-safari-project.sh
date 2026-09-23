#!/bin/sh
# Generate the Xcode app wrapper for the Safari build of the Beacon browser
# collector (browser-extension/) with Apple's Safari web extension packager.
#
# Safari has no "load unpacked": a Safari web extension ships inside a macOS app
# bundle, and that app is built with Xcode. This script wraps
# `xcrun safari-web-extension-packager` (formerly `safari-web-extension-converter`;
# both names are tried) so the bundle identifiers and flags are the same on every
# machine, and so the generated project is disposable: regenerate it from a fresh
# extension build instead of committing and hand-editing it.
#
# It does not sign, notarize, or build the app. See packaging/macos/safari/README.md.
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)"

EXTENSION_DIR="${SAFARI_EXTENSION_DIR:-$ROOT_DIR/browser-extension/dist-safari}"
PROJECT_DIR="${SAFARI_PROJECT_DIR:-$ROOT_DIR/dist/safari}"
APP_NAME="${SAFARI_APP_NAME:-Beacon Browser Collector}"
BUNDLE_IDENTIFIER="${SAFARI_BUNDLE_IDENTIFIER:-ai.asymptote.beacon.browser-collector}"
PLATFORM="macos"
COPY_RESOURCES=1
FORCE=0
DRY_RUN=0

# The bundles and static files a loadable build must contain. Kept in step with
# the "Verify the build produced a loadable MV3 extension" step in ci.yml.
REQUIRED_FILES="manifest.json sw.js content.js interceptor.js popup.html popup.js options.html options.js"

usage() {
  cat <<'USAGE'
Usage:
  sh packaging/macos/safari/create-safari-project.sh [options]

Build the extension first (cd browser-extension && npm ci && npm run build:safari),
then run this on a Mac with Xcode installed.

Options:
  --extension-dir DIR    Built extension to wrap (default: browser-extension/dist-safari).
  --project-dir DIR      Directory the Xcode project is written under
                         (default: dist/safari). The packager creates
                         DIR/<app name>/.
  --app-name NAME        App and Xcode project name
                         (default: "Beacon Browser Collector").
  --bundle-id ID         App bundle identifier
                         (default: ai.asymptote.beacon.browser-collector).
                         The packager derives the extension's identifier from it.
  --platform PLATFORM    macos (default), ios, or all.
  --reference-resources  Reference the extension files in place instead of
                         copying them into the project. Handy for a local edit,
                         rebuild, Product > Build loop; not for release builds.
  --force                Overwrite an existing project directory.
  --dry-run              Validate the inputs and print the packager command
                         without running it. Works on any OS.
  -h, --help             Show this help.

Environment defaults: SAFARI_EXTENSION_DIR, SAFARI_PROJECT_DIR, SAFARI_APP_NAME,
SAFARI_BUNDLE_IDENTIFIER.
USAGE
}

die() {
  echo "error: $*" >&2
  exit 1
}

# Print one argument quoted for a POSIX shell, so --dry-run output can be pasted.
quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

require_value() {
  [ "$2" -ge 2 ] || die "$1 requires a value"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --extension-dir)
      require_value "$1" "$#"
      EXTENSION_DIR="$2"
      shift 2
      ;;
    --project-dir)
      require_value "$1" "$#"
      PROJECT_DIR="$2"
      shift 2
      ;;
    --app-name)
      require_value "$1" "$#"
      APP_NAME="$2"
      shift 2
      ;;
    --bundle-id)
      require_value "$1" "$#"
      BUNDLE_IDENTIFIER="$2"
      shift 2
      ;;
    --platform)
      require_value "$1" "$#"
      PLATFORM="$2"
      shift 2
      ;;
    --reference-resources)
      COPY_RESOURCES=0
      shift
      ;;
    --force)
      FORCE=1
      shift
      ;;
    --dry-run)
      DRY_RUN=1
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

# --- Validate inputs (on every OS, so --dry-run exercises the same checks) ---

case "$PLATFORM" in
  macos|ios|all) ;;
  *) die "--platform must be macos, ios, or all (got '$PLATFORM')" ;;
esac

[ -n "$APP_NAME" ] || die "--app-name must not be empty"
case "$APP_NAME" in
  */*) die "--app-name must not contain '/' (got '$APP_NAME')" ;;
esac

# Apple bundle identifiers: reverse-DNS, ASCII letters, digits, hyphens, periods.
if ! printf '%s\n' "$BUNDLE_IDENTIFIER" | grep -Eq '^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$'; then
  die "--bundle-id must be a reverse-DNS identifier of letters, digits, hyphens, and periods (got '$BUNDLE_IDENTIFIER')"
fi

[ -d "$EXTENSION_DIR" ] || die "extension directory not found: $EXTENSION_DIR (run 'npm ci && npm run build:safari' in browser-extension first)"
EXTENSION_DIR="$(CDPATH= cd -- "$EXTENSION_DIR" && pwd)"

for f in $REQUIRED_FILES; do
  [ -s "$EXTENSION_DIR/$f" ] || die "missing or empty $f in $EXTENSION_DIR; is this a complete extension build?"
done
if ! grep -Eq '"manifest_version"[[:space:]]*:[[:space:]]*3([^0-9]|$)' "$EXTENSION_DIR/manifest.json"; then
  die "$EXTENSION_DIR/manifest.json is not a Manifest V3 manifest"
fi
if [ -n "$(find "$EXTENSION_DIR" -name '*.map' -print 2>/dev/null | head -n 1)" ]; then
  echo "warning: $EXTENSION_DIR contains sourcemaps (*.map); delete them before a release build, as release-extension.yml does for the Chrome zip" >&2
fi

# Make the project path absolute without creating it: validation and --dry-run
# have no side effects.
if [ -d "$PROJECT_DIR" ]; then
  PROJECT_DIR="$(CDPATH= cd -- "$PROJECT_DIR" && pwd)"
else
  case "$PROJECT_DIR" in
    /*) ;;
    *) PROJECT_DIR="$(pwd)/$PROJECT_DIR" ;;
  esac
fi
case "$PROJECT_DIR/" in
  "$EXTENSION_DIR"/*) die "--project-dir must not be inside the extension directory ($EXTENSION_DIR); the packager would wrap its own output" ;;
esac

OUTPUT_DIR="$PROJECT_DIR/$APP_NAME"
if [ -e "$OUTPUT_DIR" ] && [ "$FORCE" -ne 1 ]; then
  die "$OUTPUT_DIR already exists; pass --force to overwrite it"
fi

# --- Build the packager argument list ---

set -- "$EXTENSION_DIR" \
  --project-location "$PROJECT_DIR" \
  --app-name "$APP_NAME" \
  --bundle-identifier "$BUNDLE_IDENTIFIER" \
  --swift \
  --no-open \
  --no-prompt
case "$PLATFORM" in
  macos) set -- "$@" --macos-only ;;
  ios) set -- "$@" --ios-only ;;
  all) ;;
esac
if [ "$COPY_RESOURCES" -eq 1 ]; then
  set -- "$@" --copy-resources
fi
if [ "$FORCE" -eq 1 ]; then
  set -- "$@" --force
fi

print_command() {
  tool="$1"
  shift
  printf 'xcrun %s' "$tool"
  for arg in "$@"; do
    printf ' %s' "$(quote "$arg")"
  done
  printf '\n'
}

if [ "$DRY_RUN" -eq 1 ]; then
  print_command safari-web-extension-packager "$@"
  exit 0
fi

# --- Run on macOS only ---

if [ "$(uname -s)" != "Darwin" ]; then
  die "the Safari web extension packager ships with Xcode and only runs on macOS; use --dry-run to check arguments elsewhere"
fi
command -v xcrun >/dev/null 2>&1 || die "xcrun not found; install Xcode (the Command Line Tools alone do not include the Safari packager)"

TOOL=""
for candidate in safari-web-extension-packager safari-web-extension-converter; do
  if xcrun --find "$candidate" >/dev/null 2>&1; then
    TOOL="$candidate"
    break
  fi
done
[ -n "$TOOL" ] || die "neither safari-web-extension-packager nor safari-web-extension-converter is available through xcrun; install full Xcode and select it with 'sudo xcode-select -s /Applications/Xcode.app'"

mkdir -p "$PROJECT_DIR" || die "cannot create project directory: $PROJECT_DIR"
print_command "$TOOL" "$@"
xcrun "$TOOL" "$@"

# Prefer DIR/<app name>/ so a stale project from another --app-name is not reported.
SEARCH_DIR="$PROJECT_DIR"
[ -d "$OUTPUT_DIR" ] && SEARCH_DIR="$OUTPUT_DIR"
PROJECT_FILE="$(find "$SEARCH_DIR" -maxdepth 3 -name '*.xcodeproj' -type d -print 2>/dev/null | head -n 1)"
[ -n "$PROJECT_FILE" ] || die "$TOOL exited 0 but no .xcodeproj was found under $SEARCH_DIR"

echo "Xcode project: $PROJECT_FILE"
if [ -f "$PROJECT_FILE/project.pbxproj" ]; then
  echo "Bundle identifiers in the project:"
  sed -n 's/.*PRODUCT_BUNDLE_IDENTIFIER = "\{0,1\}\([^";]*\)"\{0,1\};.*/  \1/p' "$PROJECT_FILE/project.pbxproj" | sort -u
fi
cat <<EOF
Next steps (see packaging/macos/safari/README.md):
  - Open the project in Xcode, set the signing team, and build the app scheme.
  - Run the manual checklist in packaging/macos/safari/smoke-checklist.md.
  - For MDM, the extension's composed identifier is "<extension bundle id> (<team id>)";
    read it from the built .appex with: codesign -dv "<App>.app/Contents/PlugIns/<Extension>.appex"
EOF
