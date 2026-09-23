#!/bin/sh
# Checks for the Safari packaging helpers. Runs on Linux and macOS: the packager
# itself is replaced by a fake xcrun and uname is faked both ways, so what is
# tested here is the wrapper's argument handling and guards, not Apple's tool.
# The real conversion is a manual step on a Mac (see smoke-checklist.md).
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)"
SAFARI_DIR="$ROOT_DIR/packaging/macos/safari"
SCRIPT="$SAFARI_DIR/create-safari-project.sh"
CHECKER="$SAFARI_DIR/check-mdm-declaration.py"
DECLARATION="$SAFARI_DIR/mdm/safari-extension-settings.declaration.json"
MANIFEST="$ROOT_DIR/browser-extension/src/manifest.json"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

unset SAFARI_EXTENSION_DIR SAFARI_PROJECT_DIR SAFARI_APP_NAME SAFARI_BUNDLE_IDENTIFIER

command -v python3 >/dev/null 2>&1 || { echo "python3 is required to validate the MDM declaration" >&2; exit 1; }

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# expect_fail NAME PATTERN -- CMD...: CMD must exit non-zero and print PATTERN on stderr.
expect_fail() {
  name="$1"
  pattern="$2"
  shift 3
  if "$@" >"$TMP_DIR/out" 2>"$TMP_DIR/err"; then
    fail "$name: expected a non-zero exit"
  fi
  grep -q -- "$pattern" "$TMP_DIR/err" || fail "$name: stderr did not contain '$pattern': $(cat "$TMP_DIR/err")"
}

# A complete fake build: every file the wrapper requires, and the real manifest.
make_extension() {
  dir="$1"
  mkdir -p "$dir"
  cp "$MANIFEST" "$dir/manifest.json"
  for f in sw.js content.js interceptor.js popup.html popup.js options.html options.js; do
    printf '// %s\n' "$f" >"$dir/$f"
  done
}

EXT="$TMP_DIR/ext"
make_extension "$EXT"
PROJECTS="$TMP_DIR/projects"

# Fake bin directories. The Linux one has an xcrun that must never run.
LINUX_BIN="$TMP_DIR/linux-bin"
DARWIN_BIN="$TMP_DIR/darwin-bin"
XCRUN_LOG="$TMP_DIR/xcrun.log"
mkdir -p "$LINUX_BIN" "$DARWIN_BIN"
printf '#!/bin/sh\necho Linux\n' >"$LINUX_BIN/uname"
printf '#!/bin/sh\necho Darwin\n' >"$DARWIN_BIN/uname"
cat >"$LINUX_BIN/xcrun" <<'STUB'
#!/bin/sh
echo "xcrun must not run off macOS" >>"$XCRUN_LOG"
exit 99
STUB
# FAKE_XCRUN_TOOLS: space-separated tools that `xcrun --find` reports.
# FAKE_XCRUN_CREATE=1: create DIR/<app>/<app>.xcodeproj like the real packager.
# FAKE_XCRUN_STATUS: exit status of the tool run.
cat >"$DARWIN_BIN/xcrun" <<'STUB'
#!/bin/sh
if [ "$1" = "--find" ]; then
  for t in ${FAKE_XCRUN_TOOLS:-}; do
    [ "$t" = "$2" ] && { echo "/fake/$t"; exit 0; }
  done
  exit 1
fi
: >"$XCRUN_LOG"
for a in "$@"; do printf '%s\n' "$a" >>"$XCRUN_LOG"; done
location=""
app=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --project-location) location="$2"; shift 2 ;;
    --app-name) app="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ "${FAKE_XCRUN_CREATE:-0}" = "1" ]; then
  mkdir -p "$location/$app/$app.xcodeproj"
  cat >"$location/$app/$app.xcodeproj/project.pbxproj" <<EOF
				PRODUCT_BUNDLE_IDENTIFIER = ai.asymptote.beacon.browser-collector;
				PRODUCT_BUNDLE_IDENTIFIER = ai.asymptote.beacon.browser-collector.Extension;
EOF
fi
exit "${FAKE_XCRUN_STATUS:-0}"
STUB
chmod +x "$LINUX_BIN/uname" "$LINUX_BIN/xcrun" "$DARWIN_BIN/uname" "$DARWIN_BIN/xcrun"
export XCRUN_LOG

run_linux() {
  PATH="$LINUX_BIN:$PATH" sh "$SCRIPT" "$@"
}
run_darwin() {
  PATH="$DARWIN_BIN:$PATH" sh "$SCRIPT" "$@"
}

# --- syntax and help ---

sh -n "$SCRIPT"
sh -n "$0"
run_linux --help >"$TMP_DIR/out" || fail "--help should exit 0"
grep -q '^Usage:' "$TMP_DIR/out" || fail "--help should print usage"

# The default input is the `safari` build target's output directory.
grep -q 'default: browser-extension/dist-safari' "$TMP_DIR/out" || fail "--help should name dist-safari as the default"
grep -q 'EXTENSION_DIR="${SAFARI_EXTENSION_DIR:-$ROOT_DIR/browser-extension/dist-safari}"' "$SCRIPT" ||
  fail "the script should default to browser-extension/dist-safari"
grep -q "outdir: 'dist-safari'" "$ROOT_DIR/browser-extension/tools/targets.mjs" ||
  fail "browser-extension/tools/targets.mjs should build the safari target to dist-safari"

# --- argument handling ---

expect_fail "unknown argument" "unknown argument: --bogus" -- run_linux --bogus
expect_fail "flag without value" "--app-name requires a value" -- run_linux --app-name
expect_fail "empty app name" "must not be empty" -- run_linux --dry-run --extension-dir "$EXT" --app-name ""
expect_fail "app name with slash" "must not contain '/'" -- run_linux --dry-run --extension-dir "$EXT" --app-name "a/b"
expect_fail "bundle id with space" "reverse-DNS identifier" -- run_linux --dry-run --extension-dir "$EXT" --bundle-id "ai.asymptote bad"
expect_fail "bundle id without dot" "reverse-DNS identifier" -- run_linux --dry-run --extension-dir "$EXT" --bundle-id "beacon"
expect_fail "bundle id with underscore" "reverse-DNS identifier" -- run_linux --dry-run --extension-dir "$EXT" --bundle-id "ai.asymptote.bad_id"
expect_fail "unknown platform" "--platform must be macos, ios, or all" -- run_linux --dry-run --extension-dir "$EXT" --platform windows

# --- extension build validation ---

expect_fail "missing extension dir" "extension directory not found" -- run_linux --dry-run --extension-dir "$TMP_DIR/nope"

INCOMPLETE="$TMP_DIR/incomplete"
make_extension "$INCOMPLETE"
rm "$INCOMPLETE/interceptor.js"
expect_fail "incomplete build" "missing or empty interceptor.js" -- run_linux --dry-run --extension-dir "$INCOMPLETE"

EMPTY_FILE="$TMP_DIR/empty-file"
make_extension "$EMPTY_FILE"
: >"$EMPTY_FILE/sw.js"
expect_fail "empty bundle" "missing or empty sw.js" -- run_linux --dry-run --extension-dir "$EMPTY_FILE"

MV2="$TMP_DIR/mv2"
make_extension "$MV2"
printf '{ "manifest_version": 2, "name": "x", "version": "1" }\n' >"$MV2/manifest.json"
expect_fail "MV2 manifest" "is not a Manifest V3 manifest" -- run_linux --dry-run --extension-dir "$MV2"

MV30="$TMP_DIR/mv30"
make_extension "$MV30"
printf '{ "manifest_version": 30 }\n' >"$MV30/manifest.json"
expect_fail "manifest_version 30 is not 3" "is not a Manifest V3 manifest" -- run_linux --dry-run --extension-dir "$MV30"

WITH_MAPS="$TMP_DIR/with-maps"
make_extension "$WITH_MAPS"
printf '{}\n' >"$WITH_MAPS/sw.js.map"
run_linux --dry-run --extension-dir "$WITH_MAPS" --project-dir "$PROJECTS" >/dev/null 2>"$TMP_DIR/err" ||
  fail "a build with sourcemaps should still convert"
grep -q 'contains sourcemaps' "$TMP_DIR/err" || fail "sourcemaps should produce a warning"

# --- dry run: the packager command, on any OS, with no side effects ---

run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS" >"$TMP_DIR/out" 2>"$TMP_DIR/err" ||
  fail "default dry run should succeed off macOS: $(cat "$TMP_DIR/err")"
[ ! -e "$PROJECTS" ] || fail "--dry-run must not create the project directory"
[ ! -s "$XCRUN_LOG" ] || fail "--dry-run must not run xcrun"
[ ! -s "$TMP_DIR/err" ] || fail "default dry run should print no warnings: $(cat "$TMP_DIR/err")"

# Evaluate the printed command with xcrun replaced by an argument dumper, so
# the check covers the quoting as well as the flags.
dump_args() {
  (
    xcrun() {
      for a in "$@"; do printf '%s\n' "$a"; done
    }
    eval "$(cat "$1")"
  )
}
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
cat >"$TMP_DIR/want" <<EOF
safari-web-extension-packager
$EXT
--project-location
$PROJECTS
--app-name
Beacon Browser Collector
--bundle-identifier
ai.asymptote.beacon.browser-collector
--swift
--no-open
--no-prompt
--macos-only
--copy-resources
EOF
diff -u "$TMP_DIR/want" "$TMP_DIR/args" >&2 || fail "default packager arguments changed"

has_arg() { grep -qx -- "$1" "$TMP_DIR/args"; }

run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS" --platform all >"$TMP_DIR/out"
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
if has_arg --macos-only || has_arg --ios-only; then fail "--platform all should pass neither platform flag"; fi

run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS" --platform ios >"$TMP_DIR/out"
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
has_arg --ios-only || fail "--platform ios should pass --ios-only"
if has_arg --macos-only; then fail "--platform ios should not pass --macos-only"; fi

run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS" --reference-resources --force >"$TMP_DIR/out"
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
if has_arg --copy-resources; then fail "--reference-resources should drop --copy-resources"; fi
has_arg --force || fail "--force should be passed to the packager"

# Names with spaces and quotes survive the round trip; env defaults apply.
(
  export SAFARI_APP_NAME="Beacon's Collector" SAFARI_BUNDLE_IDENTIFIER="com.example.beacon-test"
  run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS"
) >"$TMP_DIR/out"
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
has_arg "Beacon's Collector" || fail "SAFARI_APP_NAME with a quote should round-trip"
has_arg "com.example.beacon-test" || fail "SAFARI_BUNDLE_IDENTIFIER should set the bundle identifier"

# Relative paths are made absolute.
(cd "$TMP_DIR" && PATH="$LINUX_BIN:$PATH" sh "$SCRIPT" --dry-run --extension-dir ext --project-dir rel-projects) >"$TMP_DIR/out"
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
has_arg "$TMP_DIR/rel-projects" || fail "a relative --project-dir should be made absolute"
has_arg "$EXT" || fail "a relative --extension-dir should be made absolute"

# --- output directory guards ---

mkdir -p "$PROJECTS/Beacon Browser Collector"
expect_fail "existing output dir" "already exists; pass --force" -- run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS"
run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS" --force >/dev/null ||
  fail "--force should allow an existing output directory"
rm -rf "$PROJECTS"

expect_fail "project inside extension" "must not be inside the extension directory" -- run_linux --dry-run --extension-dir "$EXT" --project-dir "$EXT/safari"
expect_fail "project is extension dir" "must not be inside the extension directory" -- run_linux --dry-run --extension-dir "$EXT" --project-dir "$EXT"

# --- off-macOS guard ---

expect_fail "Linux run" "only runs on macOS" -- run_linux --extension-dir "$EXT" --project-dir "$PROJECTS"
[ ! -s "$XCRUN_LOG" ] || fail "xcrun must not run off macOS"
[ ! -e "$PROJECTS" ] || fail "a refused run must not create the project directory"

# --- macOS path with a fake xcrun ---

# Assignments before a shell function are not reliably exported in POSIX sh,
# so the fake's knobs are exported explicitly.
export FAKE_XCRUN_TOOLS="safari-web-extension-packager safari-web-extension-converter" FAKE_XCRUN_CREATE=1 FAKE_XCRUN_STATUS=0
run_darwin --extension-dir "$EXT" --project-dir "$PROJECTS" >"$TMP_DIR/out" 2>"$TMP_DIR/err" ||
  fail "macOS run should succeed: $(cat "$TMP_DIR/err")"
[ "$(head -n 1 "$XCRUN_LOG")" = "safari-web-extension-packager" ] || fail "should prefer safari-web-extension-packager"
tail -n +2 "$XCRUN_LOG" >"$TMP_DIR/ran"
tail -n +2 "$TMP_DIR/want" >"$TMP_DIR/want-args"
diff -u "$TMP_DIR/want-args" "$TMP_DIR/ran" >&2 || fail "macOS run should pass the same arguments as --dry-run prints"
grep -q "Xcode project: $PROJECTS/Beacon Browser Collector/Beacon Browser Collector.xcodeproj" "$TMP_DIR/out" ||
  fail "should report the generated project: $(cat "$TMP_DIR/out")"
grep -q '^  ai.asymptote.beacon.browser-collector.Extension$' "$TMP_DIR/out" ||
  fail "should list the extension bundle identifier"
rm -rf "$PROJECTS"

export FAKE_XCRUN_TOOLS="safari-web-extension-converter"
run_darwin --extension-dir "$EXT" --project-dir "$PROJECTS" >/dev/null 2>&1 ||
  fail "should fall back to safari-web-extension-converter"
[ "$(head -n 1 "$XCRUN_LOG")" = "safari-web-extension-converter" ] || fail "fallback should run safari-web-extension-converter"
rm -rf "$PROJECTS"
: >"$XCRUN_LOG"

export FAKE_XCRUN_TOOLS=""
expect_fail "no packager" "neither safari-web-extension-packager nor safari-web-extension-converter" -- \
  run_darwin --extension-dir "$EXT" --project-dir "$PROJECTS"
[ ! -s "$XCRUN_LOG" ] || fail "no conversion should run without a packager"

export FAKE_XCRUN_TOOLS="safari-web-extension-packager" FAKE_XCRUN_CREATE=0
expect_fail "no project produced" "no .xcodeproj was found" -- \
  run_darwin --extension-dir "$EXT" --project-dir "$PROJECTS"
rm -rf "$PROJECTS"

export FAKE_XCRUN_CREATE=1 FAKE_XCRUN_STATUS=3
if run_darwin --extension-dir "$EXT" --project-dir "$PROJECTS" >/dev/null 2>&1; then
  fail "a failing packager should fail the wrapper"
fi
rm -rf "$PROJECTS"
unset FAKE_XCRUN_TOOLS FAKE_XCRUN_CREATE FAKE_XCRUN_STATUS

# --- MDM declaration ---

# The declaration must name the extension the wrapper produces: the packager
# derives the extension identifier as <app bundle id>.Extension.
run_linux --dry-run --extension-dir "$EXT" --project-dir "$PROJECTS" >"$TMP_DIR/out"
dump_args "$TMP_DIR/out" >"$TMP_DIR/args"
APP_BUNDLE_ID="$(grep -A1 -x -- '--bundle-identifier' "$TMP_DIR/args" | tail -n 1)"
EXTENSION_BUNDLE_ID="$APP_BUNDLE_ID.Extension"

python3 "$CHECKER" "$DECLARATION" --manifest "$MANIFEST" --extension-bundle-id "$EXTENSION_BUNDLE_ID" >/dev/null ||
  fail "the shipped MDM declaration should validate"

# Prove the checker rejects the mistakes it exists to catch.
mutate() {
  python3 - "$DECLARATION" "$1" "$2" <<'PY'
import json, sys
src, out, change = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(src))
managed = d["Payload"]["ManagedExtensions"]
key = next(iter(managed))
entry = managed[key]
if change == "state-with-space":
    entry["State"] = "Always On"
elif change == "wrong-type":
    d["Type"] = "com.apple.Safari.Extensions"
elif change == "no-team-id":
    managed[key.split(" ")[0]] = managed.pop(key)
elif change == "missing-host":
    entry["AllowedDomains"].remove("chatgpt.com")
elif change == "url-not-domain":
    entry["AllowedDomains"].append("https://claude.ai/")
elif change == "denied-host":
    entry["DeniedDomains"] = ["127.0.0.1"]
elif change == "always-off":
    entry["State"] = "AlwaysOff"
elif change == "no-server-token":
    del d["ServerToken"]
elif change == "unknown-key":
    entry["Enabled"] = True
json.dump(d, open(out, "w"))
PY
}
for change in state-with-space wrong-type no-team-id missing-host url-not-domain denied-host always-off no-server-token unknown-key; do
  mutate "$TMP_DIR/bad-$change.json" "$change"
  if python3 "$CHECKER" "$TMP_DIR/bad-$change.json" --manifest "$MANIFEST" --extension-bundle-id "$EXTENSION_BUNDLE_ID" >/dev/null 2>&1; then
    fail "MDM checker should reject a declaration with change '$change'"
  fi
done
printf '{ not json' >"$TMP_DIR/bad-json.json"
if python3 "$CHECKER" "$TMP_DIR/bad-json.json" --manifest "$MANIFEST" --extension-bundle-id "$EXTENSION_BUNDLE_ID" >/dev/null 2>&1; then
  fail "MDM checker should reject invalid JSON"
fi
if python3 "$CHECKER" "$DECLARATION" --manifest "$MANIFEST" --extension-bundle-id "com.example.other.Extension" >/dev/null 2>&1; then
  fail "MDM checker should reject a declaration that does not name the collector extension"
fi

echo "Safari packaging checks passed"
