#!/bin/bash
# Removes what install.sh added: the three beacon-policy hooks and the deny rules
# it added (rules that were already there are kept), the binary, its config and
# its local state. Beacon's own hooks are not touched.
set -euo pipefail
BASE="$HOME/.beacon/endpoint"
MANIFEST="$BASE/policy/install.json"

python3 - "$MANIFEST" "$HOME/.claude/settings.json" <<'PY'
import json, os, sys
manifest_path, user_settings = sys.argv[1:3]
try:
    manifest = json.load(open(manifest_path))
except (FileNotFoundError, ValueError):
    manifest = {"settings": {user_settings: {"deny_rules_added": []}}}
for path, entry in (manifest.get("settings") or {}).items():
    if not os.path.exists(path):
        continue
    with open(path) as fh:
        settings = json.load(fh)
    created = entry.get("created") or {}
    hooks = settings.get("hooks") or {}
    for event in list(hooks):
        kept = []
        for group in hooks[event]:
            group = dict(group)
            group["hooks"] = [h for h in group.get("hooks", []) if "beacon-policy" not in (h.get("command") or "")]
            if group["hooks"]:
                kept.append(group)
        hooks[event] = kept
        if not kept and event in created.get("events", []):
            del hooks[event]
    if "hooks" in settings and not settings["hooks"] and created.get("hooks"):
        del settings["hooks"]
    perms = settings.get("permissions") or {}
    if "deny" in perms:
        perms["deny"] = [r for r in perms["deny"] if r not in entry.get("deny_rules_added", [])]
        if not perms["deny"] and created.get("deny"):
            del perms["deny"]
    if "permissions" in settings and not settings["permissions"] and created.get("permissions"):
        del settings["permissions"]
    command_file = entry.get("command_file")
    if command_file and os.path.exists(command_file):
        os.remove(command_file)
        try:
            os.rmdir(os.path.dirname(command_file))
        except OSError:
            pass
    write_path = os.path.realpath(path)  # keep a symlinked settings.json a symlink
    mode = os.stat(write_path).st_mode & 0o777
    tmp = write_path + ".beacon-policy.tmp"
    with open(write_path, "rb") as fh:
        ends_with_newline = fh.read().endswith(b"\n")
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(settings, fh, indent=2, ensure_ascii=False)
        if ends_with_newline:
            fh.write("\n")
    os.chmod(tmp, mode)
    os.replace(tmp, write_path)
    print(f"  cleaned {path}")
PY

rm -f "$BASE/hooks/beacon-policy" "$BASE/policy.json"
rm -rf "$BASE/policy"
echo "Beacon policy hooks removed."
