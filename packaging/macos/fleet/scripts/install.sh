#!/bin/sh
set -eu

# Fleet's install wrapper: positional arguments ($1 harnesses, $2/$3 OTLP gRPC/HTTP ports, $4
# collector, $5 no-start, $6-$12 Splunk HEC endpoint, token, index, source, sourcetype,
# insecure-skip-verify, CA file) become the BEACON_* variables install-endpoint.sh reads.
#
# Only values this wrapper was actually given are passed on. install-endpoint.sh applies the defaults
# itself, after the com.beacon.endpoint managed preferences an MDM profile sets; a default filled in
# here would shadow the profile's harnesses, ports and forwarder settings.
BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"
BEACON_INSTALL_SCRIPT="${BEACON_INSTALL_SCRIPT:-/opt/beacon/scripts/install-endpoint.sh}"
export BEACON_BIN

# beacon_fleet_arg NAME VALUE exports NAME=VALUE when VALUE is non-empty and the environment has not
# already set NAME.
beacon_fleet_arg() {
  [ -n "$2" ] || return 0
  eval "_beacon_current=\${$1:-}"
  [ -z "$_beacon_current" ] || return 0
  export "$1=$2"
}

beacon_fleet_arg BEACON_ENDPOINT_HARNESSES "${1:-}"
beacon_fleet_arg BEACON_OTLP_GRPC_PORT "${2:-}"
beacon_fleet_arg BEACON_OTLP_HTTP_PORT "${3:-}"
beacon_fleet_arg BEACON_COLLECTOR "${4:-}"
beacon_fleet_arg BEACON_NO_START "${5:-}"
beacon_fleet_arg BEACON_SPLUNK_HEC_ENDPOINT "${6:-}"
beacon_fleet_arg BEACON_SPLUNK_HEC_TOKEN "${7:-}"
beacon_fleet_arg BEACON_SPLUNK_INDEX "${8:-}"
beacon_fleet_arg BEACON_SPLUNK_SOURCE "${9:-}"
beacon_fleet_arg BEACON_SPLUNK_SOURCETYPE "${10:-}"
beacon_fleet_arg BEACON_SPLUNK_INSECURE_SKIP_VERIFY "${11:-}"
beacon_fleet_arg BEACON_SPLUNK_CA_FILE "${12:-}"

exec "$BEACON_INSTALL_SCRIPT"
