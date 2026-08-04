#!/bin/sh
# Reproducible systemd smoke test for the Linux endpoint.
#
# systemd must be PID 1 to exercise the real install path, and no Modal sandbox or plain
# container gives you that -- Modal's own init holds PID 1 in both of its lanes. A privileged
# container with cgroups mounted does, so that is what this uses.
#
# Usage:
#   sh packaging/linux/smoke-systemd.sh              # auto-detects host arch
#   BEACON_SMOKE_ARCH=amd64 sh packaging/linux/smoke-systemd.sh
#
# Requires Docker and a Linux beacon binary plus a matching collector:
#   cd cli/beacon && make build-linux-$(arch)
set -eu

if [ "$(uname -s)" != "Darwin" ] && [ "$(uname -s)" != "Linux" ]; then
  echo "unsupported host: $(uname -s)" >&2
  exit 1
fi

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
ARCH="${BEACON_SMOKE_ARCH:-}"
if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    arm64|aarch64) ARCH=arm64 ;;
    x86_64|amd64)  ARCH=amd64 ;;
    *) echo "unknown machine $(uname -m); set BEACON_SMOKE_ARCH" >&2; exit 1 ;;
  esac
fi

BEACON_BIN="${BEACON_BIN:-$ROOT_DIR/cli/beacon/beacon-linux-$ARCH}"
COLLECTOR_BIN="${BEACON_COLLECTOR_BIN:-$ROOT_DIR/collector-builder/dist/beacon-otelcol/linux_$ARCH/beacon-otelcol}"

for f in "$BEACON_BIN" "$COLLECTOR_BIN"; do
  if [ ! -x "$f" ]; then
    echo "missing $f" >&2
    echo "build it with: cd cli/beacon && make build-linux-$ARCH" >&2
    exit 1
  fi
done

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

CONTAINER="${BEACON_SMOKE_CONTAINER:-beacon-systemd-smoke}"
IMAGE="${BEACON_SMOKE_IMAGE:-beacon-systemd-smoke}"
cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

echo "building $IMAGE ($ARCH)"
docker build -q -t "$IMAGE" - >/dev/null <<'DOCKERFILE'
FROM ubuntu:24.04
RUN apt-get update -qq && apt-get install -y -qq systemd systemd-sysv dbus curl ca-certificates iproute2 >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
DOCKERFILE

echo "starting systemd container"
docker run -d --name "$CONTAINER" --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  -v "$BEACON_BIN":/beacon/beacon:ro \
  -v "$COLLECTOR_BIN":/beacon/beacon-otelcol:ro \
  "$IMAGE" >/dev/null

i=0
while [ "$i" -lt 30 ]; do
  state="$(docker exec "$CONTAINER" systemctl is-system-running 2>/dev/null || true)"
  case "$state" in running|degraded) break ;; esac
  i=$((i+1)); sleep 1
done

fail() { echo "FAIL: $1" >&2; exit 1; }
in_container() { docker exec "$CONTAINER" sh -c "$1"; }

[ "$(in_container 'cat /proc/1/comm')" = "systemd" ] || fail "systemd is not PID 1"
echo "ok: systemd is PID 1"

in_container '/beacon/beacon endpoint install --system --harness claude --collector /beacon/beacon-otelcol' >/dev/null \
  || fail "endpoint install failed"
echo "ok: endpoint install --system succeeded"

in_container 'test -f /etc/beacon/endpoint/config.json' || fail "config not written to the FHS path"
in_container 'test -f /etc/systemd/system/beacon-collector.service' || fail "systemd unit not written"
echo "ok: config and unit written"

# These are the launchd semantics the unit has to preserve.
for want in 'Restart=always' 'WantedBy=multi-user.target' 'StandardOutput=journal'; do
  in_container "grep -q '$want' /etc/systemd/system/beacon-collector.service" \
    || fail "unit is missing $want"
done
echo "ok: unit preserves KeepAlive/RunAtLoad semantics"

[ "$(in_container 'systemctl is-enabled beacon-collector.service' || true)" = "enabled" ] \
  || fail "unit is not enabled"
[ "$(in_container 'systemctl is-active beacon-collector.service' || true)" = "active" ] \
  || fail "unit is not active"
echo "ok: unit enabled and active"

in_container 'curl -sf -o /dev/null --max-time 10 http://127.0.0.1:13133/' \
  || fail "collector health endpoint not reachable"
echo "ok: collector is listening and healthy"

in_container '/beacon/beacon endpoint status --system' | grep -q 'running=true' \
  || fail "status does not report the service running"
echo "ok: status reports the service running"

# Restart=always is the KeepAlive equivalent; verify it actually restarts.
before="$(in_container 'systemctl show -p MainPID --value beacon-collector.service')"
in_container "kill -9 $before" >/dev/null 2>&1 || true
sleep 8
after="$(in_container 'systemctl show -p MainPID --value beacon-collector.service')"
[ -n "$after" ] && [ "$after" != "0" ] && [ "$after" != "$before" ] \
  || fail "Restart=always did not restart the collector (before=$before after=$after)"
echo "ok: Restart=always restarted the collector ($before -> $after)"

in_container '/beacon/beacon endpoint doctor --system' >/dev/null 2>&1 \
  || echo "note: doctor reported warnings (expected: no agent session ran here)"

in_container '/beacon/beacon endpoint uninstall --system' >/dev/null || fail "uninstall failed"
in_container 'test ! -f /etc/systemd/system/beacon-collector.service' || fail "unit file survived uninstall"
in_container 'test ! -f /etc/beacon/endpoint/config.json' || fail "config survived uninstall"
[ "$(in_container 'systemctl is-enabled beacon-collector.service 2>&1' || true)" = "not-found" ] \
  || fail "unit still known to systemd after uninstall"
echo "ok: uninstall left nothing behind"

echo
echo "systemd endpoint smoke test passed"
