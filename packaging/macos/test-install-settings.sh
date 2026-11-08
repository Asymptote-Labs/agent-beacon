#!/bin/sh
# Checks the macOS install script's settings and first-install rules without installing anything: a
# stub stands in for the beacon binary, and another for `defaults`, reading KEY=VALUE text files in
# place of the preferences plists. Runs on Linux too.
#
#   sh packaging/macos/test-install-settings.sh
set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
INSTALL_SCRIPT="$ROOT_DIR/packaging/macos/install-endpoint.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

sh -n "$ROOT_DIR/packaging/macos/beacon-settings.sh"

STUB="$TMP_DIR/beacon"
ARGS="$TMP_DIR/args"
cat >"$STUB" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" >"$BEACON_TEST_ARGS"
STUB
DEFAULTS_STUB="$TMP_DIR/defaults"
cat >"$DEFAULTS_STUB" <<'STUB'
#!/bin/sh
# defaults read PATH KEY
[ "$1" = read ] || exit 1
line="$(grep "^$3=" "$2.plist" 2>/dev/null | tail -n 1)" || exit 1
[ -n "$line" ] || exit 1
printf '%s\n' "${line#*=}"
STUB
chmod 755 "$STUB" "$DEFAULTS_STUB"
MANAGED="$TMP_DIR/managed/com.beacon.endpoint"
LOCAL="$TMP_DIR/local/com.beacon.endpoint"
mkdir -p "$TMP_DIR/managed" "$TMP_DIR/local" "$TMP_DIR/system"

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# run_install [VAR=value ...] runs the install script in a clean environment pointed at the stubs:
# a first install (no system config or log) unless the caller makes one, no preferences unless the
# caller writes them, and no Installer.app session unless the caller says so.
run_install() {
  rm -f "$ARGS"
  env -i PATH="$PATH" HOME="$TMP_DIR" BEACON_BIN="$STUB" BEACON_TEST_ARGS="$ARGS" \
    BEACON_COLLECTOR="$TMP_DIR/collector" \
    BEACON_SYSTEM_CONFIG="$TMP_DIR/system/config.json" \
    BEACON_SYSTEM_LOG="$TMP_DIR/system/runtime.jsonl" \
    BEACON_DEFAULTS_BIN="$DEFAULTS_STUB" \
    BEACON_MANAGED_PREFS="$MANAGED" \
    BEACON_LOCAL_PREFS="$LOCAL" \
    BEACON_SETTINGS_OWNER="$(id -u)" \
    BEACON_INSTALLER_GUI=0 \
    "$@" sh "$INSTALL_SCRIPT" >"$TMP_DIR/out" 2>&1 || { cat "$TMP_DIR/out" >&2; return 1; }
}

flag_value() {
  awk -v flag="$1" 'found { print; exit } $0 == flag { found = 1 }' "$ARGS"
}

has_flag() {
  grep -qx -- "$1" "$ARGS"
}

expect() {
  got="$(flag_value "$1")"
  [ "$got" = "$2" ] || fail "$3: $1 is '$got', want '$2'"
}

# First install: restricted. Upgrade (a system config) or a kept log: no mode, the endpoint keeps its
# own. BEACON_LOG_ACCESS=shared opts a first install out.
run_install
expect --log-access restricted "first install"
echo '{}' >"$TMP_DIR/system/config.json"
run_install
has_flag --log-access && fail "upgrade passed --log-access $(flag_value --log-access)"
rm -f "$TMP_DIR/system/config.json"
: >"$TMP_DIR/system/runtime.jsonl"
run_install
has_flag --log-access && fail "reinstall over a kept log passed --log-access $(flag_value --log-access)"
rm -f "$TMP_DIR/system/runtime.jsonl"
run_install BEACON_LOG_ACCESS=shared
expect --log-access shared "first install with BEACON_LOG_ACCESS=shared"

# The dashboard: disabled on an unattended first install, kept for a person in Installer.app, nothing
# passed on an upgrade or a reinstall over a kept log, and BEACON_DASHBOARD (environment or
# preferences) decides either way.
run_install
expect --dashboard disabled "first install dashboard"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice
has_flag --dashboard && fail "Installer.app first install passed --dashboard $(flag_value --dashboard), want the dashboard kept"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice BEACON_DASHBOARD=disabled
expect --dashboard disabled "Installer.app first install with BEACON_DASHBOARD=disabled"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice COMMAND_LINE_INSTALL=1
expect --dashboard disabled "installer(8) first install"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=loginwindow
expect --dashboard disabled "first install at the login window"
run_install BEACON_DASHBOARD=enabled
expect --dashboard enabled "first install with BEACON_DASHBOARD=enabled"
echo '{}' >"$TMP_DIR/system/config.json"
run_install
has_flag --dashboard && fail "upgrade passed --dashboard $(flag_value --dashboard)"
run_install BEACON_DASHBOARD=disabled
expect --dashboard disabled "upgrade with BEACON_DASHBOARD=disabled"
rm -f "$TMP_DIR/system/config.json"
: >"$TMP_DIR/system/runtime.jsonl"
run_install
has_flag --dashboard && fail "reinstall over a kept log passed --dashboard $(flag_value --dashboard)"
rm -f "$TMP_DIR/system/runtime.jsonl"
printf 'BEACON_DASHBOARD=enabled\n' >"$MANAGED.plist"
chmod 644 "$MANAGED.plist"
run_install
expect --dashboard enabled "managed BEACON_DASHBOARD=enabled on a first install"
rm -f "$MANAGED.plist"

# Managed preferences are read, win over local ones, and lose to the environment and to arguments.
printf 'BEACON_LOG_ACCESS=shared\nBEACON_ENDPOINT_HARNESSES=claude,cursor\nBEACON_SESSION_SYNC=1\n' >"$MANAGED.plist"
printf 'BEACON_LOG_ACCESS=restricted\nBEACON_OTLP_GRPC_PORT=6317\n' >"$LOCAL.plist"
chmod 644 "$MANAGED.plist" "$LOCAL.plist"
run_install
expect --log-access shared "managed preferences"
expect --harness claude,cursor "managed preferences harnesses"
expect --otlp-grpc-port 6317 "local preferences fill what managed ones lack"
has_flag --session-sync || fail "managed BEACON_SESSION_SYNC=1 did not pass --session-sync"
run_install BEACON_LOG_ACCESS=restricted
expect --log-access restricted "environment over managed preferences"
rm -f "$ARGS"
env -i PATH="$PATH" HOME="$TMP_DIR" BEACON_BIN="$STUB" BEACON_TEST_ARGS="$ARGS" BEACON_COLLECTOR="$TMP_DIR/collector" \
  BEACON_SYSTEM_CONFIG="$TMP_DIR/system/config.json" BEACON_SYSTEM_LOG="$TMP_DIR/system/runtime.jsonl" \
  BEACON_DEFAULTS_BIN="$DEFAULTS_STUB" BEACON_MANAGED_PREFS="$MANAGED" BEACON_LOCAL_PREFS="$LOCAL" \
  BEACON_SETTINGS_OWNER="$(id -u)" BEACON_INSTALLER_GUI=0 \
  sh "$INSTALL_SCRIPT" _ _ _ codex >"$TMP_DIR/out" 2>&1 || fail "install with a positional harness failed"
expect --harness codex "Jamf positional argument over managed preferences"

# A preferences file another account can write, or owned by one, is ignored with a warning.
chmod 664 "$MANAGED.plist"
run_install
expect --log-access restricted "group-writable managed preferences ignored (local ones apply)"
grep -q "ignoring $MANAGED.plist" "$TMP_DIR/out" || fail "group-writable managed preferences: no warning"
chmod 644 "$MANAGED.plist"
run_install BEACON_SETTINGS_OWNER=12345
expect --log-access restricted "preferences owned by someone else ignored"
has_flag --session-sync && fail "preferences owned by someone else still set BEACON_SESSION_SYNC"
# A value that is not one line is skipped, naming only the key.
printf 'BEACON_SPLUNK_HEC_TOKEN=(\n' >"$MANAGED.plist"
run_install
has_flag --splunk-hec-token && fail "an array value passed --splunk-hec-token"
grep -q "ignoring BEACON_SPLUNK_HEC_TOKEN" "$TMP_DIR/out" || fail "array value: no warning naming the key"
rm -f "$MANAGED.plist" "$LOCAL.plist"

# Log readers: a first install a person ran in Installer.app adds them; `installer` (MDM, scripts)
# adds nobody, even with someone logged in; BEACON_LOG_READERS, set even to nothing, is the list.
run_install BEACON_CONSOLE_USER=alice
has_flag --log-readers && fail "unattended install added $(flag_value --log-readers)"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice
expect --log-readers alice "Installer.app first install"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice COMMAND_LINE_INSTALL=1
has_flag --log-readers && fail "COMMAND_LINE_INSTALL install added $(flag_value --log-readers)"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=loginwindow
has_flag --log-readers && fail "loginwindow added as a reader"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice BEACON_LOG_ACCESS=shared
has_flag --log-readers && fail "shared install added a reader"
# The CLI takes the mode in any case, so the script must too.
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice BEACON_LOG_ACCESS=' Restricted '
expect --log-readers alice "Installer.app first install with BEACON_LOG_ACCESS=Restricted"
expect --log-access restricted "BEACON_LOG_ACCESS=Restricted"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice BEACON_LOG_ACCESS=SHARED
has_flag --log-readers && fail "BEACON_LOG_ACCESS=SHARED install added a reader"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice BEACON_LOG_READERS=
has_flag --log-readers && fail "BEACON_LOG_READERS= still added the installing person"
printf 'BEACON_LOG_READERS=\n' >"$MANAGED.plist"
chmod 644 "$MANAGED.plist"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice
has_flag --log-readers && fail "managed BEACON_LOG_READERS (empty) still added the installing person"
printf 'BEACON_LOG_READERS=svc_splunk,bob\n' >"$MANAGED.plist"
run_install
expect --log-readers svc_splunk,bob "managed BEACON_LOG_READERS"
rm -f "$MANAGED.plist"
echo '{}' >"$TMP_DIR/system/config.json"
run_install BEACON_INSTALLER_GUI=1 BEACON_CONSOLE_USER=alice
has_flag --log-readers && fail "upgrade in Installer.app added the person implicitly"
rm -f "$TMP_DIR/system/config.json"

# Without the test override, Installer.app is recognized by the executable the kernel reports for an
# Installer process the console user owns, never by the process name alone, which any account can use.
PGREP_STUB="$TMP_DIR/pgrep"
LSOF_STUB="$TMP_DIR/lsof"
cat >"$PGREP_STUB" <<'STUB'
#!/bin/sh
# pgrep -x -u USER Installer: one pid when the test says an Installer process runs as USER.
[ "$3" = "$BEACON_TEST_INSTALLER_USER" ] && echo 4242
exit 0
STUB
cat >"$LSOF_STUB" <<'STUB'
#!/bin/sh
printf 'p4242\nftxt\nn%s\nftxt\nn/usr/lib/dyld\n' "$BEACON_TEST_INSTALLER_EXE"
STUB
chmod 755 "$PGREP_STUB" "$LSOF_STUB"
APPLE_INSTALLER=/System/Library/CoreServices/Installer.app/Contents/MacOS/Installer
run_install BEACON_INSTALLER_GUI= BEACON_PGREP_BIN="$PGREP_STUB" BEACON_LSOF_BIN="$LSOF_STUB" \
  BEACON_CONSOLE_USER=alice BEACON_TEST_INSTALLER_USER=alice BEACON_TEST_INSTALLER_EXE="$APPLE_INSTALLER"
expect --log-readers alice "Apple's Installer.app running as the console user"
run_install BEACON_INSTALLER_GUI= BEACON_PGREP_BIN="$PGREP_STUB" BEACON_LSOF_BIN="$LSOF_STUB" \
  BEACON_CONSOLE_USER=alice BEACON_TEST_INSTALLER_USER=alice BEACON_TEST_INSTALLER_EXE=/Users/alice/bin/Installer
has_flag --log-readers && fail "a program named Installer that is not Apple's added $(flag_value --log-readers)"
run_install BEACON_INSTALLER_GUI= BEACON_PGREP_BIN="$PGREP_STUB" BEACON_LSOF_BIN="$LSOF_STUB" \
  BEACON_CONSOLE_USER=alice BEACON_TEST_INSTALLER_USER=bob BEACON_TEST_INSTALLER_EXE="$APPLE_INSTALLER"
has_flag --log-readers && fail "Installer.app running as another account added $(flag_value --log-readers)"
run_install BEACON_INSTALLER_GUI= BEACON_PGREP_BIN="$TMP_DIR/missing" BEACON_LSOF_BIN="$LSOF_STUB" \
  BEACON_CONSOLE_USER=alice BEACON_TEST_INSTALLER_USER=alice BEACON_TEST_INSTALLER_EXE="$APPLE_INSTALLER"
has_flag --log-readers && fail "no pgrep: treated as Installer.app and added $(flag_value --log-readers)"

# The Fleet wrapper passes only the arguments it was given, so the managed preferences fill the rest
# instead of being shadowed by its defaults.
printf 'BEACON_ENDPOINT_HARNESSES=claude,codex,cursor\nBEACON_OTLP_HTTP_PORT=6318\nBEACON_SPLUNK_INDEX=fleet\n' >"$MANAGED.plist"
chmod 644 "$MANAGED.plist"
# run_fleet [ARG ...] runs the Fleet wrapper the way run_install runs the install script.
run_fleet() {
  rm -f "$ARGS"
  env -i PATH="$PATH" HOME="$TMP_DIR" BEACON_BIN="$STUB" BEACON_TEST_ARGS="$ARGS" \
    BEACON_INSTALL_SCRIPT="$INSTALL_SCRIPT" \
    BEACON_SYSTEM_CONFIG="$TMP_DIR/system/config.json" \
    BEACON_SYSTEM_LOG="$TMP_DIR/system/runtime.jsonl" \
    BEACON_DEFAULTS_BIN="$DEFAULTS_STUB" \
    BEACON_MANAGED_PREFS="$MANAGED" \
    BEACON_LOCAL_PREFS="$LOCAL" \
    BEACON_SETTINGS_OWNER="$(id -u)" \
    BEACON_INSTALLER_GUI=0 \
    sh "$ROOT_DIR/packaging/macos/fleet/scripts/install.sh" "$@" >"$TMP_DIR/out" 2>&1 || { cat "$TMP_DIR/out" >&2; return 1; }
}
run_fleet
expect --harness claude,codex,cursor "Fleet wrapper with no arguments: managed harnesses"
expect --otlp-http-port 6318 "Fleet wrapper with no arguments: managed HTTP port"
expect --otlp-grpc-port 4317 "Fleet wrapper with no arguments: default gRPC port"
expect --splunk-index fleet "Fleet wrapper with no arguments: managed Splunk index"
run_fleet codex
expect --harness codex "Fleet wrapper argument over managed preferences"
# An empty variable, as a wrapper or an MDM parameter left blank passes it, does not shadow the
# preferences either; an empty BEACON_LOG_READERS still means nobody.
run_install BEACON_ENDPOINT_HARNESSES= BEACON_SPLUNK_INDEX=
expect --harness claude,codex,cursor "empty BEACON_ENDPOINT_HARNESSES shadowed managed preferences"
expect --splunk-index fleet "empty BEACON_SPLUNK_INDEX shadowed managed preferences"
rm -f "$MANAGED.plist"

if [ "$failures" -ne 0 ]; then
  echo "$failures check(s) failed" >&2
  exit 1
fi
echo "macos install settings checks passed"
