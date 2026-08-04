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

# An upgrade must not run any of this, and the reason is not obvious.
#
# Both package managers run the *old* package's pre-removal script during an upgrade, and rpm runs
# it AFTER the new package's postinstall. So on an rpm upgrade this script would execute right after
# the new version installed and started the collector, and tear down the service the upgrade had
# just brought up -- leaving an upgraded host with configuration and no running endpoint. dpkg runs
# prerm before unpacking, which is less destructive but still pointless work the new postinstall has
# to undo.
#
# This also reaches `beacon endpoint update --apply`, which installs with `dpkg -i` / `rpm -U` and so
# runs the same scripts. Without this guard a self-update would remove the endpoint it was updating.
#
# The argument says which case this is:
#   dpkg  prerm  -> "upgrade" or "failed-upgrade" when upgrading, "remove" when removing
#   rpm   %preun -> the count of remaining versions: "1" while upgrading, "0" on the final erase
#
# `beacon endpoint install` is idempotent and the new postinstall reconciles the unit, so skipping
# here is not merely safe -- it is the correct behavior.
case "${1:-}" in
  upgrade|failed-upgrade|1)
    echo "beacon: upgrade in progress, leaving the running endpoint in place"
    exit 0
    ;;
esac

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
