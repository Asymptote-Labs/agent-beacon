#!/usr/bin/env bash
# Rebuild the committed Linux beacon-hooks binary for the MDR demo.
#
# The cloud sandbox never compiles anything: it clones this branch and runs the
# committed artifact. So editing Go source without rerunning this leaves the
# branch's source correct while the sandbox executes the old behavior, silently.
# Always go through this script.
#
#   demo/mdr/build.sh && git commit --amend --no-edit && git push -f
set -euo pipefail

cd "$(dirname "$0")/../.."

# Both architectures, because the sandbox's CPU is not contractual. If only one
# is committed and the sandbox is the other, the shim finds no binary, prints
# "{}", and the demo silently does nothing — indistinguishable from MDR not being
# configured. ~9 MB each is a cheap price for removing that failure mode.
#
# CGO_ENABLED=0 guarantees a static binary. With cgo, Go's net and os/user
# packages can link the host libc, producing something tied to this machine's
# glibc rather than runnable on the sandbox image.
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -C cli/beacon-hooks \
    -ldflags "-s -w" \
    -o "../../demo/mdr/bin/beacon-hooks-linux-$arch" .
done

chmod +x demo/mdr/bin/beacon-hooks-linux-* demo/mdr/hook.sh demo/mdr/build.sh
git add demo/mdr cli .claude .mcp.json

echo
echo "Binaries:"
ls -lh demo/mdr/bin/beacon-hooks-linux-* | awk '{print "  " $5, $9}'

echo
echo "Staged modes (both MUST be 100755):"
git ls-files -s demo/mdr | sed 's/^/  /'

if git ls-files -s demo/mdr | grep -qv '^100755'; then
  echo
  echo "ERROR: a file under demo/mdr is not staged executable." >&2
  echo "The hook shim's -x guard would fail and the hook would no-op silently." >&2
  exit 1
fi
