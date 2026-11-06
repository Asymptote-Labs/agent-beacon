# Beacon package install settings, shared by install-endpoint.sh and the package postinstall.
#
# Sourced, not run: it defines beacon_load_settings and nothing else. Both scripts need the same
# answer, because the postinstall runs two separate steps (the system install and configuring each
# logged-in user) and a setting that reached only the first was silently ignored by the second.
#
# Settings a configuration manager drops before installing the package. Salt's pkg.installed,
# Ansible's apt/dnf modules, Puppet and Chef all run the package manager without letting the caller
# set the environment of the maintainer scripts, so the BEACON_* variables could not reach a
# centrally deployed install any other way. /etc/default is the Debian convention and
# /etc/sysconfig the RHEL one; the first that exists is read (BEACON_DEFAULTS_FILE names another).
#
# The file is read, never sourced: only the documented install settings are taken, as KEY=VALUE
# lines in the shell syntax such files are written in (an optional `export`, one pair of matching
# single or double quotes, whole-line and trailing `# comments`), so nothing in it runs. A value with
# anything else left over (an unquoted space, an unterminated quote) is skipped with a warning rather
# than passed on. The file is ignored unless root owns it and no one else can write it, because the
# package scripts run as root. A variable already set in the environment wins over the file.

# beacon_defaults_trusted FILE succeeds when FILE is owned by root and not group- or world-writable.
beacon_defaults_trusted() {
  # Owner uid and permission bits, from GNU stat (every Linux userland Beacon packages for) or
  # BusyBox, which accepts the same format.
  _beacon_meta="$(stat -c '%u %a' "$1" 2>/dev/null)" || return 1
  # BEACON_DEFAULTS_OWNER lets the packaging tests run as an ordinary account.
  [ "${_beacon_meta%% *}" = "${BEACON_DEFAULTS_OWNER:-0}" ] || return 1
  # The last two octal digits are group and other: neither may include write (2, 3, 6, 7).
  case "${_beacon_meta##* }" in
    *[2367]?|*[2367]) return 1 ;;
  esac
  return 0
}

# beacon_settings_value RAW prints the value of an assignment's right-hand side, or fails when the
# text is not one plain value.
beacon_settings_value() {
  _beacon_raw="$1"
  # Leading and trailing whitespace.
  _beacon_raw="${_beacon_raw#"${_beacon_raw%%[![:space:]]*}"}"
  _beacon_raw="${_beacon_raw%"${_beacon_raw##*[![:space:]]}"}"
  case "$_beacon_raw" in
    \"*|\'*)
      _beacon_q="$(printf '%s' "$_beacon_raw" | cut -c1)"
      _beacon_inner="${_beacon_raw#?}"
      case "$_beacon_inner" in
        *"$_beacon_q"*) ;;
        *) return 1 ;; # unterminated
      esac
      _beacon_rest="${_beacon_inner#*"$_beacon_q"}"
      _beacon_inner="${_beacon_inner%%"$_beacon_q"*}"
      # After the closing quote only whitespace, then optionally a comment, may follow. A `#` right
      # after the quote is not a comment to a shell but more of the word, so it is refused too.
      case "$_beacon_rest" in
        '') printf '%s' "$_beacon_inner"; return 0 ;;
        [[:space:]]*) ;;
        *) return 1 ;;
      esac
      _beacon_rest="${_beacon_rest#"${_beacon_rest%%[![:space:]]*}"}"
      case "$_beacon_rest" in
        ''|'#'*) printf '%s' "$_beacon_inner"; return 0 ;;
      esac
      return 1
      ;;
  esac
  # Unquoted: a `#` starts a comment only at the start or after whitespace, as in the shell.
  case "$_beacon_raw" in
    '#'*) _beacon_raw='' ;;
  esac
  case "$_beacon_raw" in
    *[[:space:]]'#'*)
      _beacon_raw="$(printf '%s' "$_beacon_raw" | sed 's/[[:space:]]#.*$//')"
      ;;
  esac
  _beacon_raw="${_beacon_raw%"${_beacon_raw##*[![:space:]]}"}"
  # Whitespace left inside an unquoted value would be a second word to a shell.
  case "$_beacon_raw" in
    *[[:space:]]*) return 1 ;;
  esac
  printf '%s' "$_beacon_raw"
}

# beacon_load_settings exports the documented settings from the defaults file that are not already
# set in the environment.
beacon_load_settings() {
  _beacon_file="${BEACON_DEFAULTS_FILE:-}"
  if [ -z "$_beacon_file" ]; then
    for _beacon_candidate in /etc/default/beacon /etc/sysconfig/beacon; do
      if [ -f "$_beacon_candidate" ]; then
        _beacon_file="$_beacon_candidate"
        break
      fi
    done
  fi
  [ -n "$_beacon_file" ] && [ -f "$_beacon_file" ] || return 0
  if ! beacon_defaults_trusted "$_beacon_file"; then
    echo "beacon: ignoring $_beacon_file: it must be owned by root and writable by no one else" >&2
    return 0
  fi
  while IFS= read -r _beacon_line || [ -n "$_beacon_line" ]; do
    _beacon_line="${_beacon_line#"${_beacon_line%%[![:space:]]*}"}"
    case "$_beacon_line" in
      ''|'#'*) continue ;;
      export[[:space:]]*)
        _beacon_line="${_beacon_line#export}"
        _beacon_line="${_beacon_line#"${_beacon_line%%[![:space:]]*}"}"
        ;;
    esac
    case "$_beacon_line" in
      *=*) ;;
      *) continue ;;
    esac
    _beacon_key="${_beacon_line%%=*}"
    case "$_beacon_key" in
      BEACON_ENDPOINT_HARNESSES|BEACON_OTLP_GRPC_PORT|BEACON_OTLP_HTTP_PORT|BEACON_COLLECTOR|\
      BEACON_NO_START|BEACON_SERVICE|BEACON_LOG_ACCESS|BEACON_SESSION_SYNC|\
      BEACON_SPLUNK_HEC_ENDPOINT|BEACON_SPLUNK_HEC_TOKEN|BEACON_SPLUNK_INDEX|BEACON_SPLUNK_SOURCE|\
      BEACON_SPLUNK_SOURCETYPE|BEACON_SPLUNK_INSECURE_SKIP_VERIFY|BEACON_SPLUNK_CA_FILE) ;;
      *) continue ;;
    esac
    if ! _beacon_value="$(beacon_settings_value "${_beacon_line#*=}")"; then
      # The key only, never the value: it may be a token.
      echo "beacon: ignoring $_beacon_key in $_beacon_file: not a single value (quote a value with spaces)" >&2
      continue
    fi
    # $_beacon_key is one of the names above, so the indirection cannot evaluate anything else.
    eval "_beacon_set=\${$_beacon_key+set}"
    if [ -z "$_beacon_set" ]; then
      export "$_beacon_key=$_beacon_value"
    fi
  done <"$_beacon_file"
}
