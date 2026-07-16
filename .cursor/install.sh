#!/usr/bin/env bash
set -euo pipefail

echo "Installing Beacon Cursor Cloud binaries..."

BEACON_HOME="${BEACON_HOME:-/tmp/beacon}"
BEACON_BIN_DIR="$BEACON_HOME/bin"
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

go_is_compatible() {
  local go_binary="$1"
  local current_version
  current_version="$("$go_binary" version 2>/dev/null | awk '{print $3}')"
  [[ "$current_version" =~ ^go1\.([0-9]+) ]] && [ "${BASH_REMATCH[1]}" -ge 24 ]
}

install_go() {
  local bundled_go="$BEACON_HOME/go-toolchain/go/bin/go"
  if [ -x "$bundled_go" ] && go_is_compatible "$bundled_go"; then
    export PATH="$(dirname "$bundled_go"):$PATH"
    return
  fi

  if command -v go >/dev/null 2>&1; then
    local system_go
    system_go="$(command -v go)"
    if go_is_compatible "$system_go"; then
      return
    fi
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
  export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
  (cd "$REPO_ROOT/cli/beacon-hooks" && go build -o "$BEACON_BIN_DIR/beacon-hooks" .)
  cp "$BEACON_BIN_DIR/beacon-hooks" "$REPO_ROOT/cli/beacon/internal/embedded/hooks.bin"
  (cd "$REPO_ROOT/cli/beacon" && go build -o "$BEACON_BIN_DIR/beacon" .)
}

if [ -n "${BEACON_VERSION:-}" ]; then
  install_from_release "$BEACON_VERSION"
else
  build_from_source
fi

chmod +x "$BEACON_BIN_DIR/beacon" "$BEACON_BIN_DIR/beacon-hooks"

if [ ! -d "$REPO_ROOT" ] || [ ! -d "$REPO_ROOT/.git" ]; then
  echo "Beacon Cursor Cloud setup could not find a git repository at REPO_ROOT=$REPO_ROOT" >&2
  echo "Set BEACON_CLOUD_REPO_DIR or CURSOR_PROJECT_DIR to the cloned repository root." >&2
  exit 1
fi

if [ ! -f "$REPO_ROOT/.cursor/hooks.json" ]; then
  echo "Beacon Cursor Cloud project hooks were not found at $REPO_ROOT/.cursor/hooks.json" >&2
  echo "Commit .cursor/hooks.json before starting Cursor Cloud Agents." >&2
  echo "Generate it with: beacon cloud cursor print-hooks --binary-path $BEACON_BIN_DIR/beacon-hooks --log-path $BEACON_HOME/runtime.jsonl > .cursor/hooks.json" >&2
  exit 1
fi

echo "Beacon Cursor Cloud binaries installed in $BEACON_BIN_DIR"
