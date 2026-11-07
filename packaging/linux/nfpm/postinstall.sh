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
# The harness list a configuration manager wrote to /etc/default/beacon or /etc/sysconfig/beacon,
# read here as well as inside install-endpoint.sh: the user-config step below is a separate command,
# and a harness list that reached only the system install left each person's own runtime
# unconfigured. Only that one value is taken, in a subshell: the user-config step runs the CLI as
# each logged-in user with this script's environment, so exporting every setting would put a Splunk
# HEC token from a root-only settings file into processes those users own. install-endpoint.sh
# reads the rest itself, as root.
if [ -r /opt/beacon/scripts/beacon-settings.sh ]; then
  # shellcheck source=../beacon-settings.sh
  . /opt/beacon/scripts/beacon-settings.sh
  BEACON_ENDPOINT_HARNESSES="$(beacon_settings_harnesses)"
fi

if ! /opt/beacon/scripts/install-endpoint.sh; then
  echo "beacon: endpoint install failed" >&2
  exit 1
fi

# A system endpoint runs as root, and `endpoint install` above configured root's own Claude Code
# and Codex settings. The people who use the machine are who actually need pointing at the
# collector, so this second step resolves them -- the installing sudo user, and everyone with a live
# logind session, which is what a configuration manager running as root (Salt, Ansible) has to go on
# -- configures each one's runtime as that user, and prints who it configured. Without it the
# collector runs perfectly and captures nothing anyone cares about.
#
# Non-fatal, and it prints why when it cannot: on an unattended install there may be no such user,
# which is a legitimate outcome rather than a failure.
if [ -x /opt/beacon/bin/beacon ]; then
  echo "beacon: the hook lines above are root's own; configuring the agent runtimes of logged-in users"
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
  # The scheduled inventory job is on by default; endpoint install already wrote and loaded it,
  # this re-reconcile is idempotent and covers an upgrade that skipped install.
  /opt/beacon/bin/beacon endpoint inventory install-daemon --system >/dev/null 2>&1 || \
    echo "beacon: scheduled inventory job not configured (endpoint is installed and running)" >&2
  # The scheduled session sync is opt-in (--session-sync); reconciled the same way, kept on upgrade.
  /opt/beacon/bin/beacon endpoint sessions install-daemon --system >/dev/null 2>&1 || \
    echo "beacon: scheduled session sync job not configured (endpoint is installed and running)" >&2
fi

# A restricted log is readable only by root and the beacon-log group. Install adds only the accounts
# in BEACON_LOG_READERS, or, on a first install a person ran with sudo, that person; anyone else on
# the machine can no longer read the system log with the local tools that read it (the dashboard,
# `beacon share`, `beacon traces`). That is the point of the mode on a shared machine; an
# administrator grants it per person.
if grep -q '"log_access": *"restricted"' /etc/beacon/endpoint/config.json 2>/dev/null; then
  echo "beacon: the system log is restricted to root and the beacon-log group. To let someone read it with" \
       "the local tools (dashboard, beacon share, beacon traces): sudo usermod -aG beacon-log <user>, then log in again"
fi

echo "beacon: endpoint installed. Check it with: beacon endpoint status --system"
