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
#   cd cli/beacon && goreleaser release --snapshot --clean --skip=publish,homebrew,announce,sign
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
  echo "build one with: cd cli/beacon && goreleaser release --snapshot --clean --skip=publish,homebrew,announce,sign" >&2
  exit 1
fi
echo "package: $(basename "$PKG")"

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

CONTAINER="${BEACON_SMOKE_CONTAINER:-beacon-package-smoke}"
cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

# Pulled prebuilt when BEACON_SMOKE_REGISTRY is set, built from smoke/package-$FORMAT.Dockerfile
# otherwise; BEACON_SMOKE_BASE replaces the base image.
#
# The package manager reads no repository while the test runs: the image already carries the
# package's one dependency (systemd). dnf refreshes metadata it considers stale (after 48 hours;
# the image is rebuilt weekly), so the rpm lane disables every repository, which also makes a
# missing dependency fail at once instead of being downloaded. apt-get never refreshes its lists on
# install and fetches nothing for a package whose dependencies are installed; its --no-download
# cannot take a local .deb at all.
case "$FORMAT" in
  deb) PKGMGR_INSTALL="apt-get install -y -qq /var/tmp/beacon.deb" ;;
  rpm) PKGMGR_INSTALL="dnf install -y -q --disablerepo='*' /var/tmp/beacon.rpm" ;;
  *) echo "unsupported format $FORMAT" >&2; exit 1 ;;
esac
IMAGE="$(sh "$ROOT_DIR/packaging/linux/smoke/image.sh" "package-$FORMAT")"
echo "image: $IMAGE ($ARCH, $FORMAT)"

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

# Humans have to exist for the install to have anyone to configure. A system endpoint runs as root,
# so `endpoint install` configures root's agent settings -- and the postinstall's second step is what
# points the people using the machine at the collector.
#
# The install below runs the way a configuration manager runs it: as root, with no SUDO_USER, so the
# only way to know who uses the machine is logind. Two people are logged in, because one machine
# often has several (a shared workstation, staff signing in to someone else's), and the install has
# to configure every one of them, not just the first. `su -l` opens a real logind session through
# pam_systemd. The SUDO_USER path is covered by the CLI's unit tests.
SMOKE_USER2="${SMOKE_USER}2"
for u in "$SMOKE_USER" "$SMOKE_USER2"; do
  docker exec "$CONTAINER" useradd -m -s /bin/bash "$u" >/dev/null 2>&1 || true
  docker exec "$CONTAINER" test -d "/home/$u" || {
    echo "could not create the test user $u" >&2; exit 1; }
  # -t: a login on a terminal, which logind classes "user". Without one it is a "background"
  # session (what cron opens), which Beacon rightly does not take for a person.
  docker exec -dt "$CONTAINER" su -l "$u" -c 'exec sleep infinity'
done
# And an account whose only session is not a person's login: no terminal, so logind classes it
# "background", as it does cron's. A display manager's greeter is the desktop case of the same thing.
docker exec "$CONTAINER" useradd -m -s /bin/bash beaconsvc >/dev/null 2>&1 || true
docker exec -d "$CONTAINER" su -l beaconsvc -c 'exec sleep infinity'
i=0
while [ "$i" -lt 20 ]; do
  sessions="$(docker exec "$CONTAINER" loginctl list-sessions --no-legend 2>/dev/null || true)"
  case "$sessions" in *"$SMOKE_USER2"*) case "$sessions" in *"$SMOKE_USER "*) break ;; esac ;; esac
  i=$((i+1)); sleep 1
done
case "$sessions" in
  *"$SMOKE_USER2"*) ;;
  *) echo "logind has no sessions for the test users, so there is nobody to configure:" >&2
     echo "$sessions" >&2; exit 1 ;;
esac
echo "ok: $SMOKE_USER and $SMOKE_USER2 are logged in (logind)"

# A settings file, as a configuration manager would drop one before installing: its harness list has
# to reach both the system install and the step that configures the user, and a trailing comment
# must not fail the install.
docker exec "$CONTAINER" sh -c 'printf "%s\n" "# managed by config management" "BEACON_ENDPOINT_HARNESSES=claude,codex,cursor # all three" >/etc/default/beacon && chmod 600 /etc/default/beacon'

if ! docker exec "$CONTAINER" env -u SUDO_USER sh -c "$PKGMGR_INSTALL" >/tmp/beacon-pkg-install.log 2>&1; then
  echo "package install failed:" >&2
  tail -30 /tmp/beacon-pkg-install.log >&2
  exit 1
fi
echo "ok: package installed"

# What the postinstall prints for the person installing. apt passes maintainer-script output
# through, but `dnf -q` does not show scriptlet output at all, so on rpm the installed %post is run
# again, as the package manager runs it, to read what it says. It is idempotent: every upgrade runs
# it the same way over an installed endpoint.
POSTINST_LOG=/tmp/beacon-pkg-install.log
if [ "$FORMAT" = rpm ]; then
  POSTINST_LOG=/tmp/beacon-postinstall.log
  if ! docker exec "$CONTAINER" env -u SUDO_USER sh -c 'rpm -q --qf "%{POSTIN}" beacon >/tmp/beacon-postin.sh && sh /tmp/beacon-postin.sh' >"$POSTINST_LOG" 2>&1; then
    echo "the installed %post script failed when run again:" >&2
    tail -30 "$POSTINST_LOG" >&2
    exit 1
  fi
fi

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

docker exec "$CONTAINER" test -f "/home/$SMOKE_USER/.cursor/hooks.json" || {
  echo "Cursor from /etc/default/beacon was not configured for $SMOKE_USER" >&2
  grep -i "hook\|ignoring" "$POSTINST_LOG" >&2 || true
  exit 1
}
grep -q "sudo usermod -aG beacon-log" "$POSTINST_LOG" || {
  echo "the install did not say how to let someone read the restricted log" >&2; exit 1; }
echo "ok: the settings file reached the user step (Cursor configured), and the beacon-log hint was printed"

# The second person logged in is configured too, as themselves, and the install says so for both
# rather than leaving the root context's own hook lines as the only output.
SETTINGS2="/home/$SMOKE_USER2/.claude/settings.json"
docker exec "$CONTAINER" grep -q '"hooks"' "$SETTINGS2" || {
  echo "$SETTINGS2 has no hooks, so only the first logged-in user was configured" >&2
  grep -i "configur" /tmp/beacon-pkg-install.log >&2 || true
  exit 1
}
owner2="$(docker exec "$CONTAINER" stat -c %U "$SETTINGS2" 2>/dev/null || true)"
[ "$owner2" = "$SMOKE_USER2" ] || { echo "$SETTINGS2 is owned by $owner2" >&2; exit 1; }
for u in "$SMOKE_USER" "$SMOKE_USER2"; do
  grep -q "^Configured $u (/home/$u): " "$POSTINST_LOG" || {
    echo "the install did not report configuring $u:" >&2
    tail -20 "$POSTINST_LOG" >&2
    exit 1
  }
done
if docker exec "$CONTAINER" test -e /home/beaconsvc/.claude/settings.json; then
  echo "beaconsvc, whose only session is a background one, was configured as if it were a person" >&2; exit 1
fi
echo "ok: every logged-in user was configured, and the install said so for each (and not a background session's account)"

# Reachable by name, not just by absolute path.
#
# Every check in this file used /opt/beacon/bin/beacon, so the package could ship with nothing on
# PATH and this smoke test would still pass -- which is exactly what happened. The documentation
# and this package's own postinstall both tell the user to run `beacon endpoint status --system`,
# and that failed with "command not found" on a correctly installed system.
#
# Checked as the unprivileged user and through sudo, because they resolve differently: sudo
# replaces PATH with secure_path, so a symlink somewhere only the user's PATH covers would pass
# one and fail the other, and most system-mode documentation is written with sudo.
docker exec "$CONTAINER" su - "$SMOKE_USER" -c 'command -v beacon' >/dev/null 2>&1 || {
  echo "beacon is not on PATH for $SMOKE_USER, so every documented command fails for them" >&2
  exit 1; }
docker exec "$CONTAINER" sh -c 'command -v beacon' >/dev/null 2>&1 || {
  echo "beacon is not on root's PATH, so the sudo form of every documented command fails" >&2
  exit 1; }
docker exec "$CONTAINER" su - "$SMOKE_USER" -c 'beacon --version' >/dev/null 2>&1 || {
  echo "beacon resolves on PATH but does not execute" >&2; exit 1; }
echo "ok: beacon is reachable by name, as the documentation says it is"

# Vector ships in the package for the forwarders Beacon renders, at the path FindVector looks.
# It must run, and be static like every other Linux binary Beacon ships, or a stripped host with a
# different libc gets a Vector that cannot start. The headers must be read and include a LOAD
# segment before the missing INTERP counts, so a file readelf cannot parse does not pass.
docker exec "$CONTAINER" test -x /opt/beacon/bin/vector || {
  echo "/opt/beacon/bin/vector is missing, so no forwarder can run" >&2; exit 1; }
vector_version="$(docker exec "$CONTAINER" /opt/beacon/bin/vector --version 2>&1)" || {
  echo "/opt/beacon/bin/vector does not run: $vector_version" >&2; exit 1; }
headers="$(docker exec "$CONTAINER" readelf -lW /opt/beacon/bin/vector 2>&1)" || {
  echo "could not read /opt/beacon/bin/vector's program headers: $headers" >&2; exit 1; }
case "$headers" in *LOAD*) ;; *) echo "no program headers in /opt/beacon/bin/vector" >&2; exit 1 ;; esac
case "$headers" in *INTERP*) echo "/opt/beacon/bin/vector is dynamically linked" >&2; exit 1 ;; esac
# /opt/beacon/bin is not on PATH, and the package must not put a vector there: it would shadow one
# the user already runs.
if docker exec "$CONTAINER" sh -c 'command -v vector' >/dev/null 2>&1; then
  echo "the package put a vector on PATH: $(docker exec "$CONTAINER" sh -c 'command -v vector')" >&2
  exit 1
fi
echo "ok: packaged Vector runs, is static, and stays off PATH ($vector_version)"

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

# A first package install lands in restricted log access with no second step: the collector is the
# only writer, the log is 0640 root:beacon-log, and hooks hand their events to the ingest socket.
# This is what a configuration manager (Salt pkg.installed, Ansible apt) gets, since it cannot pass
# BEACON_LOG_ACCESS through to the postinstall.
LOG=/var/log/beacon-agent/runtime.jsonl
mode="$(docker exec "$CONTAINER" cat /etc/beacon/endpoint/config.json | python3 -c 'import json, sys; print(json.load(sys.stdin).get("log_access", ""))')"
[ "$mode" = restricted ] || { echo "config.json log_access is '$mode' after a first install, want restricted" >&2; exit 1; }
perms="$(docker exec "$CONTAINER" stat -c '%a %U:%G' "$LOG")"
[ "$perms" = "640 root:beacon-log" ] || { echo "$LOG is $perms, want 640 root:beacon-log" >&2; exit 1; }
docker exec "$CONTAINER" test -S /var/run/beacon-agent/ingest.sock || {
  echo "the collector is not serving /var/run/beacon-agent/ingest.sock" >&2; exit 1; }
if docker exec "$CONTAINER" su - "$SMOKE_USER" -c "cat $LOG" >/dev/null 2>&1; then
  echo "$SMOKE_USER can read $LOG; restricted access did not apply" >&2; exit 1
fi
echo "ok: a first package install is restricted (640 root:beacon-log, ingest socket up)"

# The installed hook, run as the user exactly as Claude Code runs it, reaches the log through the
# socket and is recorded under the account the kernel says sent it.
HOOK_CMD="$(docker exec "$CONTAINER" cat "$SETTINGS" | python3 -c '
import json, sys
print(json.load(sys.stdin)["hooks"]["UserPromptSubmit"][0]["hooks"][0]["command"])')"
docker exec "$CONTAINER" su - "$SMOKE_USER" -c "printf '%s' '{\"session_id\":\"smoke-restricted\",\"hook_event_name\":\"UserPromptSubmit\",\"prompt\":\"smoke\",\"cwd\":\"/tmp\"}' | $HOOK_CMD" >/dev/null 2>&1 || {
  echo "the installed prompt hook failed for $SMOKE_USER" >&2; exit 1; }
recorded="$(docker exec "$CONTAINER" sh -c "grep smoke-restricted $LOG" | python3 -c '
import json, sys
users = {json.loads(line).get("user", {}).get("name", "") for line in sys.stdin if line.strip()}
print(",".join(sorted(users)))')"
[ "$recorded" = "$SMOKE_USER" ] || {
  echo "the hook event was recorded as '$recorded', want $SMOKE_USER" >&2; exit 1; }
echo "ok: $SMOKE_USER's hook event reached the restricted log through the socket, as $SMOKE_USER"

# OTLP is attributed to the account that sent it, not to the root collector. Every account exports to
# the same loopback collector, so before this every OTLP event on a system install read as root.
# Each export below also claims to be root through the peer attributes, on the resource and on the
# record, and both claims must be ignored.
otlp_from() {
  # $1 = account, $2 = session id; a browser-extension-shaped log over OTLP/HTTP JSON.
  body='{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"chatgpt_web"}},{"key":"beacon.peer.uid","value":{"stringValue":"0"}},{"key":"beacon.peer.user","value":{"stringValue":"root"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1762430000000000000","body":{"stringValue":"smoke"},"attributes":[{"key":"event.name","value":{"stringValue":"user_prompt"}},{"key":"session.id","value":{"stringValue":"'"$2"'"}},{"key":"beacon.peer.uid","value":{"stringValue":"0"}},{"key":"beacon.peer.user","value":{"stringValue":"root"}}]}]}]}]}'
  docker exec "$CONTAINER" su -s /bin/sh "$1" -c "curl -fsS -o /dev/null -H 'Content-Type: application/json' --data-binary '$body' http://127.0.0.1:4318/v1/logs" || {
    echo "OTLP export as $1 failed" >&2; exit 1; }
}
otlp_from "$SMOKE_USER" smoke-otlp-user
otlp_from root smoke-otlp-root
attributed=""
i=0
while [ "$i" -lt 20 ]; do
  attributed="$(docker exec "$CONTAINER" cat /var/log/beacon-agent/runtime.jsonl | python3 -c '
import json, sys
seen = {}
for line in sys.stdin:
    e = json.loads(line)
    sid = (e.get("session") or {}).get("id", "")
    if sid.startswith("smoke-otlp-"):
        seen[sid] = (e.get("user") or {}).get("name", "")
print(" ".join("%s=%s" % kv for kv in sorted(seen.items())))')"
  case "$attributed" in *smoke-otlp-root=*smoke-otlp-user=*|*smoke-otlp-user=*smoke-otlp-root=*) break ;; esac
  i=$((i+1)); sleep 1
done
[ "$attributed" = "smoke-otlp-root=root smoke-otlp-user=$SMOKE_USER" ] || {
  echo "OTLP events were attributed as '$attributed', want smoke-otlp-root=root smoke-otlp-user=$SMOKE_USER" >&2
  docker exec "$CONTAINER" journalctl -u beacon-collector.service --no-pager -n 30 >&2 || true
  exit 1
}
echo "ok: OTLP events are recorded under the account that sent them ($attributed)"

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
mode="$(docker exec "$CONTAINER" cat /etc/beacon/endpoint/config.json | python3 -c 'import json, sys; print(json.load(sys.stdin).get("log_access", ""))')"
[ "$mode" = restricted ] || { echo "an upgrade changed log_access to '$mode'" >&2; exit 1; }
echo "ok: upgrade left the service running and the config in place"

# Removal must stop the service while the binaries it points at still exist, and must not destroy
# collected telemetry.
case "$FORMAT" in
  deb) docker exec "$CONTAINER" apt-get remove -y -qq beacon >/dev/null 2>&1 ;;
  rpm) docker exec "$CONTAINER" dnf remove -y -q --disablerepo='*' beacon >/dev/null 2>&1 ;;
esac
after="$(docker exec "$CONTAINER" systemctl is-active beacon-collector.service 2>/dev/null || true)"
[ "$after" != active ] || { echo "collector still active after package removal" >&2; exit 1; }
echo "ok: removal stopped the service"

if ! docker exec "$CONTAINER" test -f /etc/beacon/endpoint/config.json; then
  echo "removal deleted the config; only a purge should do that" >&2
  exit 1
fi
if docker exec "$CONTAINER" test -e /opt/beacon/bin/vector; then
  echo "removal left /opt/beacon/bin/vector behind" >&2; exit 1
fi
echo "ok: removal took the binaries and kept config and logs"

# rpm has no purge, so this half is deb only. It is what makes "only a purge deletes it" true.
if [ "$FORMAT" = deb ]; then
  docker exec "$CONTAINER" apt-get purge -y -qq beacon >/dev/null 2>&1 || {
    echo "apt-get purge failed" >&2; exit 1; }
  for d in /etc/beacon/endpoint /var/log/beacon-agent; do
    if docker exec "$CONTAINER" test -e "$d"; then
      echo "purge left $d behind" >&2; exit 1
    fi
  done
  echo "ok: purge removed config and logs"
fi

echo
echo "package smoke test passed ($FORMAT/$ARCH)"
