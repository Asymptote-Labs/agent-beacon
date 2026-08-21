#!/bin/sh
# Runs after the package is removed.
#
# On a dpkg purge (or an rpm erase), remove the state that preremove deliberately kept. Anything
# short of a purge leaves logs and config in place.
set -eu

case "${1:-}" in
  purge)
    rm -rf /etc/beacon/endpoint
    rm -rf /var/log/beacon-agent
    ;;
esac
