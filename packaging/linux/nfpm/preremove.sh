#!/bin/sh
# Runs before the package is removed.
#
# Stops and deregisters the service while the binaries it references still exist -- doing this in
# postremove would leave a unit pointing at a deleted collector.
#
# Debian policy (and rpm convention) is that removing a package leaves configuration behind and
# only a purge deletes it. `endpoint uninstall` deliberately removes everything it installed,
# including /etc/beacon/endpoint/config.json, because that is what uninstall means at the CLI
# level. Note that --keep-config is about *harness* telemetry settings, not Beacon's own config,
# so it does not help here. So the config is set aside and restored, leaving purge as the only
# thing that discards it.
set -eu

CONFIG_DIR=/etc/beacon/endpoint
STASH="$(mktemp -d)"
saved=0
for f in config.json otelcol.yaml; do
  if [ -f "$CONFIG_DIR/$f" ]; then
    cp -p "$CONFIG_DIR/$f" "$STASH/$f" && saved=1
  fi
done

if [ -x /opt/beacon/scripts/uninstall-endpoint.sh ]; then
  BEACON_KEEP_LOGS=1 BEACON_KEEP_CONFIG=1 /opt/beacon/scripts/uninstall-endpoint.sh || \
    echo "beacon: endpoint uninstall reported a problem; check systemctl status" >&2
fi

if [ "$saved" = 1 ]; then
  mkdir -p "$CONFIG_DIR"
  for f in config.json otelcol.yaml; do
    [ -f "$STASH/$f" ] && cp -p "$STASH/$f" "$CONFIG_DIR/$f"
  done
fi
rm -rf "$STASH"
