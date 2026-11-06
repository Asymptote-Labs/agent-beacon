#!/bin/sh
# Installs the Beacon endpoint in system mode.
#
# Called by the .deb/.rpm postinstall, and usable directly for fleet tooling. Configuration is
# environment-first with positional fallbacks, matching packaging/macos/install-endpoint.sh so a
# mixed fleet has one contract rather than two.
set -eu

# Settings a configuration manager drops before installing the package. Salt's pkg.installed,
# Ansible's apt/dnf modules, Puppet and Chef all run the package manager without letting the caller
# set the environment of the maintainer scripts, so the BEACON_* variables below could not reach a
# centrally deployed install any other way. /etc/default is the Debian convention and
# /etc/sysconfig the RHEL one; the first that exists is read.
#
# The file is read, never sourced: only the variables this script documents are taken, as plain
# KEY=VALUE lines (an optional `export`, matching single or double quotes, comments), so nothing in
# it runs. It is skipped unless root owns it and no one else can write it, because this script runs
# as root from the package manager. A variable already set in the environment wins over the file.
BEACON_DEFAULTS_FILE="${BEACON_DEFAULTS_FILE:-}"
if [ -z "$BEACON_DEFAULTS_FILE" ]; then
  for candidate in /etc/default/beacon /etc/sysconfig/beacon; do
    if [ -f "$candidate" ]; then
      BEACON_DEFAULTS_FILE="$candidate"
      break
    fi
  done
fi

beacon_defaults_trusted() {
  # Owner uid and permission bits, from GNU stat (every Linux userland Beacon packages for) or
  # BusyBox, which accepts the same format.
  meta="$(stat -c '%u %a' "$1" 2>/dev/null)" || return 1
  owner="${meta%% *}"
  mode="${meta##* }"
  # BEACON_DEFAULTS_OWNER lets the packaging tests run as an ordinary account.
  [ "$owner" = "${BEACON_DEFAULTS_OWNER:-0}" ] || return 1
  # The last two octal digits are group and other: neither may include write (2, 3, 6, 7).
  case "$mode" in
    *[2367]?|*[2367]) return 1 ;;
  esac
  return 0
}

if [ -n "$BEACON_DEFAULTS_FILE" ] && [ -f "$BEACON_DEFAULTS_FILE" ]; then
  if beacon_defaults_trusted "$BEACON_DEFAULTS_FILE"; then
    while IFS= read -r line || [ -n "$line" ]; do
      line="${line#"${line%%[![:space:]]*}"}"
      case "$line" in
        ''|'#'*) continue ;;
        export\ *) line="${line#export }"; line="${line#"${line%%[![:space:]]*}"}" ;;
      esac
      case "$line" in
        *=*) ;;
        *) continue ;;
      esac
      key="${line%%=*}"
      value="${line#*=}"
      case "$key" in
        BEACON_ENDPOINT_HARNESSES|BEACON_OTLP_GRPC_PORT|BEACON_OTLP_HTTP_PORT|BEACON_COLLECTOR|\
        BEACON_NO_START|BEACON_SERVICE|BEACON_LOG_ACCESS|BEACON_SESSION_SYNC|\
        BEACON_SPLUNK_HEC_ENDPOINT|BEACON_SPLUNK_HEC_TOKEN|BEACON_SPLUNK_INDEX|BEACON_SPLUNK_SOURCE|\
        BEACON_SPLUNK_SOURCETYPE|BEACON_SPLUNK_INSECURE_SKIP_VERIFY|BEACON_SPLUNK_CA_FILE) ;;
        *) continue ;;
      esac
      # Trailing whitespace, then one pair of matching quotes.
      value="${value%"${value##*[![:space:]]}"}"
      case "$value" in
        \"*\") value="${value#\"}"; value="${value%\"}" ;;
        \'*\') value="${value#\'}"; value="${value%\'}" ;;
      esac
      # $key is one of the names above, so the indirection below cannot evaluate anything else.
      eval "already=\${$key+set}"
      if [ -z "$already" ]; then
        export "$key=$value"
      fi
    done <"$BEACON_DEFAULTS_FILE"
  else
    echo "beacon: ignoring $BEACON_DEFAULTS_FILE: it must be owned by root and writable by no one else" >&2
  fi
fi

if [ -z "${BEACON_BIN:-}" ]; then
  if [ -x "/opt/beacon/bin/beacon" ]; then
    BEACON_BIN="/opt/beacon/bin/beacon"
  else
    BEACON_BIN="beacon"
  fi
fi

BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-${1:-claude,codex}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${2:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${3:-4318}}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-${4:-}}"
BEACON_NO_START="${BEACON_NO_START:-${5:-0}}"
BEACON_SERVICE="${BEACON_SERVICE:-${6:-}}"
BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-}"
BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-}"
BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-}"
BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-}"
BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-}"
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-0}"
BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-}"
# shared or restricted; empty keeps the mode the endpoint already has, so a package upgrade never
# changes it.
BEACON_LOG_ACCESS="${BEACON_LOG_ACCESS:-}"
# A first install from the package defaults to restricted: a package is installed by root for the
# whole machine, often by a configuration manager across a fleet, and that is the install that
# should make the collector the only writer of the system log without a second step. "First" means
# there is no system config yet; an upgrade, or a reinstall over a kept config, keeps whatever mode
# the endpoint has, so this never narrows (or widens) a running endpoint behind its operator's back.
# BEACON_LOG_ACCESS=shared opts a new install out.
BEACON_SYSTEM_CONFIG="${BEACON_SYSTEM_CONFIG:-/etc/beacon/endpoint/config.json}"
if [ -z "$BEACON_LOG_ACCESS" ] && [ ! -e "$BEACON_SYSTEM_CONFIG" ]; then
  BEACON_LOG_ACCESS=restricted
fi
# 1 turns the scheduled session sync on and 0 turns it off; empty keeps what the endpoint already
# has, so a package upgrade never changes it.
BEACON_SESSION_SYNC="${BEACON_SESSION_SYNC:-}"

# The packaged collector sits beside the CLI. Auto-detected rather than required, so a manual
# invocation without BEACON_COLLECTOR still works.
if [ -z "$BEACON_COLLECTOR" ] && [ -x "/opt/beacon/bin/beacon-otelcol" ]; then
  BEACON_COLLECTOR="/opt/beacon/bin/beacon-otelcol"
fi

set -- endpoint install --system \
  --harness "$BEACON_ENDPOINT_HARNESSES" \
  --otlp-grpc-port "$BEACON_OTLP_GRPC_PORT" \
  --otlp-http-port "$BEACON_OTLP_HTTP_PORT"

[ -n "$BEACON_COLLECTOR" ] && set -- "$@" --collector "$BEACON_COLLECTOR"
# Empty means auto-detect: systemd when it is PID 1, otherwise a supervised collector.
[ -n "$BEACON_SERVICE" ] && set -- "$@" --service "$BEACON_SERVICE"
[ "$BEACON_NO_START" = "1" ] && set -- "$@" --no-start
[ -n "$BEACON_LOG_ACCESS" ] && set -- "$@" --log-access "$BEACON_LOG_ACCESS"
case "$BEACON_SESSION_SYNC" in
  1|true|TRUE|yes|YES)
    set -- "$@" --session-sync
    ;;
  0|false|FALSE|no|NO)
    set -- "$@" --no-session-sync
    ;;
esac

[ -n "$BEACON_SPLUNK_HEC_ENDPOINT" ] && set -- "$@" --splunk-hec-endpoint "$BEACON_SPLUNK_HEC_ENDPOINT"
[ -n "$BEACON_SPLUNK_HEC_TOKEN" ] && set -- "$@" --splunk-hec-token "$BEACON_SPLUNK_HEC_TOKEN"
[ -n "$BEACON_SPLUNK_INDEX" ] && set -- "$@" --splunk-index "$BEACON_SPLUNK_INDEX"
[ -n "$BEACON_SPLUNK_SOURCE" ] && set -- "$@" --splunk-source "$BEACON_SPLUNK_SOURCE"
[ -n "$BEACON_SPLUNK_SOURCETYPE" ] && set -- "$@" --splunk-sourcetype "$BEACON_SPLUNK_SOURCETYPE"
[ "$BEACON_SPLUNK_INSECURE_SKIP_VERIFY" = "1" ] && set -- "$@" --splunk-insecure-skip-verify
[ -n "$BEACON_SPLUNK_CA_FILE" ] && set -- "$@" --splunk-ca-file "$BEACON_SPLUNK_CA_FILE"

exec "$BEACON_BIN" "$@"
