#!/bin/bash
# Build the darwin/arm64 beacon-policy binary and stage the POC package:
#   packaging/holly-policy/dist/pkg/{beacon-policy,beacon-policy.sha256,install.sh,uninstall.sh}
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION="holly-policy-$(git rev-parse --short=8 HEAD)"
OUT=packaging/holly-policy/dist/pkg
rm -rf "$OUT" && mkdir -p "$OUT"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -C cli/beacon-hooks -trimpath \
  -ldflags "-s -w -X github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version.Version=$VERSION" \
  -o "../../$OUT/beacon-policy" .
(cd "$OUT" && shasum -a 256 beacon-policy > beacon-policy.sha256)
cp packaging/holly-policy/install.sh packaging/holly-policy/uninstall.sh "$OUT/"
chmod 755 "$OUT/beacon-policy" "$OUT/install.sh" "$OUT/uninstall.sh"
echo "$VERSION"
ls -l "$OUT"
