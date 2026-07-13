#!/bin/sh
set -u

# Fully remove Beacon endpoint services, forwarding configuration, logs/state,
# package files, package receipts, and Beacon-managed active-user hooks/config.
#
# This script is intentionally destructive. By default it cleans only the active
# console user's runtime configs. Set BEACON_CLEAN_ALL_USERS=1 to scan /Users/*.

BEACON_BIN="${BEACON_BIN:-/opt/beacon/bin/beacon}"
BEACON_KEEP_LOGS="${BEACON_KEEP_LOGS:-0}"
BEACON_CLEAN_ALL_USERS="${BEACON_CLEAN_ALL_USERS:-0}"

COLLECTOR_LABEL="${COLLECTOR_LABEL:-com.beacon.endpoint.collector}"
UPDATER_LABEL="${UPDATER_LABEL:-com.beacon.endpoint.updater}"
S3_FORWARDER_LABEL="${S3_FORWARDER_LABEL:-com.beacon.endpoint.s3-forwarder}"
GCS_FORWARDER_LABEL="${GCS_FORWARDER_LABEL:-com.beacon.endpoint.gcs-forwarder}"
FALCON_FORWARDER_LABEL="${FALCON_FORWARDER_LABEL:-com.beacon.endpoint.falcon-forwarder}"

RUNTIME_DIR="${RUNTIME_DIR:-/var/log/beacon-agent}"
SYSTEM_CONFIG_DIR="${SYSTEM_CONFIG_DIR:-/Library/Application Support/Beacon}"
BEACON_CONFIG_PATH="${BEACON_CONFIG_PATH:-$SYSTEM_CONFIG_DIR/Endpoint/config.json}"
OPT_BEACON_DIR="${OPT_BEACON_DIR:-/opt/beacon}"

warn() {
  echo "WARNING: $*" >&2
}

ok() {
  echo "OK: $*"
}

backup_file() {
  path="$1"
  [ -e "$path" ] || return 0
  if ! python3_available; then
    warn "python3 is unavailable; refusing to back up $path unsafely"
    return 1
  fi

  python3 - "$path" <<'PY'
import datetime
import os
import stat
import sys

path = sys.argv[1]
try:
    before = os.lstat(path)
    if not stat.S_ISREG(before.st_mode):
        raise ValueError("not a regular file")
    source_fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    current = os.fstat(source_fd)
    if (before.st_dev, before.st_ino) != (current.st_dev, current.st_ino):
        raise ValueError("file changed while opening")
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup = f"{path}.beacon-uninstall.{stamp}.{os.getpid()}.bak"
    backup_fd = os.open(
        backup,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
        stat.S_IMODE(current.st_mode),
    )
    try:
        os.fchmod(backup_fd, stat.S_IMODE(current.st_mode))
        os.fchown(backup_fd, current.st_uid, current.st_gid)
        while True:
            chunk = os.read(source_fd, 1024 * 1024)
            if not chunk:
                break
            remaining = memoryview(chunk)
            while remaining:
                written = os.write(backup_fd, remaining)
                remaining = remaining[written:]
        os.fsync(backup_fd)
    finally:
        os.close(backup_fd)
        os.close(source_fd)
except Exception as exc:
    print(f"WARNING: refusing unsafe backup of {path}: {exc}", file=sys.stderr)
    sys.exit(1)
PY
}

bootout_label() {
  label="$1"
  plist="/Library/LaunchDaemons/$label.plist"

  echo "Stopping launch daemon: $label"
  launchctl bootout "system/$label" >/dev/null 2>&1 || true
  launchctl remove "$label" >/dev/null 2>&1 || true

  if [ -f "$plist" ]; then
    rm -f "$plist" || warn "Could not remove $plist"
  fi
}

python3_available() {
  command -v python3 >/dev/null 2>&1
}

beacon_local_otlp_endpoints() {
  if ! python3_available || [ ! -f "$BEACON_CONFIG_PATH" ]; then
    return 0
  fi

  python3 - "$BEACON_CONFIG_PATH" <<'PY'
import json
import pathlib
import sys

try:
    config = json.loads(pathlib.Path(sys.argv[1]).read_text())
    collector = config.get("collector") or {}
    ports = {
        int(collector.get("grpc_port") or 0),
        int(collector.get("http_port") or 0),
    }
except Exception:
    sys.exit(0)

endpoints = []
for port in sorted(port for port in ports if port > 0):
    endpoints.extend([
        f"http://127.0.0.1:{port}",
        f"http://localhost:{port}",
    ])
print(",".join(endpoints))
PY
}

BEACON_LOCAL_OTLP_ENDPOINTS="$(beacon_local_otlp_endpoints)"
if [ -z "$BEACON_LOCAL_OTLP_ENDPOINTS" ]; then
  warn "Could not determine installed Beacon OTLP ports; preserving user OTLP settings"
fi

remove_beacon_hooks_from_json() {
  path="$1"
  [ -f "$path" ] || return 0

  if ! python3_available; then
    warn "python3 is unavailable; skipping Beacon hook cleanup in $path"
    return 0
  fi

  backup_file "$path" || return 0

  python3 - "$path" <<'PY'
import json
import os
import pathlib
import stat
import sys
import tempfile

path = pathlib.Path(sys.argv[1])

def read_verified_text(target):
    before = os.lstat(target)
    if not stat.S_ISREG(before.st_mode):
        raise ValueError("not a regular file")
    fd = os.open(target, os.O_RDONLY | os.O_NOFOLLOW)
    current = os.fstat(fd)
    if (before.st_dev, before.st_ino) != (current.st_dev, current.st_ino):
        os.close(fd)
        raise ValueError("file changed while opening")
    with os.fdopen(fd, encoding="utf-8") as handle:
        return handle.read(), current

def replace_verified_text(target, content, original):
    fd, temporary = tempfile.mkstemp(prefix=".beacon-cleanup-", dir=target.parent)
    try:
        os.fchmod(fd, stat.S_IMODE(original.st_mode))
        os.fchown(fd, original.st_uid, original.st_gid)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        current = os.lstat(target)
        if (current.st_dev, current.st_ino) != (original.st_dev, original.st_ino):
            raise ValueError("file changed before replacement")
        os.replace(temporary, target)
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise

try:
    text, original = read_verified_text(path)
    data = json.loads(text)
except Exception as exc:
    print(f"WARNING: could not parse {path}: {exc}", file=sys.stderr)
    sys.exit(0)

hooks = data.get("hooks")
if isinstance(hooks, dict):
    for event_name in list(hooks.keys()):
        groups = hooks.get(event_name)
        if not isinstance(groups, list):
            continue

        # Cursor stores hooks as direct refs under each event:
        #   {"hooks": {"preToolUse": [{"command": "..."}]}}
        # Claude-style settings store groups with a nested hooks array:
        #   {"hooks": {"PreToolUse": [{"hooks": [{"command": "..."}]}]}}
        if all(not (isinstance(group, dict) and isinstance(group.get("hooks"), list)) for group in groups):
            kept_refs = []
            for ref in groups:
                command = ""
                if isinstance(ref, dict):
                    command = str(ref.get("command") or "")
                if "BEACON_ENDPOINT_MODE=1" in command or "beacon-hooks" in command:
                    continue
                kept_refs.append(ref)
            if kept_refs:
                hooks[event_name] = kept_refs
            else:
                hooks.pop(event_name, None)
            continue

        kept_groups = []
        for group in groups:
            if not isinstance(group, dict):
                kept_groups.append(group)
                continue

            refs = group.get("hooks")
            if not isinstance(refs, list):
                kept_groups.append(group)
                continue

            kept_refs = []
            for ref in refs:
                command = ""
                if isinstance(ref, dict):
                    command = str(ref.get("command") or "")
                if "BEACON_ENDPOINT_MODE=1" in command or "beacon-hooks" in command:
                    continue
                kept_refs.append(ref)

            if kept_refs:
                next_group = dict(group)
                next_group["hooks"] = kept_refs
                kept_groups.append(next_group)

        if kept_groups:
            hooks[event_name] = kept_groups
        else:
            hooks.pop(event_name, None)

if hooks == {}:
    data.pop("hooks", None)

try:
    replace_verified_text(
        path,
        json.dumps(data, indent=2, sort_keys=True) + "\n",
        original,
    )
except Exception as exc:
    print(f"WARNING: could not safely update {path}: {exc}", file=sys.stderr)
PY
}

remove_claude_beacon_otel_env() {
  path="$1"
  [ -f "$path" ] || return 0

  if ! python3_available; then
    warn "python3 is unavailable; skipping Claude OTLP cleanup in $path"
    return 0
  fi

  backup_file "$path" || return 0

  python3 - "$path" "$BEACON_LOCAL_OTLP_ENDPOINTS" <<'PY'
import json
import os
import pathlib
import stat
import sys
import tempfile

path = pathlib.Path(sys.argv[1])

def read_verified_text(target):
    before = os.lstat(target)
    if not stat.S_ISREG(before.st_mode):
        raise ValueError("not a regular file")
    fd = os.open(target, os.O_RDONLY | os.O_NOFOLLOW)
    current = os.fstat(fd)
    if (before.st_dev, before.st_ino) != (current.st_dev, current.st_ino):
        os.close(fd)
        raise ValueError("file changed while opening")
    with os.fdopen(fd, encoding="utf-8") as handle:
        return handle.read(), current

def replace_verified_text(target, content, original):
    fd, temporary = tempfile.mkstemp(prefix=".beacon-cleanup-", dir=target.parent)
    try:
        os.fchmod(fd, stat.S_IMODE(original.st_mode))
        os.fchown(fd, original.st_uid, original.st_gid)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        current = os.lstat(target)
        if (current.st_dev, current.st_ino) != (original.st_dev, original.st_ino):
            raise ValueError("file changed before replacement")
        os.replace(temporary, target)
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise

try:
    text, original = read_verified_text(path)
    data = json.loads(text)
except Exception as exc:
    print(f"WARNING: could not parse {path}: {exc}", file=sys.stderr)
    sys.exit(0)

env = data.get("env")
if not isinstance(env, dict):
    sys.exit(0)

endpoint = str(env.get("OTEL_EXPORTER_OTLP_ENDPOINT") or "")
enabled = str(env.get("CLAUDE_CODE_ENABLE_TELEMETRY") or "")
beacon_endpoints = {
    value.rstrip("/")
    for value in sys.argv[2].split(",")
    if value
}
looks_beacon = (
    enabled == "1"
    and endpoint.rstrip("/") in beacon_endpoints
)
if not looks_beacon:
    sys.exit(0)

for key in [
    "CLAUDE_CODE_ENABLE_TELEMETRY",
    "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OTEL_EXPORTER_OTLP_PROTOCOL",
    "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE",
    "OTEL_LOGS_EXPORTER",
    "OTEL_METRICS_EXPORTER",
    "OTEL_LOG_USER_PROMPTS",
]:
    env.pop(key, None)

data["env"] = env
try:
    replace_verified_text(
        path,
        json.dumps(data, indent=2, sort_keys=True) + "\n",
        original,
    )
except Exception as exc:
    print(f"WARNING: could not safely update {path}: {exc}", file=sys.stderr)
PY
}

remove_codex_beacon_otel_blocks() {
  path="$1"
  [ -f "$path" ] || return 0

  if ! python3_available; then
    warn "python3 is unavailable; skipping Codex OTLP cleanup in $path"
    return 0
  fi

  backup_file "$path" || return 0

  python3 - "$path" "$BEACON_LOCAL_OTLP_ENDPOINTS" <<'PY'
import os
import pathlib
import re
import stat
import sys
import tempfile

path = pathlib.Path(sys.argv[1])
beacon_endpoints = {
    value.rstrip("/")
    for value in sys.argv[2].split(",")
    if value
}

def read_verified_text(target):
    before = os.lstat(target)
    if not stat.S_ISREG(before.st_mode):
        raise ValueError("not a regular file")
    fd = os.open(target, os.O_RDONLY | os.O_NOFOLLOW)
    current = os.fstat(fd)
    if (before.st_dev, before.st_ino) != (current.st_dev, current.st_ino):
        os.close(fd)
        raise ValueError("file changed while opening")
    with os.fdopen(fd, encoding="utf-8") as handle:
        return handle.read(), current

def replace_verified_text(target, content, original):
    fd, temporary = tempfile.mkstemp(prefix=".beacon-cleanup-", dir=target.parent)
    try:
        os.fchmod(fd, stat.S_IMODE(original.st_mode))
        os.fchown(fd, original.st_uid, original.st_gid)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        current = os.lstat(target)
        if (current.st_dev, current.st_ino) != (original.st_dev, original.st_ino):
            raise ValueError("file changed before replacement")
        os.replace(temporary, target)
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise

try:
    text, original = read_verified_text(path)
except Exception as exc:
    print(f"WARNING: could not safely read {path}: {exc}", file=sys.stderr)
    sys.exit(0)

lines = text.splitlines()
sections = []
current = []

for line in lines:
    stripped = line.strip()
    if stripped.startswith("[") and stripped.endswith("]") and current:
        sections.append(current)
        current = [line]
    else:
        current.append(line)
if current:
    sections.append(current)

out = []
for section in sections:
    header = section[0].strip() if section else ""
    is_otel = bool(re.match(r"^\[otel(\.|])", header))
    body = "\n".join(section)
    beacon_endpoint = any(endpoint in body for endpoint in beacon_endpoints)
    if is_otel and beacon_endpoint:
        continue
    out.extend(section)

try:
    replace_verified_text(path, "\n".join(out).rstrip() + "\n", original)
except Exception as exc:
    print(f"WARNING: could not safely update {path}: {exc}", file=sys.stderr)
PY
}

remove_user_beacon_artifacts() {
  user="$1"
  home="$2"

  echo "Cleaning Beacon user artifacts for $user ($home)"

  remove_beacon_hooks_from_json "$home/.claude/settings.json"
  remove_claude_beacon_otel_env "$home/.claude/settings.json"
  remove_beacon_hooks_from_json "$home/.codex/hooks.json"
  remove_codex_beacon_otel_blocks "$home/.codex/config.toml"
  remove_beacon_hooks_from_json "$home/.cursor/hooks.json"

  rm -rf "$home/.beacon/endpoint" ||
    warn "Could not remove $home/.beacon/endpoint"
}

cleanup_console_user() {
  console_user="$(stat -f '%Su' /dev/console 2>/dev/null || true)"
  if [ -z "$console_user" ] || [ "$console_user" = "root" ] || [ "$console_user" = "loginwindow" ]; then
    warn "No active console user; skipping active user cleanup"
    return 0
  fi

  home_dir="$(dscl . -read "/Users/$console_user" NFSHomeDirectory 2>/dev/null | awk '{print $2}')"
  if [ -z "$home_dir" ] || [ ! -d "$home_dir" ]; then
    warn "Could not resolve home directory for console user $console_user"
    return 0
  fi

  remove_user_beacon_artifacts "$console_user" "$home_dir"
}

cleanup_all_users() {
  for home_dir in /Users/*; do
    [ -d "$home_dir" ] || continue
    user="$(basename "$home_dir")"
    case "$user" in
      Shared|Guest|.localized)
        continue
        ;;
    esac
    if id "$user" >/dev/null 2>&1; then
      remove_user_beacon_artifacts "$user" "$home_dir"
    fi
  done
}

echo "=== Beacon Full Cleanup / Rollback ==="
date -u +"time_utc=%Y-%m-%dT%H:%M:%SZ"

echo
echo "Installed Beacon version before cleanup:"
if [ -x "$BEACON_BIN" ]; then
  "$BEACON_BIN" version || true
else
  warn "Beacon binary not found at $BEACON_BIN"
fi

echo
echo "Running Beacon endpoint uninstall if available..."
if [ -x "$BEACON_BIN" ]; then
  case "$BEACON_KEEP_LOGS" in
    1|true|TRUE|yes|YES)
      "$BEACON_BIN" endpoint uninstall --system --keep-logs ||
        warn "Beacon endpoint uninstall failed or endpoint was already removed"
      ;;
    *)
      "$BEACON_BIN" endpoint uninstall --system ||
        warn "Beacon endpoint uninstall failed or endpoint was already removed"
      ;;
  esac
fi

echo
echo "Stopping/removing leftover Beacon launch daemons..."
bootout_label "$COLLECTOR_LABEL"
bootout_label "$UPDATER_LABEL"
bootout_label "$S3_FORWARDER_LABEL"
bootout_label "$GCS_FORWARDER_LABEL"
bootout_label "$FALCON_FORWARDER_LABEL"

echo
echo "Cleaning user-level Beacon config..."
case "$BEACON_CLEAN_ALL_USERS" in
  1|true|TRUE|yes|YES)
    cleanup_all_users
    ;;
  *)
    cleanup_console_user
    ;;
esac

echo
echo "Removing system Beacon files..."
rm -rf "$SYSTEM_CONFIG_DIR" || warn "Could not remove $SYSTEM_CONFIG_DIR"
rm -rf "$OPT_BEACON_DIR" || warn "Could not remove $OPT_BEACON_DIR"
case "$BEACON_KEEP_LOGS" in
  1|true|TRUE|yes|YES)
    ok "Keeping Beacon logs because BEACON_KEEP_LOGS=$BEACON_KEEP_LOGS"
    ;;
  *)
    rm -rf "$RUNTIME_DIR" || warn "Could not remove $RUNTIME_DIR"
    ;;
esac

echo
echo "Removing Beacon package receipts..."
for pkgid in ai.asymptote.beacon.endpoint; do
  pkgutil --pkg-info "$pkgid" >/dev/null 2>&1 || continue
  echo "Forgetting package receipt: $pkgid"
  pkgutil --forget "$pkgid" >/dev/null 2>&1 ||
    warn "Could not forget receipt $pkgid"
done

echo
echo "Post-cleanup launchd state:"
for label in "$COLLECTOR_LABEL" "$UPDATER_LABEL" "$S3_FORWARDER_LABEL" "$GCS_FORWARDER_LABEL" "$FALCON_FORWARDER_LABEL"; do
  if launchctl print "system/$label" >/dev/null 2>&1; then
    warn "$label still exists"
  else
    ok "$label removed"
  fi
done

echo
echo "Beacon cleanup completed. A reboot is recommended if any launchd state appears stale."
exit 0
