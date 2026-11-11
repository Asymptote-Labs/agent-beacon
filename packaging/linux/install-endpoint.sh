#!/bin/sh
# Installs the Beacon endpoint in system mode.
#
# Called by the .deb/.rpm postinstall, and usable directly for fleet tooling. Configuration is
# environment-first with positional fallbacks, matching packaging/macos/install-endpoint.sh so a
# mixed fleet has one contract rather than two.
set -eu

# The install settings a configuration manager can drop in /etc/default/beacon or
# /etc/sysconfig/beacon; see beacon-settings.sh. It sits beside this script in the package
# (/opt/beacon/scripts) and in the repository.
BEACON_SCRIPTS_DIR="${BEACON_SCRIPTS_DIR:-$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)}"
# Positional arguments are named on the command line, so like the environment they win over the
# settings file: each is resolved into its variable before the file is read, which then fills only
# what neither gave.
[ -n "${1:-}" ] && BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-$1}"
[ -n "${2:-}" ] && BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-$2}"
[ -n "${3:-}" ] && BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-$3}"
[ -n "${4:-}" ] && BEACON_COLLECTOR="${BEACON_COLLECTOR:-$4}"
[ -n "${5:-}" ] && BEACON_NO_START="${BEACON_NO_START:-$5}"
[ -n "${6:-}" ] && BEACON_SERVICE="${BEACON_SERVICE:-$6}"
# shellcheck source=beacon-settings.sh
. "$BEACON_SCRIPTS_DIR/beacon-settings.sh"
beacon_load_settings

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
# The CLI takes the mode in any case and ignores surrounding blanks, so the checks below must too:
# `Restricted` from a settings file installs a restricted log, and has to add its readers.
BEACON_LOG_ACCESS="$(printf '%s' "$BEACON_LOG_ACCESS" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
# A first install from the package defaults to restricted: a package is installed by root for the
# whole machine, often by a configuration manager across a fleet, and that is the install that
# should make the collector the only writer of the system log without a second step.
#
# Only a genuinely first install, and only where the collector will be a systemd service:
#   - a system config means an upgrade or a reinstall over a kept config, and a runtime log left
#     behind by `uninstall --keep-logs` means a reinstall whose old mode (recorded beside that log
#     when it was restricted) the install keeps on its own. Either way no mode is passed, so this
#     never narrows or widens an existing endpoint behind its operator's back.
#   - without systemd as PID 1 (a container, WSL without systemd) or with BEACON_SERVICE=none, the
#     collector is a supervised process nothing restarts. A restricted log there would leave every
#     hook event spooled in its user's home whenever that process is down, where a shared log keeps
#     taking them directly, so those installs stay shared unless asked.
# BEACON_LOG_ACCESS=shared opts a new install out.
BEACON_SYSTEM_CONFIG="${BEACON_SYSTEM_CONFIG:-/etc/beacon/endpoint/config.json}"
BEACON_SYSTEM_LOG="${BEACON_SYSTEM_LOG:-/var/log/beacon-agent/runtime.jsonl}"
BEACON_INIT_COMM="${BEACON_INIT_COMM:-/proc/1/comm}"
first_install=0
if [ ! -e "$BEACON_SYSTEM_CONFIG" ] && [ ! -e "$BEACON_SYSTEM_LOG" ]; then
  first_install=1
fi
if [ -z "$BEACON_LOG_ACCESS" ] && [ "$first_install" = 1 ]; then
  init="$(cat "$BEACON_INIT_COMM" 2>/dev/null || true)"
  case "$BEACON_SERVICE" in
    ''|auto|systemd)
      if [ "$init" = systemd ]; then
        BEACON_LOG_ACCESS=restricted
      fi
      ;;
  esac
fi
# Accounts to add to the beacon-log group of a restricted log, comma-separated. Set, even to
# nothing, it is exactly the list: an unattended fleet install sets BEACON_LOG_READERS= so that nobody
# is ever added implicitly. Unset, a first install that a person ran with sudo at a terminal
# (`sudo apt install`, `sudo dpkg -i`) adds that person, so whoever installed Beacon on their own
# machine can use the dashboard, `beacon traces` and `beacon share` without a second step. A
# configuration manager running as root has no SUDO_USER, and one that reaches root through sudo
# (Ansible `become`, a Packer sudo provisioner) sets SUDO_USER to its automation login, so SUDO_USER
# alone is not a person: the install must also have a controlling terminal and not run under
# DEBIAN_FRONTEND=noninteractive, which Ansible's apt module sets. Otherwise nobody is added, and the
# postinstall prints how to add someone. Automation that allocates a terminal (ssh -tt) passes this
# check, so a fleet sets BEACON_LOG_READERS= to add nobody whatever runs the install. Members are
# only ever added, never removed.
#
# beacon_person_at_terminal succeeds when a person is running this install interactively. It asks
# for a controlling terminal (/dev/tty) rather than a terminal on standard input, because rpm runs
# scriptlets with standard input on a pipe of its own even under `sudo dnf install` at a terminal;
# the controlling terminal is the session's and survives that. BEACON_INSTALL_TTY=1|0 lets the
# packaging tests, which run without a terminal, say either way.
beacon_person_at_terminal() {
  [ "${DEBIAN_FRONTEND:-}" != noninteractive ] || return 1
  case "${BEACON_INSTALL_TTY:-}" in
    1) return 0 ;;
    0) return 1 ;;
  esac
  # A subshell: a failed redirection on a builtin would end this non-interactive shell.
  ( : <"${BEACON_CONTROLLING_TTY:-/dev/tty}" ) 2>/dev/null
}
if [ -z "${BEACON_LOG_READERS+set}" ]; then
  BEACON_LOG_READERS=
  if [ "$first_install" = 1 ] && [ "$BEACON_LOG_ACCESS" = restricted ]; then
    case "${SUDO_USER:-}" in
      ''|root) ;;
      *) beacon_person_at_terminal && BEACON_LOG_READERS="$SUDO_USER" ;;
    esac
  fi
fi
# enabled or disabled: whether any account may run `beacon endpoint dashboard`. Empty keeps what the
# endpoint already has, so a package upgrade never changes it. An unattended first install (a
# configuration manager, or anything else with no person at a terminal behind sudo, by the same test
# as BEACON_LOG_READERS above) defaults to disabled: that install sets up the machine for the people
# who use it, who should not be able to open a view of its telemetry unless the administrator says
# so. A person who installs the package on their own machine keeps the dashboard.
# BEACON_DASHBOARD=enabled or disabled decides either way.
BEACON_DASHBOARD="${BEACON_DASHBOARD:-}"
BEACON_DASHBOARD="$(printf '%s' "$BEACON_DASHBOARD" | tr -d '[:space:]')"
if [ -z "$BEACON_DASHBOARD" ] && [ "$first_install" = 1 ]; then
  case "${SUDO_USER:-}" in
    ''|root) BEACON_DASHBOARD=disabled ;;
    *) beacon_person_at_terminal || BEACON_DASHBOARD=disabled ;;
  esac
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
[ -n "$BEACON_LOG_READERS" ] && set -- "$@" --log-readers "$BEACON_LOG_READERS"
[ -n "$BEACON_DASHBOARD" ] && set -- "$@" --dashboard "$BEACON_DASHBOARD"
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
