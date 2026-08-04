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

# A system endpoint runs as root, and `endpoint install` above configured root's own Claude Code
# and Codex settings. The person who ran the install is who actually needs pointing at the
# collector, so this second step resolves them (from SUDO_USER, else logind) and configures their
# runtime. Without it the collector runs perfectly and captures nothing anyone cares about.
#
# Non-fatal, and it prints why when it cannot: on an unattended install there may be no such user,
# which is a legitimate outcome rather than a failure.
if [ -x /opt/beacon/bin/beacon ]; then
  /opt/beacon/bin/beacon endpoint user-config repair-installed \
    --system --harness "${BEACON_ENDPOINT_HARNESSES:-claude,codex}" || \
    echo "beacon: could not configure the installing user's agent runtime; run" \
         "'beacon endpoint user-config repair-installed --system' once logged in" >&2
fi

# Reconcile the scheduled updater against the configured mode. Non-fatal: a missing or refused
# scheduler must not fail the package install, since the endpoint itself is already collecting.
if [ -x /opt/beacon/bin/beacon ]; then
  /opt/beacon/bin/beacon endpoint update install-daemon >/dev/null 2>&1 || \
    echo "beacon: scheduled updater not configured (endpoint is installed and running)" >&2
fi

echo "beacon: endpoint installed. Check it with: beacon endpoint status --system"
