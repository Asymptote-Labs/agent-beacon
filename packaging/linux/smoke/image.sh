#!/bin/sh
# Resolves the container image a Linux smoke test runs in, and prints its reference on stdout.
#
#   sh packaging/linux/smoke/image.sh NAME            # NAME is a Dockerfile here: systemd, package-deb, package-rpm
#   sh packaging/linux/smoke/image.sh --publish NAME  # build with a fresh base and push to the registry
#
# The tag is a digest of the Dockerfile, so an image is only ever reused for the recipe it was built
# from. With BEACON_SMOKE_REGISTRY set (CI sets ghcr.io/<owner>), the prebuilt image is pulled from
# there, which is what keeps the smoke jobs off the distribution mirrors: an apt or dnf outage used to
# fail every one of them at `docker build`. When the pull fails -- the image is not published yet,
# because the Dockerfile changed in the pull request being tested, or nothing is logged in -- the image
# is built here, as it is on a developer machine. BEACON_SMOKE_BASE replaces the base image and always
# builds locally, since the registry holds only the default bases.
#
# The build output goes to stderr in full, so a mirror failure reads as one rather than as a bare
# `exit code: 100`.
set -eu

publish=0
if [ "${1:-}" = "--publish" ]; then
  publish=1
  shift
fi
name="${1:?usage: image.sh [--publish] NAME}"
dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
dockerfile="$dir/$name.Dockerfile"
[ -f "$dockerfile" ] || { echo "image.sh: no $dockerfile" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum "$dockerfile" | cut -c1-16)"
else
  digest="$(shasum -a 256 "$dockerfile" | cut -c1-16)"
fi
repo="beacon-smoke-$name"
registry="${BEACON_SMOKE_REGISTRY:-}"
base="${BEACON_SMOKE_BASE:-}"

if [ "$publish" = 1 ]; then
  [ -n "$registry" ] || { echo "image.sh: --publish needs BEACON_SMOKE_REGISTRY" >&2; exit 1; }
  ref="$registry/$repo:$digest"
  # --pull so the weekly rebuild picks up the base image's security updates under the same tag.
  docker build --pull -t "$ref" -f "$dockerfile" "$dir" >&2
  docker push "$ref" >&2
  echo "$ref"
  exit 0
fi

if [ -n "$registry" ] && [ -z "$base" ]; then
  ref="$registry/$repo:$digest"
  if docker pull -q "$ref" >/dev/null 2>&1; then
    echo "$ref"
    exit 0
  fi
  echo "image.sh: $ref is not available; building $name locally" >&2
fi

ref="$repo:$digest"
if [ -n "$base" ]; then
  ref="$repo:local"
  docker build --build-arg "BASE=$base" -t "$ref" -f "$dockerfile" "$dir" >&2
else
  docker build -t "$ref" -f "$dockerfile" "$dir" >&2
fi
echo "$ref"
