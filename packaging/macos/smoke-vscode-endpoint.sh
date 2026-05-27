#!/bin/sh
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMPDIR="${TMPDIR:-/tmp}/beacon-vscode-smoke.$$"
LOG="$TMPDIR/runtime.jsonl"
WORKSPACE="$TMPDIR/workspace"
DASHBOARD_ADDR="127.0.0.1:18765"
DASHBOARD_PID=""

cleanup() {
  if [ -n "$DASHBOARD_PID" ]; then
    kill "$DASHBOARD_PID" 2>/dev/null || true
    wait "$DASHBOARD_PID" 2>/dev/null || true
  fi
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

mkdir -p "$WORKSPACE"

cd "$ROOT/cli/beacon"
go build -o "$TMPDIR/beacon" .

cd "$ROOT/cli/beacon-hooks"
go build -o "$TMPDIR/beacon-hooks" .

"$TMPDIR/beacon" endpoint integrations vscode print-config --log-path "$LOG" >/dev/null

cat <<JSON | BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG="$LOG" BEACON_CONTENT_RETENTION=metadata "$TMPDIR/beacon-hooks" --platform vscode prompt-submit >/dev/null
{"sessionId":"vscode-smoke","hookEventName":"UserPromptSubmit","cwd":"$WORKSPACE","prompt":"summarize this repository"}
JSON

cat <<JSON | BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG="$LOG" BEACON_CONTENT_RETENTION=metadata "$TMPDIR/beacon-hooks" --platform vscode pre-tool >/dev/null
{"sessionId":"vscode-smoke","hookEventName":"PreToolUse","cwd":"$WORKSPACE","tool_name":"runCommand","tool_input":{"command":"go test ./..."}}
JSON

cat <<JSON | BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG="$LOG" BEACON_CONTENT_RETENTION=metadata "$TMPDIR/beacon-hooks" --platform vscode post-tool >/dev/null
{"sessionId":"vscode-smoke","hookEventName":"PostToolUse","cwd":"$WORKSPACE","tool_name":"runCommand","tool_input":{"command":"go test ./..."},"tool_response":"ok"}
JSON

"$TMPDIR/beacon" endpoint integrations vscode validate --log-path "$LOG" >/dev/null

"$TMPDIR/beacon" endpoint dashboard --log-path "$LOG" --addr "$DASHBOARD_ADDR" >"$TMPDIR/dashboard.log" 2>&1 &
DASHBOARD_PID="$!"

SUMMARY=""
i=0
while [ "$i" -lt 20 ]; do
  SUMMARY="$(curl -fsS "http://$DASHBOARD_ADDR/api/summary" 2>/dev/null || true)"
  if [ -n "$SUMMARY" ]; then
    break
  fi
  i=$((i + 1))
  sleep 0.25
done

if [ -z "$SUMMARY" ]; then
  echo "dashboard did not start"
  cat "$TMPDIR/dashboard.log" 2>/dev/null || true
  exit 1
fi

echo "$SUMMARY" | grep -q "vscode" || {
  echo "dashboard summary did not include vscode"
  exit 1
}

if grep -E '"event":\{"kind":"agent_runtime","action":"(chat|metric\.observed)"' "$LOG" >/dev/null; then
  echo "runtime log contains noisy chat or metric events"
  exit 1
fi

echo "VS Code endpoint smoke events written to $LOG"
