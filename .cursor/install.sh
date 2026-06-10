#!/usr/bin/env bash
set -euo pipefail

echo "Installing Beacon Cursor Cloud hooks..."

BEACON_HOME="${BEACON_HOME:-/tmp/beacon}"
BEACON_BIN_DIR="$BEACON_HOME/bin"
BEACON_LOG_PATH="${BEACON_CLOUD_LOG_PATH:-$BEACON_HOME/runtime.jsonl}"
REPO_ROOT="${BEACON_CLOUD_REPO_DIR:-${CURSOR_PROJECT_DIR:-$(pwd)}}"

mkdir -p "$BEACON_BIN_DIR" "$BEACON_HOME/logs"

install_from_release() {
  local version="$1"
  local os="linux"
  local arch

  case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) echo "unsupported arch $(uname -m)" >&2; return 1 ;;
  esac

  local archive="beacon_${version#v}_${os}_${arch}.tar.gz"
  local base="https://github.com/asymptote-labs/agent-beacon/releases/download/${version}"

  curl -fsSL "${base}/${archive}" -o "$BEACON_HOME/${archive}"
  tar -xzf "$BEACON_HOME/${archive}" -C "$BEACON_BIN_DIR"
}

install_go() {
  if command -v go >/dev/null 2>&1; then
    return
  fi

  local go_version="${BEACON_GO_VERSION:-1.24.4}"
  local arch

  case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) echo "unsupported arch $(uname -m)" >&2; return 1 ;;
  esac

  local tarball="go${go_version}.linux-${arch}.tar.gz"
  mkdir -p "$BEACON_HOME/go-toolchain"
  curl -fsSL "https://go.dev/dl/${tarball}" -o "$BEACON_HOME/${tarball}"
  tar -xzf "$BEACON_HOME/${tarball}" -C "$BEACON_HOME/go-toolchain"
  export PATH="$BEACON_HOME/go-toolchain/go/bin:$PATH"
}

build_from_source() {
  install_go
  (cd "$REPO_ROOT/cli/beacon" && go build -o "$BEACON_BIN_DIR/beacon" .)
  (cd "$REPO_ROOT/cli/beacon-hooks" && go build -o "$BEACON_BIN_DIR/beacon-hooks" .)
}

if [ -n "${BEACON_VERSION:-}" ]; then
  install_from_release "$BEACON_VERSION"
else
  build_from_source
fi

chmod +x "$BEACON_BIN_DIR/beacon" "$BEACON_BIN_DIR/beacon-hooks"

mkdir -p "$REPO_ROOT/.cursor"
"$BEACON_BIN_DIR/beacon" cloud cursor install-hooks \
  --binary-path "$BEACON_BIN_DIR/beacon-hooks" \
  --log-path "$BEACON_LOG_PATH" \
  --hooks-json "$REPO_ROOT/.cursor/hooks.json"

echo ".cursor/hooks.json" >> "$REPO_ROOT/.git/info/exclude" 2>/dev/null || true
echo "Beacon Cursor Cloud hooks installed at $REPO_ROOT/.cursor/hooks.json"
