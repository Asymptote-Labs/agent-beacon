#!/bin/sh
# Resolves the container image a Linux smoke test runs in, and prints its reference on stdout.
#
#   sh packaging/linux/smoke/image.sh NAME            # NAME is a Dockerfile here: systemd, package-deb, package-rpm
#   sh packaging/linux/smoke/image.sh --publish NAME  # build with a fresh base, boot-check, push to the registry
#   sh packaging/linux/smoke/image.sh --digest NAME   # print the tag NAME's Dockerfile is published under
#
# The tag is a digest of the Dockerfile, so an image is only ever reused for the recipe it was built
# from. With BEACON_SMOKE_REGISTRY set (CI sets ghcr.io/<owner>), the prebuilt image is pulled from
# there, which is what keeps the smoke jobs off the distribution mirrors: an apt or dnf outage used to
# fail every one of them at `docker build`. When the pull fails -- the image is not published yet,
# because the Dockerfile changed in the pull request being tested, or nothing is logged in -- the image
# is built here, as it is on a developer machine. BEACON_SMOKE_BASE replaces the base image and always
# builds locally, since the registry holds only the default bases.
#
# --publish moves the tag CI pulls only after the new image has booted systemd to a running state, so
# a broken base update stays out of every smoke job instead of failing them all until someone
# notices. Each publish also carries a dated tag, <digest>-YYYYMMDD, which is what a rollback points
# the plain tag back at:
#
#   docker buildx imagetools create -t ghcr.io/<owner>/beacon-smoke-NAME:<digest> \
#     ghcr.io/<owner>/beacon-smoke-NAME:<digest>-<date>
#
# The build output goes to stderr in full, so a mirror failure reads as one rather than as a bare
# `exit code: 100`.
set -eu

mode=resolve
case "${1:-}" in
  --publish) mode=publish; shift ;;
  --digest) mode=digest; shift ;;
esac
publish=0
[ "$mode" = publish ] && publish=1
name="${1:?usage: image.sh [--publish|--digest] NAME}"
dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
dockerfile="$dir/$name.Dockerfile"
[ -f "$dockerfile" ] || { echo "image.sh: no $dockerfile" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum "$dockerfile" | cut -c1-16)"
else
  digest="$(shasum -a 256 "$dockerfile" | cut -c1-16)"
fi
if [ "$mode" = digest ]; then
  echo "$digest"
  exit 0
fi
repo="beacon-smoke-$name"
registry="${BEACON_SMOKE_REGISTRY:-}"
base="${BEACON_SMOKE_BASE:-}"

# boot_check IMAGE: systemd comes up as PID 1 in IMAGE and reaches running (or degraded, which a
# container without every kernel facility always is), the state each smoke test waits for first.
boot_check() {
  check="beacon-smoke-check-$$"
  docker rm -f "$check" >/dev/null 2>&1 || true
  docker run -d --name "$check" --platform "$platform" --privileged --cgroupns=host \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock "$1" >/dev/null
  i=0 state=
  while [ "$i" -lt 60 ]; do
    state="$(docker exec "$check" systemctl is-system-running 2>/dev/null || true)"
    case "$state" in running|degraded) break ;; esac
    i=$((i+1)); sleep 1
  done
  pid1="$(docker exec "$check" cat /proc/1/comm 2>/dev/null || true)"
  docker rm -f "$check" >/dev/null 2>&1 || true
  case "$state" in running|degraded) ;; *)
    echo "image.sh: $1 did not boot (systemd state '$state', PID 1 '$pid1'); not publishing it" >&2
    return 1 ;;
  esac
  [ "$pid1" = systemd ] || { echo "image.sh: $1 has PID 1 '$pid1', not systemd; not publishing it" >&2; return 1; }
}

if [ "$publish" = 1 ]; then
  [ -n "$registry" ] || { echo "image.sh: --publish needs BEACON_SMOKE_REGISTRY" >&2; exit 1; }
  ref="$registry/$repo:$digest"
  dated="$ref-$(date -u +%Y%m%d)"
  # linux/amd64 is what the CI runners are, whatever machine publishes.
  platform="${BEACON_SMOKE_PLATFORM:-linux/amd64}"
  # --pull so the weekly rebuild picks up the base image's security updates under the same tag.
  # The source label links the GHCR package to the repository, which is what lets a workflow's job
  # token pull and prune it; GHCR reads it only from a single image manifest, so no provenance index
  # is pushed around it. Publishing needs BuildKit (buildx), which GitHub's runners have.
  source="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-asymptote-labs/agent-beacon}"
  docker build --pull --platform "$platform" --provenance=false \
    --label "org.opencontainers.image.source=$source" -t "$dated" -t "$ref" -f "$dockerfile" "$dir" >&2
  boot_check "$dated"
  # Dated tag first: if the second push fails, CI still pulls the previous image.
  docker push "$dated" >&2
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
