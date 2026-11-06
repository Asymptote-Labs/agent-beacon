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
if [ -z "$BEACON_LOG_ACCESS" ] && [ ! -e "$BEACON_SYSTEM_CONFIG" ] && [ ! -e "$BEACON_SYSTEM_LOG" ]; then
  init="$(cat "$BEACON_INIT_COMM" 2>/dev/null || true)"
  case "$BEACON_SERVICE" in
    ''|auto|systemd)
      if [ "$init" = systemd ]; then
        BEACON_LOG_ACCESS=restricted
      fi
      ;;
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
