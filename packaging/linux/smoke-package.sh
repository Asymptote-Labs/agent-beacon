#!/bin/sh
# Installs the built .deb (or .rpm) in a container where systemd is genuinely PID 1, and asserts
# that installing the package alone produces a running system-mode endpoint.
#
# This is the test for the actual goal: a Linux user should be able to install a package and be
# done. smoke-systemd.sh covers the service backend by calling `endpoint install` directly; this
# covers the package's own postinstall doing it for them, which is a different thing and the one
# users experience.
#
# Needs systemd as PID 1, which only a privileged container provides. Build the packages first:
#   cd cli/beacon && goreleaser release --snapshot --clean --skip=publish,homebrew,announce
set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
ARCH="${BEACON_SMOKE_ARCH:-}"
if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    arm64|aarch64) ARCH=arm64 ;;
    x86_64|amd64)  ARCH=amd64 ;;
    *) echo "unknown machine $(uname -m); set BEACON_SMOKE_ARCH" >&2; exit 1 ;;
  esac
fi
FORMAT="${BEACON_SMOKE_FORMAT:-deb}"
SMOKE_USER="${BEACON_SMOKE_USER:-beaconsmoke}"

PKG="${BEACON_SMOKE_PKG:-}"
if [ -z "$PKG" ]; then
  # Newest matching package, so a fresh snapshot build is picked up without being named.
  PKG="$(ls -t "$ROOT_DIR"/cli/beacon/dist/*_linux_"$ARCH"."$FORMAT" 2>/dev/null | head -1 || true)"
fi
if [ -z "$PKG" ] || [ ! -f "$PKG" ]; then
  echo "no $FORMAT package for linux_$ARCH in cli/beacon/dist" >&2
  echo "build one with: cd cli/beacon && goreleaser release --snapshot --clean --skip=publish,homebrew,announce" >&2
  exit 1
fi
echo "package: $(basename "$PKG")"

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

CONTAINER="${BEACON_SMOKE_CONTAINER:-beacon-package-smoke}"
cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

case "$FORMAT" in
  deb) IMAGE=beacon-package-smoke-deb
       BASE=ubuntu:24.04
       INSTALL_PKGS="systemd systemd-sysv dbus curl ca-certificates iproute2"
       PKGMGR_INSTALL="apt-get install -y -qq /var/tmp/beacon.deb" ;;
  rpm) IMAGE=beacon-package-smoke-rpm
       BASE=fedora:40
       INSTALL_PKGS="systemd dbus curl ca-certificates iproute"
       PKGMGR_INSTALL="dnf install -y -q /var/tmp/beacon.rpm" ;;
  *) echo "unsupported format $FORMAT" >&2; exit 1 ;;
esac

echo "building $IMAGE ($ARCH, $FORMAT)"
if [ "$FORMAT" = deb ]; then
  docker build -q -t "$IMAGE" - >/dev/null <<DOCKERFILE
FROM $BASE
RUN apt-get update -qq && apt-get install -y -qq $INSTALL_PKGS >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
DOCKERFILE
else
  docker build -q -t "$IMAGE" - >/dev/null <<DOCKERFILE
FROM $BASE
RUN dnf install -y -q $INSTALL_PKGS >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
DOCKERFILE
fi

echo "starting systemd container"
docker run -d --name "$CONTAINER" --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  "$IMAGE" >/dev/null

i=0
while [ "$i" -lt 30 ]; do
  state="$(docker exec "$CONTAINER" systemctl is-system-running 2>/dev/null || true)"
  case "$state" in running|degraded) break ;; esac
  i=$((i+1)); sleep 1
done
pid1="$(docker exec "$CONTAINER" cat /proc/1/comm 2>/dev/null || true)"
[ "$pid1" = systemd ] || { echo "PID 1 is $pid1, not systemd" >&2; exit 1; }
echo "ok: systemd is PID 1"

# The whole user-facing action: install the package. Nothing else.
#
# Two details that are load-bearing. The extension matters because both package managers dispatch
# on it. And the destination must be /var/tmp, not /tmp: Fedora's systemd enables tmp.mount, which
# mounts a tmpfs over /tmp once PID 1 starts -- so a file copied to /tmp before that is shadowed
# and dnf reports it as unopenable. Ubuntu does not do this, which is why only the rpm lane hit it.
docker cp "$PKG" "$CONTAINER":/var/tmp/beacon."$FORMAT" >/dev/null

# A human has to exist for the install to have anyone to configure. A system endpoint runs as root,
# so `endpoint install` configures root's agent settings -- and the postinstall's second step is what
# points the actual operator's Claude Code at the collector. SUDO_USER is how that operator is
# identified, and dpkg/rpm pass their environment through to maintainer scripts, so setting it here
# is the same thing `sudo apt install ./beacon.deb` does.
docker exec "$CONTAINER" useradd -m -s /bin/bash "$SMOKE_USER" >/dev/null 2>&1 || true
docker exec "$CONTAINER" test -d "/home/$SMOKE_USER" || {
  echo "could not create the test user $SMOKE_USER" >&2; exit 1; }

if ! docker exec -e SUDO_USER="$SMOKE_USER" "$CONTAINER" sh -c "$PKGMGR_INSTALL" >/tmp/beacon-pkg-install.log 2>&1; then
  echo "package install failed:" >&2
  tail -30 /tmp/beacon-pkg-install.log >&2
  exit 1
fi
echo "ok: package installed"

# Everything below must be true without any further user action.
unit_state="$(docker exec "$CONTAINER" systemctl is-active beacon-collector.service 2>/dev/null || true)"
[ "$unit_state" = active ] || {
  echo "collector unit is $unit_state, not active" >&2
  docker exec "$CONTAINER" systemctl status beacon-collector.service --no-pager >&2 || true
  docker exec "$CONTAINER" journalctl -u beacon-collector.service --no-pager -n 40 >&2 || true
  exit 1
}
echo "ok: collector service is active straight after install"

enabled="$(docker exec "$CONTAINER" systemctl is-enabled beacon-collector.service 2>/dev/null || true)"
[ "$enabled" = enabled ] || { echo "unit is $enabled, so it would not survive reboot" >&2; exit 1; }
echo "ok: unit is enabled, so it survives reboot"

for f in /etc/beacon/endpoint/config.json /etc/beacon/endpoint/otelcol.yaml; do
  docker exec "$CONTAINER" test -f "$f" || { echo "missing $f" >&2; exit 1; }
done
echo "ok: system config written to /etc/beacon/endpoint"

# The step that makes any of this useful. A running collector with nothing exporting to it captures
# an empty file, and that failure is silent -- status and doctor both look fine.
SETTINGS="/home/$SMOKE_USER/.claude/settings.json"
docker exec "$CONTAINER" test -f "$SETTINGS" || {
  echo "$SETTINGS was not written, so $SMOKE_USER's agent exports nowhere" >&2
  grep -i "user-config\|could not configure" /tmp/beacon-pkg-install.log >&2 || true
  exit 1
}
docker exec "$CONTAINER" grep -q "127.0.0.1:4317" "$SETTINGS" || {
  echo "$SETTINGS does not point at the local collector" >&2
  docker exec "$CONTAINER" cat "$SETTINGS" >&2 || true
  exit 1
}
owner="$(docker exec "$CONTAINER" stat -c %U "$SETTINGS" 2>/dev/null || true)"
[ "$owner" = "$SMOKE_USER" ] || {
  echo "$SETTINGS is owned by $owner, so $SMOKE_USER cannot read it" >&2; exit 1; }
echo "ok: the installing user's Claude Code points at the collector"

# The hook half of the same step, and the only free coverage of the root-side child-process launch.
# Installing hooks for another user runs the CLI as them -- via runuser when root on Linux, since a
# minimal Debian or RPM system may have no sudo at all. If that launch silently did nothing, the
# native config above would still be written and this would be the only signal.
docker exec "$CONTAINER" grep -q '"hooks"' "$SETTINGS" || {
  echo "$SETTINGS has no hooks block, so hook telemetry was not installed for $SMOKE_USER" >&2
  grep -i "hook" /tmp/beacon-pkg-install.log >&2 || true
  exit 1
}
ADAPTER="/home/$SMOKE_USER/.beacon/endpoint/hooks/beacon-hooks"
docker exec "$CONTAINER" test -x "$ADAPTER" || {
  echo "$ADAPTER is missing or not executable, so the configured hooks cannot run" >&2; exit 1; }
adapter_owner="$(docker exec "$CONTAINER" stat -c %U "$ADAPTER" 2>/dev/null || true)"
[ "$adapter_owner" = "$SMOKE_USER" ] || {
  echo "$ADAPTER is owned by $adapter_owner, not $SMOKE_USER" >&2; exit 1; }
echo "ok: hooks installed for the user and runnable by them"

# The two commands the docs tell a new user to run. Both must report healthy with no manual setup.
if ! docker exec "$CONTAINER" /opt/beacon/bin/beacon endpoint status --system --json >/tmp/beacon-status.json 2>&1; then
  echo "endpoint status failed:" >&2; cat /tmp/beacon-status.json >&2; exit 1
fi
python3 - <<'PY' || exit 1
import json, sys
s = json.load(open('/tmp/beacon-status.json'))
svc = s.get('service', {})
if svc.get('kind') != 'systemd':
    print(f"  service.kind = {svc.get('kind')!r}, want systemd", file=sys.stderr); sys.exit(1)
if not svc.get('running'):
    print(f"  service not running: {svc.get('message')}", file=sys.stderr); sys.exit(1)
print("ok: status reports a running systemd service")
PY

# Read as JSON, not by grepping the human-readable output. The first version of this check greped
# for "^fail" and "[fail]", neither of which doctor ever prints -- its text form says
# "<name>: fail ..." -- so a failing doctor passed CI. A check that cannot fail is worse than no
# check, because it reads as coverage.
docker exec "$CONTAINER" /opt/beacon/bin/beacon endpoint doctor --system --json \
  >/tmp/beacon-doctor.json 2>/tmp/beacon-doctor.err || true
python3 - <<'PY' || exit 1
import json, sys
try:
    with open('/tmp/beacon-doctor.json') as f:
        report = json.load(f)
except Exception as exc:
    print(f"  could not read doctor --json ({exc}), so its result is unknown", file=sys.stderr)
    print(open('/tmp/beacon-doctor.err').read(), file=sys.stderr)
    sys.exit(1)
checks = report.get('checks')
if not checks:
    print("  doctor reported no checks at all, so nothing was verified", file=sys.stderr)
    sys.exit(1)
failed = [c for c in checks if c.get('status') == 'fail']
if failed:
    for c in failed:
        print(f"  FAIL {c.get('name')} target={c.get('target')}: {c.get('message')}", file=sys.stderr)
    sys.exit(1)
print(f"ok: doctor reports no failures across {len(checks)} checks")
PY

# Reinstalling the same package is an upgrade as far as both package managers are concerned, and it
# is the exact sequence `beacon endpoint update --apply` performs. It is worth its own assertion
# because the old package's pre-removal script runs during an upgrade -- and on rpm it runs *after*
# the new postinstall, so a script that unconditionally tears the service down would leave an
# upgraded host with configuration and no running endpoint.
case "$FORMAT" in
  deb) docker exec "$CONTAINER" sh -c "dpkg --install /var/tmp/beacon.deb" >/tmp/beacon-upgrade.log 2>&1 ;;
  rpm) docker exec "$CONTAINER" sh -c "rpm --upgrade --replacepkgs /var/tmp/beacon.rpm" >/tmp/beacon-upgrade.log 2>&1 ;;
esac || { echo "upgrade failed:" >&2; tail -20 /tmp/beacon-upgrade.log >&2; exit 1; }
after_upgrade="$(docker exec "$CONTAINER" systemctl is-active beacon-collector.service 2>/dev/null || true)"
[ "$after_upgrade" = active ] || {
  echo "collector is $after_upgrade after an upgrade; the upgrade tore down its own service" >&2
  tail -20 /tmp/beacon-upgrade.log >&2
  docker exec "$CONTAINER" systemctl status beacon-collector.service --no-pager >&2 || true
  exit 1
}
docker exec "$CONTAINER" test -f /etc/beacon/endpoint/config.json || {
  echo "an upgrade must not remove the config" >&2; exit 1; }
echo "ok: upgrade left the service running and the config in place"

# Removal must stop the service while the binaries it points at still exist, and must not destroy
# collected telemetry.
case "$FORMAT" in
  deb) docker exec "$CONTAINER" apt-get remove -y -qq beacon >/dev/null 2>&1 ;;
  rpm) docker exec "$CONTAINER" dnf remove -y -q beacon >/dev/null 2>&1 ;;
esac
after="$(docker exec "$CONTAINER" systemctl is-active beacon-collector.service 2>/dev/null || true)"
[ "$after" != active ] || { echo "collector still active after package removal" >&2; exit 1; }
echo "ok: removal stopped the service"

if ! docker exec "$CONTAINER" test -f /etc/beacon/endpoint/config.json; then
  echo "removal deleted the config; only a purge should do that" >&2
  exit 1
fi
echo "ok: removal kept config and logs (purge removes them)"

echo
echo "package smoke test passed ($FORMAT/$ARCH)"
