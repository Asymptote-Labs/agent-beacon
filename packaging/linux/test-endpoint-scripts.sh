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
    BEACON_SYSTEM_LOG="$TMP_DIR/log/runtime.jsonl" \
    BEACON_INIT_COMM="$TMP_DIR/init-comm" \
    "$@" sh "$INSTALL_SCRIPT" $INSTALL_ARGS >"$TMP_DIR/out" 2>&1 || { cat "$TMP_DIR/out" >&2; return 1; }
}
# Positional arguments for run_install, split on spaces (none of the tests' values contain one).
INSTALL_ARGS=''

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

# systemd is PID 1 unless a test says otherwise.
printf 'systemd\n' >"$TMP_DIR/init-comm"

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

# Only where the collector will be a systemd service: a supervised collector nothing restarts would
# leave a restricted log spooling every hook event whenever it is down.
printf 'init\n' >"$TMP_DIR/init-comm"
run_install
has_flag --log-access && fail "no systemd: passed --log-access $(flag_value --log-access), want the shared default"
printf 'systemd\n' >"$TMP_DIR/init-comm"
run_install BEACON_SERVICE=none
has_flag --log-access && fail "BEACON_SERVICE=none: passed --log-access $(flag_value --log-access), want the shared default"
run_install BEACON_SERVICE=systemd
expect_log_access restricted "BEACON_SERVICE=systemd"
run_install BEACON_SERVICE=none BEACON_LOG_ACCESS=restricted
expect_log_access restricted "BEACON_SERVICE=none asking for restricted"

# A runtime log left by `uninstall --keep-logs` is a reinstall: the install keeps the mode recorded
# beside it, so no mode is passed.
mkdir -p "$TMP_DIR/log"
: >"$TMP_DIR/log/runtime.jsonl"
run_install
has_flag --log-access && fail "reinstall over a kept log: passed --log-access $(flag_value --log-access); the kept mode must be kept"
rm -rf "$TMP_DIR/log"

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
BEACON_SERVICE=$(touch${IFS}/tmp/beacon-defaults-ran)
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
[ "$(flag_value --service)" = '$(touch${IFS}/tmp/beacon-defaults-ran)' ] || fail "defaults file: --service is '$(flag_value --service)'"
[ -e /tmp/beacon-defaults-ran ] && fail "defaults file: a value was executed"

# Trailing comments, as a shell reads them, and values that are not one value are skipped with a
# warning instead of failing the package install.
cat >"$DEFAULTS" <<'CONF'
BEACON_LOG_ACCESS=shared # fleet default
BEACON_OTLP_HTTP_PORT="4320"   # alt port
BEACON_ENDPOINT_HARNESSES='claude,cursor'#no space so part of nothing: quoted then comment-less junk
BEACON_OTLP_GRPC_PORT=4317 4318
BEACON_SERVICE="systemd
BEACON_SPLUNK_INDEX=main#notacomment
CONF
chmod 600 "$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
expect_log_access shared "trailing comment"
[ "$(flag_value --otlp-http-port)" = "4320" ] || fail "quoted value with a comment: --otlp-http-port is '$(flag_value --otlp-http-port)'"
[ "$(flag_value --harness)" = "claude,codex" ] || fail "junk after a closing quote was taken: --harness is '$(flag_value --harness)'"
[ "$(flag_value --otlp-grpc-port)" = "4317" ] || fail "two words were taken as one value: --otlp-grpc-port is '$(flag_value --otlp-grpc-port)'"
has_flag --service && fail "an unterminated quote was taken: --service $(flag_value --service)"
[ "$(flag_value --splunk-index)" = "main#notacomment" ] || fail "a # inside a word is not a comment: --splunk-index is '$(flag_value --splunk-index)'"
for key in BEACON_ENDPOINT_HARNESSES BEACON_OTLP_GRPC_PORT BEACON_SERVICE; do
  grep -q "ignoring $key in $DEFAULTS" "$TMP_DIR/out" || fail "no warning for the malformed $key"
done
grep -q "4318" "$TMP_DIR/out" && fail "a warning printed a value; it must name the key only"

# The package postinstall loads the same settings itself, for the step that configures each
# logged-in user, which runs as a separate command.
printf 'BEACON_ENDPOINT_HARNESSES=claude,codex,cursor # all three\n' >"$DEFAULTS"
got="$(env -i PATH="$PATH" BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_DEFAULTS_OWNER="$(id -u)" \
  sh -c '. "$1"; beacon_load_settings; printf %s "${BEACON_ENDPOINT_HARNESSES:-}"' sh "$ROOT_DIR/packaging/linux/beacon-settings.sh")"
[ "$got" = "claude,codex,cursor" ] || fail "beacon_load_settings exported BEACON_ENDPOINT_HARNESSES='$got'"

# Run the real postinstall, with its /opt/beacon paths pointed at stubs, as a configuration manager
# would: root, no environment of its own, a settings file holding a harness list and a Splunk HEC
# token. The per-user step re-runs the CLI as each logged-in user with the postinstall's
# environment, so the harness list must reach it and the token must not.
PI_ROOT="$TMP_DIR/pi"
mkdir -p "$PI_ROOT/opt/scripts" "$PI_ROOT/opt/bin" "$PI_ROOT/env"
cp "$ROOT_DIR/packaging/linux/beacon-settings.sh" "$PI_ROOT/opt/scripts/beacon-settings.sh"
printf '#!/bin/sh\nexit 0\n' >"$PI_ROOT/opt/scripts/install-endpoint.sh"
cat >"$PI_ROOT/opt/bin/beacon" <<'STUB'
#!/bin/sh
# One file per subcommand: its arguments, then its environment.
out="$BEACON_TEST_PI_ENV/$2"
printf '%s\n' "$@" >"$out.args"
env >"$out.env"
STUB
chmod 755 "$PI_ROOT/opt/scripts/install-endpoint.sh" "$PI_ROOT/opt/bin/beacon"
sed "s#/opt/beacon#$PI_ROOT/opt#g; s#/etc/beacon/endpoint/config.json#$PI_ROOT/none.json#g" \
  "$ROOT_DIR/packaging/linux/nfpm/postinstall.sh" >"$PI_ROOT/postinstall.sh"
printf '%s\n' 'BEACON_ENDPOINT_HARNESSES=claude,codex,cursor' 'BEACON_SPLUNK_HEC_TOKEN=pi-secret-token' >"$DEFAULTS"
chmod 600 "$DEFAULTS"
env -i PATH="$PATH" BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_DEFAULTS_OWNER="$(id -u)" \
  BEACON_TEST_PI_ENV="$PI_ROOT/env" sh "$PI_ROOT/postinstall.sh" >"$TMP_DIR/out" 2>&1 || {
  cat "$TMP_DIR/out" >&2; fail "the postinstall failed"; }
if [ -f "$PI_ROOT/env/user-config.args" ]; then
  grep -qx 'claude,codex,cursor' "$PI_ROOT/env/user-config.args" || \
    fail "postinstall: the user-config step did not get the file's harness list: $(tr '\n' ' ' <"$PI_ROOT/env/user-config.args")"
  grep -rq 'pi-secret-token' "$PI_ROOT/env" && fail "postinstall: the Splunk HEC token reached a command's environment or arguments"
else
  fail "postinstall: the user-config step did not run"
fi
grep -q 'beacon-settings.sh' "$ROOT_DIR/cli/beacon/.goreleaser.yaml" || fail "the packages do not ship beacon-settings.sh"

# The environment wins over the file.
run_install BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_LOG_ACCESS=restricted
expect_log_access restricted "environment over defaults file"

# Positional arguments are explicit too, so they win over the file; the file fills what they leave.
printf 'BEACON_ENDPOINT_HARNESSES=claude\nBEACON_OTLP_GRPC_PORT=5317\nBEACON_OTLP_HTTP_PORT=5318\n' >"$DEFAULTS"
INSTALL_ARGS='claude,codex,cursor 4417'
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
INSTALL_ARGS=''
[ "$(flag_value --harness)" = "claude,codex,cursor" ] || fail "positional over defaults file: --harness is '$(flag_value --harness)'"
[ "$(flag_value --otlp-grpc-port)" = "4417" ] || fail "positional over defaults file: --otlp-grpc-port is '$(flag_value --otlp-grpc-port)'"
[ "$(flag_value --otlp-http-port)" = "5318" ] || fail "defaults file under positional: --otlp-http-port is '$(flag_value --otlp-http-port)'"
# ...and the environment still wins over a positional argument, as before.
INSTALL_ARGS='claude,codex,cursor'
run_install BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_ENDPOINT_HARNESSES=codex
INSTALL_ARGS=''
[ "$(flag_value --harness)" = "codex" ] || fail "environment over positional: --harness is '$(flag_value --harness)'"

# A key assigned twice takes its last value, as a shell would: configuration managers override a
# setting by appending a line. The environment still wins over every one of them.
printf 'BEACON_LOG_ACCESS=restricted\nBEACON_LOG_ACCESS=shared # appended\n' >"$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
expect_log_access shared "defaults file key assigned twice"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS" BEACON_LOG_ACCESS=restricted
expect_log_access restricted "environment over a key assigned twice"
# A malformed later line is skipped, so the earlier value stands.
printf 'BEACON_LOG_ACCESS=shared\nBEACON_LOG_ACCESS=restricted junk\n' >"$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS"
expect_log_access shared "malformed later assignment"
# A name that is not a plain identifier is ignored, and does not fail the install.
printf 'BEACON_LOG_ACCESS BEACON_SESSION_SYNC=1\n' >"$DEFAULTS"
run_install BEACON_DEFAULTS_FILE="$DEFAULTS" || fail "a key with a space failed the install"
has_flag --session-sync && fail "a key with a space set BEACON_SESSION_SYNC"
expect_log_access restricted "key with a space"

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
