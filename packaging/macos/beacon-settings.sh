# Beacon package install settings on macOS, shared by install-endpoint.sh and the package postinstall.
#
# Sourced, not run: it defines functions and nothing else. The macOS counterpart of
# packaging/linux/beacon-settings.sh (the settings file in /etc/default or /etc/sysconfig) and of the
# Windows registry policy read by packaging/windows/install-endpoint.ps1: the same BEACON_* names,
# kept where each platform's fleet tooling already writes settings.
#
# An MDM (Jamf, Kandji, Intune, Workspace ONE) installs the package with `installer` or its own agent
# and cannot set the environment of the package's scripts, so the BEACON_* variables could not reach
# a centrally deployed install any other way. It can push a configuration profile, which macOS
# writes as managed preferences: payload type com.beacon.endpoint, keys named exactly like the
# variables (BEACON_LOG_ACCESS = restricted, BEACON_LOG_READERS = "", ...). Without an MDM, an
# administrator can write the same keys to the local preferences:
#
#   sudo defaults write /Library/Preferences/com.beacon.endpoint BEACON_LOG_ACCESS restricted
#
# Managed preferences win over local ones, and a variable already set in the environment wins over
# both (a variable set to an empty string counts as unset, except BEACON_LOG_READERS). Only the documented install settings are read, one key at a time with `defaults read`, so
# nothing in the files runs. A file is ignored unless root owns it and no one else can write it,
# because the package scripts run as root. A value that is not a single line (an array or a
# dictionary) is skipped with a warning naming only its key.

# The documented install settings, the only keys read.
BEACON_SETTINGS_KEYS='BEACON_ENDPOINT_HARNESSES BEACON_OTLP_GRPC_PORT BEACON_OTLP_HTTP_PORT
BEACON_COLLECTOR BEACON_NO_START BEACON_LOG_ACCESS BEACON_LOG_READERS BEACON_DASHBOARD
BEACON_SESSION_SYNC BEACON_SPLUNK_HEC_ENDPOINT BEACON_SPLUNK_HEC_TOKEN BEACON_SPLUNK_INDEX
BEACON_SPLUNK_SOURCE BEACON_SPLUNK_SOURCETYPE BEACON_SPLUNK_INSECURE_SKIP_VERIFY BEACON_SPLUNK_CA_FILE
BEACON_FALCON_HEC_ENDPOINT BEACON_FALCON_HEC_TOKEN BEACON_FALCON_INDEX BEACON_FALCON_SOURCE
BEACON_FALCON_SOURCETYPE BEACON_FALCON_INSECURE_SKIP_VERIFY BEACON_FALCON_CA_FILE'

# beacon_prefs_trusted FILE succeeds when FILE is owned by root and not group- or world-writable.
beacon_prefs_trusted() {
  # GNU stat first, where the packaging tests run on Linux (its -f means file system status, so it
  # cannot be tried second); BSD stat on macOS rejects -c and answers -f.
  _beacon_meta="$(stat -c '%u %a' "$1" 2>/dev/null || stat -f '%u %Lp' "$1" 2>/dev/null)" || return 1
  # BEACON_SETTINGS_OWNER lets the packaging tests run as an ordinary account.
  [ "${_beacon_meta%% *}" = "${BEACON_SETTINGS_OWNER:-0}" ] || return 1
  case "${_beacon_meta##* }" in
    *[2367]?|*[2367]) return 1 ;;
  esac
  return 0
}

# beacon_load_settings exports the documented settings from managed, then local, preferences that
# are not already set in the environment.
beacon_load_settings() {
  _beacon_defaults="${BEACON_DEFAULTS_BIN:-/usr/bin/defaults}"
  [ -x "$_beacon_defaults" ] || return 0
  # Which settings are already set is decided once, before any file is read. A variable set to an
  # empty string counts as unset, because wrappers (the Fleet install script, a Jamf policy with an
  # empty parameter) pass every variable whether or not they were given a value, and an empty one
  # must not shadow the profile. The exception is BEACON_LOG_READERS, where empty is a value of its
  # own: nobody is added.
  _beacon_preset=' '
  for _beacon_key in $BEACON_SETTINGS_KEYS; do
    # $_beacon_key is one of the fixed names above, so the indirection cannot evaluate anything else.
    if [ "$_beacon_key" = BEACON_LOG_READERS ]; then
      eval "_beacon_set=\${$_beacon_key+set}"
    else
      eval "_beacon_set=\${$_beacon_key:+set}"
    fi
    [ -n "$_beacon_set" ] && _beacon_preset="$_beacon_preset$_beacon_key "
  done
  _beacon_taken=' '
  for _beacon_prefs in "${BEACON_MANAGED_PREFS:-/Library/Managed Preferences/com.beacon.endpoint}" \
    "${BEACON_LOCAL_PREFS:-/Library/Preferences/com.beacon.endpoint}"; do
    [ -f "$_beacon_prefs.plist" ] || continue
    if ! beacon_prefs_trusted "$_beacon_prefs.plist"; then
      echo "beacon: ignoring $_beacon_prefs.plist: it must be owned by root and writable by no one else" >&2
      continue
    fi
    for _beacon_key in $BEACON_SETTINGS_KEYS; do
      case "$_beacon_preset" in
        *" $_beacon_key "*) continue ;;
      esac
      # Taken from the first file that has it: managed preferences shadow local ones.
      case "$_beacon_taken" in
        *" $_beacon_key "*) continue ;;
      esac
      _beacon_value="$("$_beacon_defaults" read "$_beacon_prefs" "$_beacon_key" 2>/dev/null)" || continue
      case "$_beacon_value" in
        *"
"*|'('*|'{'*)
          # The key only, never the value: it may be a token.
          echo "beacon: ignoring $_beacon_key in $_beacon_prefs.plist: not a single value" >&2
          continue
          ;;
      esac
      export "$_beacon_key=$_beacon_value"
      _beacon_taken="$_beacon_taken$_beacon_key "
    done
  done
}

# beacon_settings_harnesses prints BEACON_ENDPOINT_HARNESSES as the environment or the preferences
# set it, or nothing, without exporting anything into the caller: the postinstall needs only this one
# value, and loading every setting into it would hand a forwarder token to the processes it starts.
beacon_settings_harnesses() {
  (
    beacon_load_settings
    printf '%s' "${BEACON_ENDPOINT_HARNESSES:-}"
  )
}

# beacon_installer_app_running CONSOLE succeeds when Apple's Installer.app is running as CONSOLE.
#
# A process name proves nothing: any account can start a program called Installer. So each Installer
# process the console user owns is checked by the executable the kernel says it runs (its `txt`
# mapping in lsof, which comes from the vnode, not from anything the process chose), and only Apple's
# /System/Library/CoreServices/Installer.app on the sealed system volume counts.
beacon_installer_app_running() {
  _beacon_pgrep="${BEACON_PGREP_BIN:-/usr/bin/pgrep}"
  _beacon_lsof="${BEACON_LSOF_BIN:-/usr/sbin/lsof}"
  [ -x "$_beacon_pgrep" ] && [ -x "$_beacon_lsof" ] || return 1
  for _beacon_pid in $("$_beacon_pgrep" -x -u "$1" Installer 2>/dev/null); do
    case "$_beacon_pid" in
      ''|*[!0-9]*) continue ;;
    esac
    # -Fn prints one `n<path>` line per file; the first txt file is the program itself.
    _beacon_exe="$("$_beacon_lsof" -a -p "$_beacon_pid" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1)"
    [ "$_beacon_exe" = /System/Library/CoreServices/Installer.app/Contents/MacOS/Installer ] && return 0
  done
  return 1
}

# beacon_installing_person prints the account of the person who ran this install in Installer.app,
# or nothing for an unattended install. `installer` (which MDM agents and scripts run) sets
# COMMAND_LINE_INSTALL; a person double-clicking the package does it in Installer.app, the console
# user's own session, which beacon_installer_app_running checks by the executable rather than by a
# process name. Both must hold, so an MDM install that happens while someone is logged in adds
# nobody. MDM InstallApplication never sets COMMAND_LINE_INSTALL, so a fleet that does not want even
# that check deciding sets BEACON_LOG_READERS to an empty string in its profile.
beacon_installing_person() {
  [ -z "${COMMAND_LINE_INSTALL:-}" ] || return 0
  _beacon_console="${BEACON_CONSOLE_USER:-$(stat -f '%Su' /dev/console 2>/dev/null || true)}"
  case "$_beacon_console" in
    ''|root|loginwindow|_mbsetupuser|-*) return 0 ;;
  esac
  # BEACON_INSTALLER_GUI=1|0 lets the packaging tests, which have no Installer.app, say either way.
  _beacon_gui="${BEACON_INSTALLER_GUI:-}"
  if [ -z "$_beacon_gui" ]; then
    if beacon_installer_app_running "$_beacon_console"; then _beacon_gui=1; else _beacon_gui=0; fi
  fi
  [ "$_beacon_gui" = 1 ] || return 0
  printf '%s' "$_beacon_console"
}
