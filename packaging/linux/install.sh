#!/usr/bin/env bash
# Install the Beacon endpoint package on Linux.
#
#   curl -fsSL https://github.com/asymptote-labs/agent-beacon/releases/latest/download/install.sh | bash
#
# Downloads the .deb or .rpm for this machine's architecture, checks it against
# the release checksums.txt (and its signature, once a release key is pinned
# below), and installs it with apt-get or dnf/yum so package
# dependencies resolve. Set BEACON_VERSION (for example 1.3.24 or v1.3.24) to
# install a specific release instead of the latest.
#
# The package is downloaded into a fresh directory under /tmp rather than the
# working directory. APT reads a local package as its unprivileged _apt user; a
# package in a home directory _apt cannot traverse still installs, but APT ends
# with "pkgAcquire::Run (13: Permission denied)", which reads like a failure.
set -euo pipefail

repo="asymptote-labs/agent-beacon"

# Comma-separated OpenPGP fingerprints of the Beacon release keys. The release copy of
# this script is stamped from cli/beacon/internal/endpoint/selfupdate/release-key.fingerprint.
# While it is empty, packages are checked against checksums.txt alone; once set, only
# against a checksums.txt signed by one of those keys.
release_key_fingerprints=

fail() {
  echo "beacon install: $*" >&2
  exit 1
}

[[ "$(uname -s)" == Linux ]] || fail "this installer supports Linux only. See https://docs.asymptotelabs.ai for other platforms."

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "Beacon publishes Linux packages for amd64 and arm64, not $(uname -m)." ;;
esac

if command -v apt-get >/dev/null 2>&1 && command -v dpkg >/dev/null 2>&1; then
  format=deb
  installer=(apt-get install -y)
elif command -v dnf >/dev/null 2>&1; then
  format=rpm
  installer=(dnf install -y)
elif command -v yum >/dev/null 2>&1; then
  format=rpm
  installer=(yum install -y)
else
  fail "no apt-get, dnf, or yum found. Use the tarball install instead: https://docs.asymptotelabs.ai/platforms/linux"
fi

for program in curl sha256sum awk mktemp; do
  command -v "$program" >/dev/null 2>&1 || fail "required command not found: $program"
done

if ((EUID == 0)); then
  sudo=()
else
  command -v sudo >/dev/null 2>&1 || fail "installing the package needs root; rerun as root or install sudo."
  sudo=(sudo)
fi

tag="${BEACON_VERSION:-}"
if [[ -z "$tag" ]]; then
  latest_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${repo}/releases/latest")"
  tag="${latest_url##*/}"
fi
[[ "$tag" == v* ]] || tag="v${tag}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  fail "could not determine which Beacon release to install (got '${tag}')."
fi
version="${tag#v}"
package="beacon_${version}_linux_${arch}.${format}"
base="https://github.com/${repo}/releases/download/${tag}"

tmp="$(mktemp -d /tmp/beacon-install.XXXXXXXX)"
trap 'rm -rf -- "$tmp"' EXIT

echo "Installing Beacon ${tag} (${arch}, .${format})..."
curl -fsSL "${base}/${package}" -o "${tmp}/${package}"
curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt"

if [[ -n "$release_key_fingerprints" ]]; then
  command -v gpgv >/dev/null 2>&1 || fail "gpgv is needed to verify the release signature; install gpgv or gnupg and rerun."
  curl -fsSL "${base}/checksums.txt.sig" -o "${tmp}/checksums.txt.sig" || fail "${tag} has no checksums.txt.sig."
  curl -fsSL "${base}/beacon-release-key.gpg" -o "${tmp}/beacon-release-key.gpg" || fail "${tag} has no beacon-release-key.gpg."
  mkdir -m 0700 "${tmp}/gnupg"
  status="$(gpgv --homedir "${tmp}/gnupg" --keyring "${tmp}/beacon-release-key.gpg" --status-fd 1 \
    "${tmp}/checksums.txt.sig" "${tmp}/checksums.txt" 2>/dev/null)" || fail "checksums.txt signature does not verify."
  # The key file comes from the release too, so only the pinned fingerprint makes it Beacon's.
  awk -v fprs="$release_key_fingerprints" 'BEGIN { n = split(toupper(fprs), pinned, ",") }
    $1 == "[GNUPG:]" && $2 == "VALIDSIG" { for (i = 1; i <= n; i++) if (toupper($NF) == pinned[i]) ok = 1 }
    END { exit !ok }' <<<"$status" ||
    fail "checksums.txt is not signed by a Beacon release key (${release_key_fingerprints})."
fi

checksum="$(awk -v name="$package" '$2 == name { print $1 }' "${tmp}/checksums.txt")"
[[ "$checksum" =~ ^[0-9a-fA-F]{64}$ ]] || fail "no unique SHA-256 checksum for ${package} in checksums.txt."
(cd "$tmp" && printf '%s  %s\n' "$checksum" "$package" | sha256sum --check --quiet -) || fail "checksum mismatch for ${package}."

# Let APT's _apt user reach the package so it does not fall back to an
# unsandboxed download and print a permission warning.
chmod 0711 "$tmp"
chmod 0644 "${tmp}/${package}"

${sudo[@]+"${sudo[@]}"} "${installer[@]}" "${tmp}/${package}"

echo
beacon version
echo "Beacon installed. Check the endpoint with:"
echo "  beacon endpoint status --system"
