#!/bin/sh
# Builds the next state of Beacon's signed apt repository from the .deb files of one release.
#
#   packaging/linux/apt/build-apt-repo.sh --out DIR --keyring FILE --signing-key FPR \
#     [--existing DIR [--existing-keyring FILE]] DEB...
#
# The published repository lives in object storage and keeps every version's .deb under pool/, so
# this script never downloads the pool. It takes the published index instead (--existing: a copy
# of dists/stable holding InRelease and main/binary-<arch>/Packages), checks that index against
# the committed public key (--keyring, a binary keyring), adds the new packages to it, and signs
# the result with --signing-key. Without --existing it starts an empty repository, which is right
# only for the very first publish.
#
# --existing-keyring names the keys the published index may be signed by, when that is more than
# --keyring: during a key rotation the bucket's InRelease is still signed by the retired key, which
# users no longer need to trust. It defaults to --keyring. The new index is always checked against
# --keyring alone.
#
# Checking the published index first matters because the result is re-signed: a Packages file
# altered in the bucket would otherwise come back out carrying a valid signature.
#
# DIR receives only what changed and must be uploaded in this order, so a client never reads an
# index that names a file not yet there:
#   1. pool/                         the new .deb files (immutable; never overwritten)
#   2. dists/stable/main/            Packages, Packages.gz and their by-hash/ copies
#   3. dists/stable/Release.gpg, Release, then InRelease last
# The Release file sets Acquire-By-Hash, so a client that read the old InRelease mid-upload still
# fetches the index files it names through by-hash/ paths that are never replaced.
#
# A version already in the index is accepted again only with identical bytes, so rerunning a
# release is a no-op and a rebuilt package can never replace one that clients have cached.
#
# Needs apt-ftparchive (apt-utils), dpkg-deb, gpg and gpgv. Set APT_GPG_PASSPHRASE_FILE when the
# signing key has a passphrase.
set -eu

SUITE=stable
COMPONENT=main
ARCHES="amd64 arm64"

fail() {
  echo "build-apt-repo: $*" >&2
  exit 1
}

out=""
keyring=""
signing_key=""
existing=""
existing_keyring=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift 2 ;;
    --keyring) keyring="$2"; shift 2 ;;
    --signing-key) signing_key="$2"; shift 2 ;;
    --existing) existing="$2"; shift 2 ;;
    --existing-keyring) existing_keyring="$2"; shift 2 ;;
    --) shift; break ;;
    -*) fail "unknown option $1" ;;
    *) break ;;
  esac
done
[ -n "$out" ] || fail "--out is required"
[ -n "$keyring" ] || fail "--keyring is required"
[ -n "$signing_key" ] || fail "--signing-key is required"
[ $# -gt 0 ] || fail "no .deb files given"
[ -f "$keyring" ] || fail "keyring $keyring does not exist"
for tool in apt-ftparchive dpkg-deb gpg gpgv sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed"
done
if [ -e "$out" ] && [ -n "$(ls -A "$out" 2>/dev/null)" ]; then
  fail "$out is not empty"
fi
mkdir -p "$out"
out="$(CDPATH='' cd -- "$out" && pwd)"
keyring="$(CDPATH='' cd -- "$(dirname -- "$keyring")" && pwd)/$(basename -- "$keyring")"
if [ -n "$existing_keyring" ]; then
  [ -f "$existing_keyring" ] || fail "keyring $existing_keyring does not exist"
  existing_keyring="$(CDPATH='' cd -- "$(dirname -- "$existing_keyring")" && pwd)/$(basename -- "$existing_keyring")"
else
  existing_keyring="$keyring"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
dists="$out/dists/$SUITE"

# Prints one "package|version|arch sha256" line per stanza of a Packages file.
stanza_keys() {
  awk 'BEGIN { RS = "" }
    {
      p = v = a = s = ""
      n = split($0, line, "\n")
      for (i = 1; i <= n; i++) {
        if (line[i] ~ /^Package: /) p = substr(line[i], 10)
        else if (line[i] ~ /^Version: /) v = substr(line[i], 10)
        else if (line[i] ~ /^Architecture: /) a = substr(line[i], 15)
        else if (line[i] ~ /^SHA256: /) s = substr(line[i], 9)
      }
      print p "|" v "|" a " " s
    }' "$1"
}

# --- Check the published index -------------------------------------------------------------
for arch in $ARCHES; do
  : > "$work/old-$arch"
done
if [ -n "$existing" ]; then
  [ -f "$existing/InRelease" ] || fail "$existing/InRelease does not exist"
  gpgv --keyring "$existing_keyring" --output "$work/old-Release" "$existing/InRelease" 2>"$work/gpgv.log" || {
    cat "$work/gpgv.log" >&2
    fail "the published InRelease is not signed by the committed key or a retired one"
  }
  for arch in $ARCHES; do
    rel="$COMPONENT/binary-$arch/Packages"
    # The SHA256 block lists " <hash> <size> <path>"; only that block is read.
    want="$(awk -v rel="$rel" '
      /^[^ ]/ { in_sha = ($0 == "SHA256:") ; next }
      in_sha && $3 == rel { print $1 }' "$work/old-Release")"
    if [ -z "$want" ]; then
      # An architecture added after the first publish has no index yet.
      continue
    fi
    [ -f "$existing/$rel" ] || fail "the published InRelease lists $rel but it was not provided"
    got="$(sha256sum "$existing/$rel" | awk '{ print $1 }')"
    [ "$got" = "$want" ] || fail "the published $rel does not match its InRelease checksum"
    cp "$existing/$rel" "$work/old-$arch"
  done
fi

# --- Stage the new packages ----------------------------------------------------------------
stage="$work/stage"
mkdir -p "$stage"
for deb in "$@"; do
  [ -f "$deb" ] || fail "$deb does not exist"
  pkg="$(dpkg-deb -f "$deb" Package)"
  ver="$(dpkg-deb -f "$deb" Version)"
  arch="$(dpkg-deb -f "$deb" Architecture)"
  case " $ARCHES " in
    *" $arch "*) ;;
    *) fail "$deb is for $arch, which this repository does not carry" ;;
  esac
  first="$(printf '%s' "$pkg" | cut -c1)"
  dest="pool/$COMPONENT/$first/$pkg/${pkg}_${ver}_${arch}.deb"
  [ -e "$stage/$dest" ] && fail "two packages map to $dest"
  mkdir -p "$stage/$(dirname -- "$dest")"
  cp "$deb" "$stage/$dest"
done

for arch in $ARCHES; do
  (cd "$stage" && apt-ftparchive --arch "$arch" packages pool) > "$work/new-$arch"
  # apt-ftparchive includes Architecture: all packages in every arch; there are none here, but
  # a stanza for another arch would mean the staging above is wrong.
  stanza_keys "$work/new-$arch" | while read -r key _; do
    case "$key" in
      *"|$arch") ;;
      *) fail "unexpected stanza $key in the $arch index" ;;
    esac
  done
done

# --- Merge ---------------------------------------------------------------------------------
# A stanza already published under the same package, version and arch is kept as published when
# the bytes are identical and refused when they differ. Everything else is appended.
mkdir -p "$out/pool"
for arch in $ARCHES; do
  stanza_keys "$work/old-$arch" > "$work/old-keys-$arch"
  stanza_keys "$work/new-$arch" > "$work/new-keys-$arch"
  : > "$work/skip-$arch"
  while read -r key sha; do
    published="$(awk -v key="$key" '$1 == key { print $2 }' "$work/old-keys-$arch")"
    if [ -z "$published" ]; then
      continue
    fi
    [ "$published" = "$sha" ] || fail "$key is already published with different contents (published $published, new $sha); a released version cannot be replaced"
    echo "$key" >> "$work/skip-$arch"
  done < "$work/new-keys-$arch"

  index_dir="$dists/$COMPONENT/binary-$arch"
  mkdir -p "$index_dir"
  {
    if [ -s "$work/old-$arch" ]; then
      awk 'BEGIN { RS = ""; ORS = "\n\n" } { print }' "$work/old-$arch"
    fi
    awk -v skipfile="$work/skip-$arch" '
      BEGIN {
        while ((getline k < skipfile) > 0) skip[k] = 1
        RS = ""; ORS = "\n\n"
      }
      {
        p = v = a = ""
        n = split($0, line, "\n")
        for (i = 1; i <= n; i++) {
          if (line[i] ~ /^Package: /) p = substr(line[i], 10)
          else if (line[i] ~ /^Version: /) v = substr(line[i], 10)
          else if (line[i] ~ /^Architecture: /) a = substr(line[i], 15)
        }
        if (!((p "|" v "|" a) in skip)) print
      }' "$work/new-$arch"
  } > "$index_dir/Packages"
  gzip -9n < "$index_dir/Packages" > "$index_dir/Packages.gz"

  # Only packages not already published are uploaded.
  while read -r key _; do
    grep -qxF "$key" "$work/skip-$arch" && continue
    pkg="${key%%|*}"
    rest="${key#*|}"
    ver="${rest%%|*}"
    first="$(printf '%s' "$pkg" | cut -c1)"
    rel="pool/$COMPONENT/$first/$pkg/${pkg}_${ver}_${arch}.deb"
    mkdir -p "$out/$(dirname -- "$rel")"
    cp "$stage/$rel" "$out/$rel"
  done < "$work/new-keys-$arch"
done

# --- Release, signed ----------------------------------------------------------------------
# Generated before the by-hash copies exist, so it lists only the index files.
(cd "$dists" && apt-ftparchive \
  -o APT::FTPArchive::Release::Origin=Beacon \
  -o APT::FTPArchive::Release::Label=Beacon \
  -o APT::FTPArchive::Release::Suite="$SUITE" \
  -o APT::FTPArchive::Release::Codename="$SUITE" \
  -o APT::FTPArchive::Release::Architectures="$ARCHES" \
  -o APT::FTPArchive::Release::Components="$COMPONENT" \
  -o APT::FTPArchive::Release::Description="Beacon Endpoint Agent" \
  -o APT::FTPArchive::Release::Acquire-By-Hash=yes \
  release .) > "$work/Release"
mv "$work/Release" "$dists/Release"

for arch in $ARCHES; do
  index_dir="$dists/$COMPONENT/binary-$arch"
  mkdir -p "$index_dir/by-hash/SHA256"
  for f in Packages Packages.gz; do
    sum="$(sha256sum "$index_dir/$f" | awk '{ print $1 }')"
    cp "$index_dir/$f" "$index_dir/by-hash/SHA256/$sum"
  done
done

set -- --batch --yes --local-user "$signing_key"
if [ -n "${APT_GPG_PASSPHRASE_FILE:-}" ]; then
  set -- "$@" --pinentry-mode loopback --passphrase-file "$APT_GPG_PASSPHRASE_FILE"
fi
gpg "$@" --digest-algo SHA512 --clearsign --output "$dists/InRelease" "$dists/Release"
gpg "$@" --digest-algo SHA512 --armor --detach-sign --output "$dists/Release.gpg" "$dists/Release"

# The signing key in the secret must be the key users were told to trust.
gpgv --keyring "$keyring" "$dists/InRelease" 2>"$work/gpgv.log" || {
  cat "$work/gpgv.log" >&2
  fail "the new InRelease does not verify against $keyring; the signing key is not the committed key"
}
gpgv --keyring "$keyring" "$dists/Release.gpg" "$dists/Release" 2>"$work/gpgv.log" || {
  cat "$work/gpgv.log" >&2
  fail "the new Release.gpg does not verify against $keyring"
}

echo "build-apt-repo: staged $(find "$out/pool" -name '*.deb' | wc -l | tr -d ' ') new package(s) in $out"
