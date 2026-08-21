#!/bin/sh
# Runs at the very end of an rpm transaction, after every install and removal script.
#
# rpm's ordering makes this necessary. During an upgrade it runs the *old* package's %preun after
# the new package's %post, so anything the new version started can be stopped again by the version
# being replaced. The preremove script guards against that for packages built with the guard, but an
# upgrade *from* an older package runs that older package's script, and nothing in the new package
# can change what it does.
#
# So the last word belongs here: reconcile the endpoint after the dust settles. `endpoint install` is
# idempotent, so on a transaction that did not disturb anything this is a no-op that costs one exec.
set -eu

if [ ! -x /opt/beacon/bin/beacon ]; then
  exit 0
fi

state="$(systemctl is-active beacon-collector.service 2>/dev/null || true)"
if [ "$state" = active ]; then
  exit 0
fi

echo "beacon: reconciling the endpoint after the transaction (service was $state)"
if ! /opt/beacon/scripts/install-endpoint.sh; then
  echo "beacon: endpoint reconcile failed; run 'sudo beacon endpoint doctor --system --fix'" >&2
fi
