#!/bin/sh
# Runs after the .deb/.rpm unpacks /opt/beacon.
#
# Installing the package is the whole user-facing action, so this performs the endpoint install
# rather than leaving a second manual step. It is the Linux counterpart of the macOS pkg's
# postinstall.
set -eu

# Both package managers invoke this for upgrades as well as first installs. `endpoint install` is
# idempotent and rewrites its own config, so no branch on the argument is needed -- but an upgrade
# must not lose an operator's explicit auto-update choice, which reconcile below preserves.
if ! /opt/beacon/scripts/install-endpoint.sh; then
  echo "beacon: endpoint install failed" >&2
  exit 1
fi

# Reconcile the scheduled updater against the configured mode. Non-fatal: a missing or refused
# scheduler must not fail the package install, since the endpoint itself is already collecting.
if [ -x /opt/beacon/bin/beacon ]; then
  /opt/beacon/bin/beacon endpoint update install-daemon >/dev/null 2>&1 || \
    echo "beacon: scheduled updater not configured (endpoint is installed and running)" >&2
fi

echo "beacon: endpoint installed. Check it with: beacon endpoint status --system"
