#!/usr/bin/env bash
# Beacon hook shim for the Asymptote MDR demo, run inside a Claude Code on the
# web sandbox.
#
# Cloud-only by design: it no-ops immediately unless CLAUDE_CODE_REMOTE is set,
# so cloning this branch does not attach hooks to a local checkout. Every failure
# path prints "{}" and exits 0, because a hook that errors or hangs degrades the
# developer's session.
#
# Configuration comes from the cloud environment's variables (BEACON_MDR_URL,
# BEACON_MDR_TOKEN, BEACON_CLOUD_*), which hooks inherit. Nothing secret lives here.
set -u

[ "${CLAUDE_CODE_REMOTE:-}" = "true" ] || { echo '{}'; exit 0; }

ROOT="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo '{}'; exit 0 ;;
esac

BIN="$ROOT/demo/mdr/bin/beacon-hooks-linux-$ARCH"
[ -f "$BIN" ] || { echo '{}'; exit 0; }
# git preserves the exec bit, but a stray 100644 commit would otherwise fail
# silently and look exactly like MDR not being installed at all.
[ -x "$BIN" ] || chmod +x "$BIN" 2>/dev/null || { echo '{}'; exit 0; }

mkdir -p /tmp/beacon
export BEACON_ENDPOINT_MODE=1
export BEACON_ENDPOINT_LOG=/tmp/beacon/runtime.jsonl
export BEACON_ORIGIN=cloud
export BEACON_RUN_PROVIDER=claude_code_web

exec "$BIN" --platform claude "$@"
