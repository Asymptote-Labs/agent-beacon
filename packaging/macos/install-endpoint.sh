#!/bin/sh
set -eu

# The install settings an MDM pushes as managed preferences (com.beacon.endpoint); see
# beacon-settings.sh beside this script (/opt/beacon/scripts in the package).
BEACON_SCRIPTS_DIR="${BEACON_SCRIPTS_DIR:-$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)}"
# Positional arguments (Jamf passes them from $4) are named on the command line, so like the
# environment they win over the preferences: each is resolved into its variable before those are read.
[ -n "${4:-}" ] && BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-$4}"
[ -n "${5:-}" ] && BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-$5}"
[ -n "${6:-}" ] && BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-$6}"
[ -n "${7:-}" ] && BEACON_COLLECTOR="${BEACON_COLLECTOR:-$7}"
[ -n "${8:-}" ] && BEACON_NO_START="${BEACON_NO_START:-$8}"
[ -n "${9:-}" ] && BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-$9}"
[ -n "${10:-}" ] && BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-${10}}"
[ -n "${11:-}" ] && BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-${11}}"
[ -n "${12:-}" ] && BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-${12}}"
[ -n "${13:-}" ] && BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-${13}}"
[ -n "${14:-}" ] && BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-${14}}"
[ -n "${15:-}" ] && BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-${15}}"
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

BEACON_ENDPOINT_HARNESSES="${BEACON_ENDPOINT_HARNESSES:-${4:-claude,codex}}"
BEACON_OTLP_GRPC_PORT="${BEACON_OTLP_GRPC_PORT:-${5:-4317}}"
BEACON_OTLP_HTTP_PORT="${BEACON_OTLP_HTTP_PORT:-${6:-4318}}"
BEACON_COLLECTOR="${BEACON_COLLECTOR:-${7:-}}"
BEACON_NO_START="${BEACON_NO_START:-${8:-0}}"
BEACON_SPLUNK_HEC_ENDPOINT="${BEACON_SPLUNK_HEC_ENDPOINT:-${9:-}}"
BEACON_SPLUNK_HEC_TOKEN="${BEACON_SPLUNK_HEC_TOKEN:-${10:-}}"
BEACON_SPLUNK_INDEX="${BEACON_SPLUNK_INDEX:-${11:-}}"
BEACON_SPLUNK_SOURCE="${BEACON_SPLUNK_SOURCE:-${12:-}}"
BEACON_SPLUNK_SOURCETYPE="${BEACON_SPLUNK_SOURCETYPE:-${13:-}}"
BEACON_SPLUNK_INSECURE_SKIP_VERIFY="${BEACON_SPLUNK_INSECURE_SKIP_VERIFY:-${14:-0}}"
BEACON_SPLUNK_CA_FILE="${BEACON_SPLUNK_CA_FILE:-${15:-}}"
BEACON_FALCON_HEC_ENDPOINT="${BEACON_FALCON_HEC_ENDPOINT:-}"
BEACON_FALCON_HEC_TOKEN="${BEACON_FALCON_HEC_TOKEN:-}"
BEACON_FALCON_INDEX="${BEACON_FALCON_INDEX:-}"
BEACON_FALCON_SOURCE="${BEACON_FALCON_SOURCE:-}"
BEACON_FALCON_SOURCETYPE="${BEACON_FALCON_SOURCETYPE:-}"
BEACON_FALCON_INSECURE_SKIP_VERIFY="${BEACON_FALCON_INSECURE_SKIP_VERIFY:-0}"
BEACON_FALCON_CA_FILE="${BEACON_FALCON_CA_FILE:-}"
# shared or restricted; empty keeps the mode the endpoint already has, so a package upgrade never
# changes it.
BEACON_LOG_ACCESS="${BEACON_LOG_ACCESS:-}"
# The CLI takes the mode in any case and ignores surrounding blanks, so the checks below must too:
# `Restricted` from a settings file installs a restricted log, and has to add its readers.
BEACON_LOG_ACCESS="$(printf '%s' "$BEACON_LOG_ACCESS" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
# A first install from the package defaults to restricted, as on Linux and Windows: a package is
# installed by an administrator for the whole machine, often by an MDM across a fleet, and that is
# the install that should make the collector the only writer of the system log without a second
# step. Only a genuinely first install: an existing system config (an upgrade, or a reinstall over a
# kept config) or a runtime log left by `uninstall --keep-logs` passes no mode, so this never narrows
# or widens an existing endpoint. BEACON_LOG_ACCESS=shared opts a new install out.
BEACON_SYSTEM_CONFIG="${BEACON_SYSTEM_CONFIG:-/Library/Application Support/Beacon/Endpoint/config.json}"
BEACON_SYSTEM_LOG="${BEACON_SYSTEM_LOG:-/var/log/beacon-agent/runtime.jsonl}"
first_install=0
if [ ! -e "$BEACON_SYSTEM_CONFIG" ] && [ ! -e "$BEACON_SYSTEM_LOG" ]; then
  first_install=1
fi
if [ -z "$BEACON_LOG_ACCESS" ] && [ "$first_install" = 1 ]; then
  BEACON_LOG_ACCESS=restricted
fi
# Accounts to add to the beacon-log group of a restricted log, comma-separated. Set, even to nothing,
# it is exactly the list: an MDM sets BEACON_LOG_READERS to an empty string so that nobody is ever
# added implicitly. Unset, a first install that a person ran in Installer.app adds that person, so
# whoever installed Beacon on their own Mac can use the dashboard, `beacon traces` and `beacon share`
# without a second step. Members are only ever added, never removed.
if [ -z "${BEACON_LOG_READERS+set}" ]; then
  BEACON_LOG_READERS=
  if [ "$first_install" = 1 ] && [ "$BEACON_LOG_ACCESS" = restricted ]; then
    BEACON_LOG_READERS="$(beacon_installing_person)"
  fi
fi
# enabled or disabled: whether any account may run `beacon endpoint dashboard`. Empty keeps what the
# endpoint already has, so a package upgrade never changes it. An unattended first install (an MDM,
# `installer`, anything but a person in Installer.app, by the same test as BEACON_LOG_READERS above)
# defaults to disabled: that install sets up the machine for the people who use it, who should not
# be able to open a view of its telemetry unless the administrator says so. A person who installs
# the package on their own Mac keeps the dashboard. BEACON_DASHBOARD=enabled or disabled decides
# either way.
BEACON_DASHBOARD="${BEACON_DASHBOARD:-}"
BEACON_DASHBOARD="$(printf '%s' "$BEACON_DASHBOARD" | tr -d '[:space:]')"
if [ -z "$BEACON_DASHBOARD" ] && [ "$first_install" = 1 ] && [ -z "$(beacon_installing_person)" ]; then
  BEACON_DASHBOARD=disabled
fi
# 1 turns the scheduled session sync on and 0 turns it off; empty keeps what the endpoint already
# has, so a package upgrade never changes it.
BEACON_SESSION_SYNC="${BEACON_SESSION_SYNC:-}"

if [ -z "$BEACON_COLLECTOR" ] && [ -x "/opt/beacon/bin/beacon-otelcol" ]; then
  BEACON_COLLECTOR="/opt/beacon/bin/beacon-otelcol"
fi

set -- endpoint install \
  --system \
  --harness "$BEACON_ENDPOINT_HARNESSES" \
  --otlp-grpc-port "$BEACON_OTLP_GRPC_PORT" \
  --otlp-http-port "$BEACON_OTLP_HTTP_PORT"

if [ -n "$BEACON_COLLECTOR" ]; then
  set -- "$@" --collector "$BEACON_COLLECTOR"
fi

if [ -n "$BEACON_LOG_ACCESS" ]; then
  set -- "$@" --log-access "$BEACON_LOG_ACCESS"
fi

if [ -n "$BEACON_LOG_READERS" ]; then
  set -- "$@" --log-readers "$BEACON_LOG_READERS"
fi
if [ -n "$BEACON_DASHBOARD" ]; then
  set -- "$@" --dashboard "$BEACON_DASHBOARD"
fi

case "$BEACON_SESSION_SYNC" in
  1|true|TRUE|yes|YES)
    set -- "$@" --session-sync
    ;;
  0|false|FALSE|no|NO)
    set -- "$@" --no-session-sync
    ;;
esac

if [ -n "$BEACON_SPLUNK_HEC_ENDPOINT" ]; then
  set -- "$@" --splunk-hec-endpoint "$BEACON_SPLUNK_HEC_ENDPOINT"
fi
if [ -n "$BEACON_SPLUNK_HEC_TOKEN" ]; then
  set -- "$@" --splunk-hec-token "$BEACON_SPLUNK_HEC_TOKEN"
fi
if [ -n "$BEACON_SPLUNK_INDEX" ]; then
  set -- "$@" --splunk-index "$BEACON_SPLUNK_INDEX"
fi
if [ -n "$BEACON_SPLUNK_SOURCE" ]; then
  set -- "$@" --splunk-source "$BEACON_SPLUNK_SOURCE"
fi
if [ -n "$BEACON_SPLUNK_SOURCETYPE" ]; then
  set -- "$@" --splunk-sourcetype "$BEACON_SPLUNK_SOURCETYPE"
fi
case "$BEACON_SPLUNK_INSECURE_SKIP_VERIFY" in
  1|true|TRUE|yes|YES)
    set -- "$@" --splunk-insecure-skip-verify
    ;;
esac
if [ -n "$BEACON_SPLUNK_CA_FILE" ]; then
  set -- "$@" --splunk-ca-file "$BEACON_SPLUNK_CA_FILE"
fi

if [ -n "$BEACON_FALCON_HEC_ENDPOINT" ]; then
  set -- "$@" --falcon-hec-endpoint "$BEACON_FALCON_HEC_ENDPOINT"
fi
if [ -n "$BEACON_FALCON_HEC_TOKEN" ]; then
  set -- "$@" --falcon-hec-token "$BEACON_FALCON_HEC_TOKEN"
fi
if [ -n "$BEACON_FALCON_INDEX" ]; then
  set -- "$@" --falcon-index "$BEACON_FALCON_INDEX"
fi
if [ -n "$BEACON_FALCON_SOURCE" ]; then
  set -- "$@" --falcon-source "$BEACON_FALCON_SOURCE"
fi
if [ -n "$BEACON_FALCON_SOURCETYPE" ]; then
  set -- "$@" --falcon-sourcetype "$BEACON_FALCON_SOURCETYPE"
fi
case "$BEACON_FALCON_INSECURE_SKIP_VERIFY" in
  1|true|TRUE|yes|YES)
    set -- "$@" --falcon-insecure-skip-verify
    ;;
esac
if [ -n "$BEACON_FALCON_CA_FILE" ]; then
  set -- "$@" --falcon-ca-file "$BEACON_FALCON_CA_FILE"
fi

case "$BEACON_NO_START" in
  1|true|TRUE|yes|YES)
    set -- "$@" --no-start
    ;;
esac

exec "$BEACON_BIN" "$@"

