#!/bin/sh
# Checks packaging/linux/install-endpoint.sh without installing anything: a stub stands in for the
# beacon binary and records the arguments the script would run it with.
#
#   sh packaging/linux/test-endpoint-scripts.sh
set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
INSTALL_SCRIPT="$ROOT_DIR/packaging/linux/install-endpoint.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

for script in "$ROOT_DIR"/packaging/linux/*.sh "$ROOT_DIR"/packaging/linux/nfpm/*.sh; do
  case "$(head -n 1 "$script")" in
    *bash*) bash -n "$script" ;;
    *) sh -n "$script" ;;
  esac
done

STUB="$TMP_DIR/beacon"
ARGS="$TMP_DIR/args"
cat >"$STUB" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" >"$BEACON_TEST_ARGS"
STUB
chmod 755 "$STUB"

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# run_install [VAR=value ...] runs the install script in a clean environment pointed at the stub,
# with no system config unless the caller makes one, and no defaults file unless it names one.
run_install() {
  rm -f "$ARGS"
  env -i PATH="$PATH" HOME="$TMP_DIR" BEACON_BIN="$STUB" BEACON_TEST_ARGS="$ARGS" \
    BEACON_COLLECTOR="$TMP_DIR/collector" \
    BEACON_SYSTEM_CONFIG="$TMP_DIR/etc/config.json" \
    BEACON_DEFAULTS_FILE="$TMP_DIR/none" \
    BEACON_DEFAULTS_OWNER="$(id -u)" \
    "$@" sh "$INSTALL_SCRIPT" >"$TMP_DIR/out" 2>&1 || { cat "$TMP_DIR/out" >&2; return 1; }
}

# flag_value FLAG prints the value passed after FLAG, or nothing when it was not passed.
flag_value() {
  awk -v flag="$1" 'found { print; exit } $0 == flag { found = 1 }' "$ARGS"
}

has_flag() {
  grep -qx -- "$1" "$ARGS"
}

expect_log_access() {
  got="$(flag_value --log-access)"
  [ "$got" = "$1" ] || fail "$2: --log-access is '$got', want '$1'"
}

# A first install, with no system config, defaults to restricted.
run_install
expect_log_access restricted "first install"
[ "$(sed -n 1,3p "$ARGS" | tr '\n' ' ')" = "endpoint install --system " ] || fail "first install: not an endpoint install --system: $(tr '\n' ' ' <"$ARGS")"

# An upgrade (a system config exists) passes no mode, so the endpoint keeps the one it has.
mkdir -p "$TMP_DIR/etc"
echo '{}' >"$TMP_DIR/etc/config.json"
run_install
has_flag --log-access && fail "upgrade: passed --log-access $(flag_value --log-access); the existing mode must be kept"
rm -f "$TMP_DIR/etc/config.json"

# The environment opts a first install out, and can ask for restricted on an upgrade.
run_install BEACON_LOG_ACCESS=shared
expect_log_access shared "first install with BEACON_LOG_ACCESS=shared"
echo '{}' >"$TMP_DIR/etc/config.json"
run_install BEACON_LOG_ACCESS=restricted
expect_log_access restricted "upgrade with BEACON_LOG_ACCESS=restricted"
rm -f "$TMP_DIR/etc/config.json"

# A defaults file supplies settings a configuration manager cannot pass through the environment.
DEFAULTS="$TMP_DIR/default-beacon"
cat >"$DEFAULTS" <<'CONF'
# Written by Salt before pkg.installed.
BEACON_LOG_ACCESS=shared
export BEACON_ENDPOINT_HARNESSES="claude,codex,cursor"
  BEACON_SESSION_SYNC='1'
BEACON_OTLP_HTTP_PORT=4319   
NOT_A_BEACON_SETTING=1
BEACON_BIN=/tmp/evil
PATH=/tmp/evil
BEACON_SERVICE=$(touch /tmp/beacon-defaults-ran)
not even an assignment
CONF
chmod 600 "$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
expect_log_access shared "defaults file"
[ "$(flag_value --harness)" = "claude,codex,cursor" ] || fail "defaults file: --harness is '$(flag_value --harness)'"
[ "$(flag_value --otlp-http-port)" = "4319" ] || fail "defaults file: --otlp-http-port is '$(flag_value --otlp-http-port)'"
has_flag --session-sync || fail "defaults file: BEACON_SESSION_SYNC='1' did not pass --session-sync"
# Only documented install settings are taken: the binary still came from the environment, and the
# command substitution was passed as an inert string rather than run.
[ -s "$ARGS" ] || fail "defaults file: BEACON_BIN in the file replaced the binary"
[ "$(flag_value --service)" = '$(touch /tmp/beacon-defaults-ran)' ] || fail "defaults file: --service is '$(flag_value --service)'"
[ -e /tmp/beacon-defaults-ran ] && fail "defaults file: a value was executed"

# The environment wins over the file.
run_install BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_LOG_ACCESS=restricted
expect_log_access restricted "environment over defaults file"

# Without a mode, a defaults file still gets the first-install default.
printf 'BEACON_SESSION_SYNC=0\n' >"$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
expect_log_access restricted "defaults file without a mode"
has_flag --no-session-sync || fail "defaults file: BEACON_SESSION_SYNC=0 did not pass --no-session-sync"

# A file anyone else can write is ignored, loudly: the script runs as root.
printf 'BEACON_LOG_ACCESS=shared\n' >"$DEFAULTS"
for mode in 620 602 660 666 622; do
  chmod "$mode" "$DEFAULTS"
  run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
  expect_log_access restricted "defaults file mode $mode"
  grep -q "ignoring $DEFAULTS" "$TMP_DIR/out" || fail "defaults file mode $mode: no warning"
done
chmod 644 "$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
expect_log_access shared "defaults file mode 644"
# ...and so is one owned by another account.
run_install BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_DEFAULTS_OWNER=12345
expect_log_access restricted "defaults file owned by someone else"

if [ "$failures" -ne 0 ]; then
  echo "$failures check(s) failed" >&2
  exit 1
fi
echo "linux endpoint script checks passed"
