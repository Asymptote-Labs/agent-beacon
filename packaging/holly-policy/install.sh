#!/bin/bash
# Beacon policy hooks (secret exposure), POC installer for Claude Code on macOS.
#
#   pbpaste | ./install.sh --token-file /dev/stdin                     # key from the clipboard, never on disk
#   BEACON_POLICY_TOKEN=ask_live_... ./install.sh                       # every Claude Code session (user scope)
#   BEACON_POLICY_TOKEN=ask_live_... ./install.sh --scope project --project-dir <project>
#
# What it does:
#   1. Verifies ./beacon-policy against ./beacon-policy.sha256 and installs it at
#      ~/.beacon/endpoint/hooks/beacon-policy (beside Beacon's own hooks, which it
#      does not touch).
#   2. Writes ~/.beacon/endpoint/policy.json (0600) with the decide URL and token.
#      The token is kept out of settings.json on purpose: its env block reaches
#      the agent's Bash tool.
#   3. Adds six hooks (UserPromptSubmit, PreToolUse, SessionStart, Stop, SessionEnd,
#      UserPromptExpansion for /beacon-allow), the /beacon-allow command, Read deny
#      rules for credential files, and Read and Edit deny rules for Beacon's own
#      policy config to the chosen settings.json. Existing entries,
#      Beacon's included, are left as they are. Running it again changes nothing.
#   4. Runs a self-test: the prompt scanner and prefilter offline, then one
#      dry-run call to the decide endpoint to prove the token works.
#
# ./uninstall.sh removes exactly what this added.
set -euo pipefail

DEFAULT_URL="https://asymptote-edge-gulcylfs4a-uw.a.run.app/v1/mdr/decide"
HERE="$(cd "$(dirname "$0")" && pwd)"
SCOPE="user"
PROJECT_DIR=""
URL="${BEACON_POLICY_URL:-$DEFAULT_URL}"
BINARY="$HERE/beacon-policy"
SHAFILE="$HERE/beacon-policy.sha256"
DENY_RULES=1
TOKEN="${BEACON_POLICY_TOKEN:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --scope) SCOPE="$2"; shift 2 ;;
    --project-dir) PROJECT_DIR="$2"; shift 2 ;;
    --url) URL="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --sha256) SHAFILE="$2"; shift 2 ;;
    --token-file) TOKEN="$(tr -d '[:space:]' < "$2")"; shift 2 ;;
    --no-deny-rules) DENY_RULES=0; shift ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

say() { printf '  %s\n' "$*"; }
fail() { printf 'install failed: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Darwin" ] || fail "this POC build is for macOS"
[ "$(uname -m)" = "arm64" ] || fail "this POC build is for Apple Silicon (arm64)"
command -v python3 >/dev/null || fail "python3 is required (it ships with the Xcode command line tools)"
[ -n "$TOKEN" ] || fail "set BEACON_POLICY_TOKEN or pass --token-file"
case "$TOKEN" in ask_live_*) ;; *) fail "the token should start with ask_live_" ;; esac
case "$URL" in https://*|http://127.0.0.1*|http://localhost*) ;; *) fail "the decide URL must be https" ;; esac
[ -f "$BINARY" ] || fail "binary not found at $BINARY"

case "$SCOPE" in
  user) SETTINGS="$HOME/.claude/settings.json" ;;
  project)
    [ -n "$PROJECT_DIR" ] || fail "--scope project needs --project-dir"
    PROJECT_DIR="$(cd "$PROJECT_DIR" && pwd)"
    SETTINGS="$PROJECT_DIR/.claude/settings.json" ;;
  *) fail "--scope must be user or project" ;;
esac

echo "Beacon policy hooks: installing ($SCOPE scope)"

# 1. Binary.
if [ -f "$SHAFILE" ]; then
  expected="$(awk '{print $1}' "$SHAFILE")"
  actual="$(shasum -a 256 "$BINARY" | awk '{print $1}')"
  [ "$expected" = "$actual" ] || fail "checksum mismatch for $BINARY (expected $expected, got $actual)"
  say "checksum ok"
else
  say "no .sha256 next to the binary; skipping the checksum"
fi
BASE="$HOME/.beacon/endpoint"
DEST="$BASE/hooks/beacon-policy"
mkdir -p "$BASE/hooks" "$BASE/policy"
chmod 700 "$BASE/policy"
cp "$BINARY" "$DEST.tmp" && chmod 755 "$DEST.tmp" && mv -f "$DEST.tmp" "$DEST"
xattr -d com.apple.quarantine "$DEST" 2>/dev/null || true
say "binary: $DEST ($("$DEST" version 2>/dev/null | head -1))"

# 2. Config.
CONFIG="$BASE/policy.json"
umask 077
python3 - "$CONFIG" "$URL" "$TOKEN" <<'PY'
import json, os, sys
path, url, token = sys.argv[1:4]
tmp = path + ".tmp"
with open(tmp, "w") as fh:
    json.dump({"url": url, "token": token, "timeout_ms": 8000}, fh, indent=2)
    fh.write("\n")
os.chmod(tmp, 0o600)
os.replace(tmp, path)
PY
umask 022
say "config: $CONFIG (0600)"

# 3. Settings.
mkdir -p "$(dirname "$SETTINGS")"
python3 - "$SETTINGS" "$DEST" "$BASE/policy/install.json" "$DENY_RULES" "$HOME/.claude/settings.json" <<'PY'
import json, os, re, sys

settings_path, binary, manifest_path, deny_enabled, user_settings = sys.argv[1:6]
deny_enabled = deny_enabled == "1"

def load(path):
    try:
        with open(path) as fh:
            text = fh.read()
    except FileNotFoundError:
        return {}
    return json.loads(text) if text.strip() else {}

settings = load(settings_path)

# Log to the same runtime log Beacon's own Claude hooks use, so policy events
# ship with the rest of the endpoint's telemetry.
log_path, config_path = None, None
for source in (settings, load(user_settings)):
    for groups in (source.get("hooks") or {}).values():
        for group in groups:
            for hook in group.get("hooks", []):
                cmd = hook.get("command") or ""
                if "beacon-hooks" not in cmd:
                    continue
                m = re.search(r"--log\s+'([^']+)'", cmd) or re.search(r"--log\s+(\S+)", cmd)
                if m and not log_path:
                    log_path = m.group(1)
                m = re.search(r"--config\s+'([^']+)'", cmd) or re.search(r"--config\s+(\S+)", cmd)
                if m and not config_path:
                    config_path = m.group(1)
if not log_path:
    log_path = os.path.expanduser("~/.beacon/endpoint/logs/runtime.jsonl")

def command(sub):
    parts = [f"'{binary}'", "--platform claude", f"--log '{log_path}'"]
    if config_path:
        parts.append(f"--config '{config_path}'")
    parts.append(sub)
    return " ".join(parts)

ours = {
    "UserPromptSubmit": {"hooks": [{"type": "command", "command": command("policy-prompt"), "timeout": 5}]},
    "PreToolUse": {"matcher": "Bash|Read|Grep|Glob|WebFetch|mcp__.*",
                   "hooks": [{"type": "command", "command": command("policy-tool"), "timeout": 15}]},
    "SessionStart": {"hooks": [{"type": "command", "command": command("policy-session")}]},
    # Record the developer's answers to asked calls at the end of each turn and
    # of the session (prompt-submit and pre-tool also record them).
    "Stop": {"hooks": [{"type": "command", "command": command("policy-resolve")}]},
    "SessionEnd": {"hooks": [{"type": "command", "command": command("policy-resolve"), "timeout": 5}]},
    # /beacon-allow <why>: a one-time, reasoned override of a blocked prompt.
    "UserPromptExpansion": {"matcher": "beacon-allow",
                            "hooks": [{"type": "command", "command": command("policy-allow"), "timeout": 10}]},
}

try:
    manifest = json.load(open(manifest_path))
except (FileNotFoundError, ValueError):
    manifest = {}
entry = manifest.setdefault("settings", {}).setdefault(settings_path, {"deny_rules_added": []})
# Remember which containers this install created, so uninstall can remove them
# and leave a file that had them untouched. Only the first install records this.
if "created" not in entry:
    entry["created"] = {
        "hooks": "hooks" not in settings,
        "events": [e for e in ours if e not in (settings.get("hooks") or {})],
        "permissions": "permissions" not in settings,
        "deny": "deny" not in (settings.get("permissions") or {}),
    }

hooks = settings.setdefault("hooks", {})
for event in list(hooks):
    kept = []
    for group in hooks[event]:
        group = dict(group)
        group["hooks"] = [h for h in group.get("hooks", []) if "beacon-policy" not in (h.get("command") or "")]
        if group["hooks"]:
            kept.append(group)
    hooks[event] = kept
for event, group in ours.items():
    hooks.setdefault(event, []).append(group)

DENY = [
    "Read(~/.npmrc)",
    "Read(~/Library/Preferences/pnpm/config.yaml)",
    "Read(~/.aws/credentials)",
    "Read(~/.config/gh/hosts.yml)",
    "Read(~/.git-credentials)",
    "Read(~/.netrc)",
    # Beacon's own policy config: the decide token, the URL, the rule override
    # and the session state. The agent should neither read the token nor turn
    # the gate off by rewriting them with its file tools.
    "Read(~/.beacon/endpoint/policy.json)",
    "Read(~/.beacon/endpoint/policy/**)",
    "Edit(~/.beacon/endpoint/policy.json)",
    "Edit(~/.beacon/endpoint/policy/**)",
]
if deny_enabled:
    perms = settings.setdefault("permissions", {})
    deny = perms.setdefault("deny", [])
    for rule in DENY:
        if rule not in deny:
            deny.append(rule)
            if rule not in entry["deny_rules_added"]:
                entry["deny_rules_added"].append(rule)
manifest["log_path"] = log_path

# The /beacon-allow command. The hook blocks its expansion, so this text only
# reaches the model if the hook is not running.
commands_dir = os.path.join(os.path.dirname(settings_path), "commands")
os.makedirs(commands_dir, exist_ok=True)
command_file = os.path.join(commands_dir, "beacon-allow.md")
with open(command_file, "w") as fh:
    fh.write("""---
description: Send a prompt Beacon blocked for containing a secret, once, with your reason
argument-hint: <why this is OK to send>
---
The developer ran /beacon-allow, but Beacon's policy hook did not handle it, so no override was recorded. Tell the developer the override did not take effect and that Beacon's hooks may not be active in this folder.
""")
entry["command_file"] = command_file

# Write through a symlink (dotfile managers link settings.json into a repo):
# replacing the link itself would silently detach the file from the dotfiles.
write_path = os.path.realpath(settings_path)
mode = os.stat(write_path).st_mode & 0o777 if os.path.exists(write_path) else 0o644
tmp = write_path + ".beacon-policy.tmp"
with open(tmp, "w", encoding="utf-8") as fh:
    json.dump(settings, fh, indent=2, ensure_ascii=False)
    fh.write("\n")
os.chmod(tmp, mode)
os.replace(tmp, write_path)
with open(manifest_path, "w") as fh:
    json.dump(manifest, fh, indent=2)
os.chmod(manifest_path, 0o600)
print(f"  settings: {settings_path}")
print(f"  events log: {log_path}")
print(f"  deny rules: {', '.join(DENY) if deny_enabled else 'skipped'}")
PY

# 4. Self-test.
p45='{"id":"p45","tool":"Bash","input":{"command":"/opt/homebrew/bin/pnpm config get '"'"'//registry.tiptap.dev/:_authToken'"'"'"}}'
echo "$p45" | "$DEST" policy-scan | grep -q secret-source.npm-config || fail "self-test: the prefilter did not route the P-45 command"
fake="ghp_$(printf 'aZ3kQ9mX2pL7vR4tB8nC1wE6yU5sD0fGh2Jk')"
echo "{\"id\":\"t\",\"text\":\"use $fake\"}" | "$DEST" policy-scan | grep -q github_token || fail "self-test: the prompt scanner missed a token"
say "self-test: prefilter and prompt scanner ok"
status="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 -X POST "$URL" \
  -H 'Content-Type: application/json' -H @<(printf 'Authorization: Bearer %s\n' "$TOKEN") \
  -d '{"phase":"pre-tool","harness":"claude","session_id":"install-self-test","dry_run":true,"tool":{"name":"Bash","input":{"command":"echo self-test"}},"prefilter":{"rule_ids":["self-test"]}}' || echo 000)"
case "$status" in
  200) say "self-test: decide endpoint reachable, token accepted" ;;
  401|403) fail "self-test: the decide endpoint rejected the token (HTTP $status)" ;;
  *) say "warning: decide endpoint returned HTTP $status; tool calls will fail open until it answers" ;;
esac

echo "Done. New Claude Code sessions pick this up; running sessions reload settings automatically."
[ "$SCOPE" = "project" ] && echo "Open the project in Claude Code and accept the workspace trust prompt so its hooks run."
exit 0
