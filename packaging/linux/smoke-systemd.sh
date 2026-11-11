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
cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

# Pulled prebuilt when BEACON_SMOKE_REGISTRY is set, built from smoke/systemd.Dockerfile otherwise.
IMAGE="$(sh "$ROOT_DIR/packaging/linux/smoke/image.sh" systemd)"
echo "image: $IMAGE ($ARCH)"

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

# The scheduled session sync: opt-in, kept by repair, a timer that does not fire at load (the
# install backfill owns that moment), a unit that runs as root, and a confined sweep of the console
# user's session store that writes each record once.
in_container '/beacon/beacon endpoint repair --system --harness claude --collector /beacon/beacon-otelcol --session-sync' \
  | grep -q 'Scheduled session sync job: /etc/systemd/system/beacon-session-sync.timer' \
  || fail "repair --session-sync did not install the session sync job"
[ "$(in_container 'systemctl is-enabled beacon-session-sync.timer' || true)" = "enabled" ] \
  && [ "$(in_container 'systemctl is-active beacon-session-sync.timer' || true)" = "active" ] \
  || fail "session sync timer is not enabled and active"
in_container "grep -q '^OnActiveSec=900s' /etc/systemd/system/beacon-session-sync.timer && ! grep -q OnBootSec /etc/systemd/system/beacon-session-sync.timer" \
  || fail "session sync timer would fire at load"
[ -z "$(in_container 'systemctl show -p ExecMainStartTimestamp --value beacon-session-sync.service')" ] \
  || fail "session sync ran at load"
in_container "grep -q '\"--scheduled\" \"--system\"' /etc/systemd/system/beacon-session-sync.service && grep -q '^User=root' /etc/systemd/system/beacon-session-sync.service" \
  || fail "session sync service does not run the scheduled system sweep as root"
in_container '/beacon/beacon endpoint status --system' | grep -q 'Session sync: every 15m0s; job loaded=true' \
  || fail "status does not report the session sync job"
in_container 'systemctl start beacon-session-sync.service' || fail "session sync service failed"
[ "$(in_container 'systemctl show -p Result --value beacon-session-sync.service')" = "success" ] \
  || fail "session sync service did not succeed"
echo "ok: session sync timer installed, not fired at load, and its unit runs"

ts="$(date -u +%Y-%m-%dT%H:%M:%S.000Z)"
in_container "useradd -m alice && mkdir -p /home/alice/.claude/projects/-tmp-repo && printf '%s\n' '{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello from alice\"},\"uuid\":\"u1\",\"timestamp\":\"$ts\",\"cwd\":\"/tmp/repo\",\"sessionId\":\"sess-alice\",\"version\":\"2.1.154\"}' > /home/alice/.claude/projects/-tmp-repo/sess-alice.jsonl && chown -R alice:alice /home/alice/.claude" \
  || fail "could not create the console user's session"
in_container 'SUDO_USER=alice /beacon/beacon endpoint sessions sync --system' | grep -q 'Session sync: [1-9][0-9]* events' \
  || fail "system session sync did not read the console user's session"
first="$(in_container 'grep -c sess-alice /var/log/beacon-agent/runtime.jsonl' || true)"
in_container 'SUDO_USER=alice /beacon/beacon endpoint sessions sync --system' >/dev/null || fail "second session sync failed"
second="$(in_container 'grep -c sess-alice /var/log/beacon-agent/runtime.jsonl' || true)"
[ "$first" -gt 0 ] && [ "$first" = "$second" ] \
  || fail "session sync wrote the session $first then $second times; cursors should make the second run read nothing"
echo "ok: session sync read the console user's session once ($first events)"

in_container '/beacon/beacon endpoint doctor --system' >/dev/null 2>&1 \
  || echo "note: doctor reported warnings (expected: no agent session ran here)"

# The exact query beacon-sandbox uses to decide a unit is gone, checked here because this job has a
# real systemd and the sandbox's Linux scenarios currently cannot reach their uninstall probe.
#
# Asserted in both directions. `list-unit-files` printing nothing is how the sandbox reads "removed",
# and a query that prints nothing for a unit that is still installed would make every removal look
# successful — so the pre-uninstall row is what proves the empty result afterwards means something.
listed_before="$(in_container 'systemctl list-unit-files beacon-collector.service --no-legend 2>&1' || true)"
[ -n "$(printf '%s' "$listed_before" | tr -d '[:space:]')" ] \
  || fail "list-unit-files printed nothing for an installed unit, so its silence after uninstall would prove nothing"
echo "ok: list-unit-files reports the installed unit ($listed_before)"

in_container '/beacon/beacon endpoint uninstall --system' >/dev/null || fail "uninstall failed"
in_container 'test ! -f /etc/systemd/system/beacon-collector.service' || fail "unit file survived uninstall"
in_container 'test ! -f /etc/beacon/endpoint/config.json' || fail "config survived uninstall"
[ "$(in_container 'systemctl is-enabled beacon-collector.service 2>&1' || true)" = "not-found" ] \
  || fail "unit still known to systemd after uninstall"
in_container 'test ! -e /etc/systemd/system/beacon-session-sync.timer && test ! -e /etc/systemd/system/beacon-session-sync.service' \
  || fail "session sync units survived uninstall"

listed_after="$(in_container 'systemctl list-unit-files beacon-collector.service --no-legend 2>&1' || true)"
[ -z "$(printf '%s' "$listed_after" | tr -d '[:space:]')" ] \
  || fail "list-unit-files still reports the unit after uninstall ($listed_after): beacon-sandbox reads a non-empty result as a unit that was never removed"
echo "ok: list-unit-files is silent after uninstall, which is what beacon-sandbox reads as removed"

echo "ok: uninstall left nothing behind"

echo
echo "systemd endpoint smoke test passed"
