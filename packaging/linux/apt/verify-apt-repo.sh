#!/bin/sh
# Checks a Beacon apt repository the way a client sees it: runs a real apt-get update against it
# with only the given keyring trusted, then downloads one package per architecture and checks the
# version it carries.
#
#   packaging/linux/apt/verify-apt-repo.sh URL KEYRING VERSION [PACKAGE]
#
# VERSION "-" checks only the signature and the index (apt-get update) and downloads nothing.
# APT_VERIFY_ARCHES limits the downloads to some of the architectures (default amd64 arm64), for a
# staged tree that holds only the packages a run adds.
#
# URL is the repository root (https://..., or file:/path for a staged copy). KEYRING is a binary
# keyring file. Runs as an ordinary user: apt gets private state, cache and source directories and
# never reads or changes the host's own configuration.
#
# apt-get update runs with --error-on=any, so a signature problem, a checksum mismatch or a
# missing index fails here instead of becoming a warning.
set -eu

fail() {
  echo "verify-apt-repo: $*" >&2
  exit 1
}

[ $# -ge 3 ] || fail "usage: verify-apt-repo.sh URL KEYRING VERSION [PACKAGE]"
url="$1"
keyring="$2"
version="$3"
package="${4:-beacon}"
ARCHES="${APT_VERIFY_ARCHES:-amd64 arm64}"

[ -f "$keyring" ] || fail "keyring $keyring does not exist"
keyring="$(CDPATH='' cd -- "$(dirname -- "$keyring")" && pwd)/$(basename -- "$keyring")"

dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT INT TERM
mkdir -p "$dir/lists/partial" "$dir/cache/archives/partial" "$dir/sources.list.d" \
  "$dir/apt.conf.d" "$dir/preferences.d" "$dir/trusted.gpg.d" "$dir/download"
: > "$dir/status"
# The only source and the only trusted key.
echo "deb [signed-by=$keyring arch=amd64,arm64] $url stable main" > "$dir/sources.list"

# A private configuration in place of /etc/apt/apt.conf. Dir::Etc::Parts names an empty
# directory, so none of the host's apt.conf.d snippets (hooks, proxies, Docker's clean hook) apply.
cat > "$dir/apt.conf" <<EOF
Dir::State::Lists "$dir/lists";
Dir::State::status "$dir/status";
Dir::Cache "$dir/cache";
Dir::Etc::SourceList "$dir/sources.list";
Dir::Etc::SourceParts "$dir/sources.list.d";
Dir::Etc::Parts "$dir/apt.conf.d";
Dir::Etc::PreferencesParts "$dir/preferences.d";
Dir::Etc::Trusted "/dev/null";
Dir::Etc::TrustedParts "$dir/trusted.gpg.d";
Debug::NoLocking "1";
APT::Sandbox::User "$(id -un)";
APT::Architecture "amd64";
APT::Architectures { "amd64"; "arm64"; };
Acquire::Retries "3";
EOF
APT_CONFIG="$dir/apt.conf"
export APT_CONFIG

apt-get --error-on=any update || fail "apt-get update against $url failed"

if [ "$version" = "-" ]; then
  echo "verify-apt-repo: $url has a valid index signed by the given key"
  exit 0
fi

for arch in $ARCHES; do
  (cd "$dir/download" && apt-get download "$package:$arch=$version") \
    || fail "could not download $package:$arch=$version from $url"
done

for arch in $ARCHES; do
  deb="$(find "$dir/download" -name "${package}_*_${arch}.deb" | head -n 1)"
  [ -n "$deb" ] || fail "apt downloaded no $arch package"
  got_version="$(dpkg-deb -f "$deb" Version)"
  got_arch="$(dpkg-deb -f "$deb" Architecture)"
  [ "$got_version" = "$version" ] || fail "$arch package reports version $got_version, expected $version"
  [ "$got_arch" = "$arch" ] || fail "$arch package reports architecture $got_arch"
done

echo "verify-apt-repo: $url serves $package $version for $ARCHES, signed by the given key"
