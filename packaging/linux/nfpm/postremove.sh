#!/bin/sh
# Runs after the package is removed.
#
# On a dpkg purge, remove the state that preremove deliberately kept. Anything short of a purge
# leaves logs and config in place.
#
# rpm has no purge. Its %postun gets the count of remaining versions ("0" on the final erase), and
# that is deliberately not matched: rpm convention is that erasing a package leaves the data it
# generated, and deleting collected telemetry on `dnf remove` would leave RPM users no way to remove
# the package and keep it. Full removal on RPM is `beacon endpoint uninstall --system` first.
set -eu

case "${1:-}" in
  purge)
    rm -rf /etc/beacon/endpoint
    rm -rf /var/log/beacon-agent
    ;;
esac
