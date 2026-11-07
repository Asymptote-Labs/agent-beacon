#!/bin/sh
# Exercises build-apt-repo.sh and verify-apt-repo.sh against stand-in packages, a throwaway
# signing key and a local directory standing in for the bucket. Runs in CI on ubuntu; needs
# apt-utils, dpkg-dev and gnupg, and no network.
#
#   packaging/linux/apt/test-apt-repo.sh
#
# It publishes two releases in sequence and checks that both stay installable, that a rerun is a
# no-op, and that a replaced package, a tampered index and the wrong signing key are all refused.
# It also resumes a publish that stopped before InRelease and rotates the signing key.
set -eu

HERE="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
BUILD="$HERE/build-apt-repo.sh"
VERIFY="$HERE/verify-apt-repo.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
export GNUPGHOME="$work/gnupg"
mkdir -m 0700 "$GNUPGHOME"

pass() { echo "ok   - $*"; }
fail() { echo "FAIL - $*" >&2; exit 1; }

new_key() {
  gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
    --quick-generate-key "$1" ed25519 sign never
  gpg --batch --with-colons --list-keys "$1" | awk -F: '$1 == "fpr" { print $10; exit }'
}

# make_deb VERSION ARCH [EXTRA] -- EXTRA changes the bytes without changing the version.
make_deb() {
  root="$work/debroot-$1-$2"
  rm -rf "$root"
  mkdir -p "$root/DEBIAN" "$root/opt/beacon"
  cat > "$root/DEBIAN/control" <<EOF
Package: beacon
Version: $1
Architecture: $2
Maintainer: Beacon Test <test@example.invalid>
Description: stand-in package for the apt repository test
EOF
  printf '%s %s %s\n' "$1" "$2" "${3:-}" > "$root/opt/beacon/marker"
  mkdir -p "$work/debs"
  dpkg-deb --root-owner-group --build "$root" "$work/debs/beacon_$1_linux_$2.deb" >/dev/null
  echo "$work/debs/beacon_$1_linux_$2.deb"
}

# publish OUT -- copies a staged tree into the bucket in the documented order.
publish() {
  mkdir -p "$work/bucket"
  if [ -d "$1/pool" ]; then
    cp -R "$1/pool" "$work/bucket/"
  fi
  mkdir -p "$work/bucket/dists/stable"
  cp -R "$1/dists/stable/main" "$work/bucket/dists/stable/"
  for f in Release.gpg Release InRelease; do
    cp "$1/dists/stable/$f" "$work/bucket/dists/stable/$f"
  done
}

# existing -- the copy of the published index the release workflow downloads: InRelease, and
# each Packages file through the by-hash path InRelease names, never the plain path.
existing() {
  rm -rf "$work/existing"
  mkdir -p "$work/existing/main"
  cp "$work/bucket/dists/stable/InRelease" "$work/existing/"
  for arch in amd64 arm64; do
    rel="main/binary-$arch/Packages"
    want="$(awk -v rel="$rel" '/^[^ ]/ { in_sha = ($0 == "SHA256:"); next } in_sha && $3 == rel { print $1 }' "$work/existing/InRelease")"
    mkdir -p "$work/existing/main/binary-$arch"
    cp "$work/bucket/dists/stable/main/binary-$arch/by-hash/SHA256/$want" "$work/existing/$rel"
  done
}

fpr="$(new_key 'Beacon Test Archive <archive@example.invalid>')"
gpg --batch --export "$fpr" > "$work/keyring.gpg"
other_fpr="$(new_key 'Someone Else <other@example.invalid>')"
gpg --batch --export "$other_fpr" > "$work/other-keyring.gpg"

# 1. First publish.
deb_a1="$(make_deb 1.0.0 amd64)"
deb_b1="$(make_deb 1.0.0 arm64)"
sh "$BUILD" --out "$work/out1" --keyring "$work/keyring.gpg" --signing-key "$fpr" "$deb_a1" "$deb_b1"
sh "$VERIFY" "file:$work/out1" "$work/keyring.gpg" 1.0.0
publish "$work/out1"
sh "$VERIFY" "file:$work/bucket" "$work/keyring.gpg" 1.0.0
pass "first publish serves 1.0.0"

# 2. Second release, merged onto the published index.
existing
deb_a2="$(make_deb 1.0.1 amd64)"
deb_b2="$(make_deb 1.0.1 arm64)"
sh "$BUILD" --out "$work/out2" --keyring "$work/keyring.gpg" --signing-key "$fpr" \
  --existing "$work/existing" "$deb_a2" "$deb_b2"
[ "$(find "$work/out2/pool" -name '*.deb' | wc -l | tr -d ' ')" = 2 ] || fail "second publish should stage only the two new packages"
publish "$work/out2"
sh "$VERIFY" "file:$work/bucket" "$work/keyring.gpg" 1.0.1
sh "$VERIFY" "file:$work/bucket" "$work/keyring.gpg" 1.0.0
pass "second publish serves 1.0.1 and keeps 1.0.0"

grep -q '^Acquire-By-Hash: yes$' "$work/bucket/dists/stable/Release" || fail "Release does not set Acquire-By-Hash"
sum="$(sha256sum "$work/bucket/dists/stable/main/binary-amd64/Packages" | awk '{ print $1 }')"
[ -f "$work/bucket/dists/stable/main/binary-amd64/by-hash/SHA256/$sum" ] || fail "no by-hash copy of the amd64 index"
pass "Release sets Acquire-By-Hash and the by-hash copies exist"

# 3. Rerunning the same release changes nothing.
existing
sh "$BUILD" --out "$work/out3" --keyring "$work/keyring.gpg" --signing-key "$fpr" \
  --existing "$work/existing" "$deb_a2" "$deb_b2"
[ -z "$(find "$work/out3/pool" -name '*.deb')" ] || fail "a rerun staged packages again"
cmp -s "$work/out3/dists/stable/main/binary-amd64/Packages" "$work/bucket/dists/stable/main/binary-amd64/Packages" \
  || fail "a rerun changed the amd64 index"
[ "$(grep -c '^Package: ' "$work/out3/dists/stable/main/binary-amd64/Packages")" = 2 ] || fail "the amd64 index should list two versions"
pass "a rerun is a no-op"

# 4. A rebuilt package cannot replace a published version.
deb_a2_changed="$(make_deb 1.0.1 amd64 rebuilt)"
if sh "$BUILD" --out "$work/out4" --keyring "$work/keyring.gpg" --signing-key "$fpr" \
  --existing "$work/existing" "$deb_a2_changed" 2>"$work/err4"; then
  fail "a changed 1.0.1 was accepted"
fi
grep -q 'already published with different contents' "$work/err4" || { cat "$work/err4" >&2; fail "wrong error for a changed package"; }
pass "a replaced package is refused"

# 5. A tampered index in the bucket is refused rather than re-signed.
existing
printf '\nPackage: evil\nVersion: 9\nArchitecture: amd64\nFilename: pool/evil.deb\n' >> "$work/existing/main/binary-amd64/Packages"
deb_a3="$(make_deb 1.0.2 amd64)"
deb_b3="$(make_deb 1.0.2 arm64)"
if sh "$BUILD" --out "$work/out5" --keyring "$work/keyring.gpg" --signing-key "$fpr" \
  --existing "$work/existing" "$deb_a3" "$deb_b3" 2>"$work/err5"; then
  fail "a tampered index was accepted"
fi
grep -q 'does not match its InRelease checksum' "$work/err5" || { cat "$work/err5" >&2; fail "wrong error for a tampered index"; }
pass "a tampered index is refused"

# 6. A published index signed by another key is refused.
existing
if sh "$BUILD" --out "$work/out6" --keyring "$work/other-keyring.gpg" --signing-key "$other_fpr" \
  --existing "$work/existing" "$deb_a3" "$deb_b3" 2>"$work/err6"; then
  fail "an index signed by another key was accepted"
fi
grep -q 'not signed by any committed key' "$work/err6" || { cat "$work/err6" >&2; fail "wrong error for another key's index"; }
pass "an index signed by another key is refused"

# 7. A signing key that is not the committed key is refused before anything is published.
if sh "$BUILD" --out "$work/out7" --keyring "$work/keyring.gpg" --signing-key "$other_fpr" \
  --existing "$work/existing" "$deb_a3" "$deb_b3" 2>"$work/err7"; then
  fail "signing with another key was accepted"
fi
grep -q 'signing key is not a committed key' "$work/err7" || { cat "$work/err7" >&2; fail "wrong error for the wrong signing key"; }
pass "the wrong signing key is refused"

# 8. A client that trusts another key refuses the repository.
if sh "$VERIFY" "file:$work/bucket" "$work/other-keyring.gpg" 1.0.1 >/dev/null 2>&1; then
  fail "a client trusting another key accepted the repository"
fi
pass "a client trusting another key refuses the repository"

# 9. A publish that stopped after the index files and before InRelease is resumed: the plain
#    Packages is already ahead of InRelease, but the by-hash copies InRelease names are not.
existing
sh "$BUILD" --out "$work/out9" --keyring "$work/keyring.gpg" --signing-key "$fpr" \
  --existing "$work/existing" "$deb_a3" "$deb_b3"
cp -R "$work/out9/pool" "$work/bucket/"
cp -R "$work/out9/dists/stable/main" "$work/bucket/dists/stable/"
existing
sh "$BUILD" --out "$work/out9b" --keyring "$work/keyring.gpg" --signing-key "$fpr" \
  --existing "$work/existing" "$deb_a3" "$deb_b3"
publish "$work/out9b"
for v in 1.0.0 1.0.1 1.0.2; do
  sh "$VERIFY" "file:$work/bucket" "$work/keyring.gpg" "$v" >/dev/null
done
pass "a publish that stopped before InRelease is resumed"

# 10. Key rotation, step one: the new public key is committed beside the old one and both keys
#     sign. The index the old key alone signed is accepted, and a client that still trusts only
#     the old key, one that trusts only the new key, and one that trusts both all accept the
#     result. Several releases merge in one run (a missed release added by the next one).
gpg --batch --export "$fpr" "$other_fpr" > "$work/both-keyring.gpg"
existing
deb_a4="$(make_deb 1.0.3 amd64)"
deb_b4="$(make_deb 1.0.3 arm64)"
deb_a5="$(make_deb 1.0.4 amd64)"
deb_b5="$(make_deb 1.0.4 arm64)"
sh "$BUILD" --out "$work/out10" --keyring "$work/both-keyring.gpg" \
  --signing-key "$fpr" --signing-key "$other_fpr" \
  --existing "$work/existing" "$deb_a4" "$deb_b4" "$deb_a5" "$deb_b5"
publish "$work/out10"
for v in 1.0.0 1.0.3 1.0.4; do
  sh "$VERIFY" "file:$work/bucket" "$work/both-keyring.gpg" "$v" >/dev/null
done
sh "$VERIFY" "file:$work/bucket" "$work/keyring.gpg" 1.0.4 >/dev/null \
  || fail "a client that trusts only the old key refused the index during the rotation"
sh "$VERIFY" "file:$work/bucket" "$work/other-keyring.gpg" 1.0.4 >/dev/null \
  || fail "a client that trusts only the new key refused the index during the rotation"
pass "during a rotation both keys sign, so clients trusting either key keep updating"

# 11. Step two: the old key is removed from the keyring and the secret. The index both keys
#     signed is still read (the old key's signature is ignored, the new one's is good).
existing
deb_a6="$(make_deb 1.0.5 amd64)"
deb_b6="$(make_deb 1.0.5 arm64)"
sh "$BUILD" --out "$work/out11" --keyring "$work/other-keyring.gpg" --signing-key "$other_fpr" \
  --existing "$work/existing" "$deb_a6" "$deb_b6"
publish "$work/out11"
sh "$VERIFY" "file:$work/bucket" "$work/other-keyring.gpg" 1.0.5 >/dev/null
if sh "$VERIFY" "file:$work/bucket" "$work/keyring.gpg" 1.0.5 >/dev/null 2>&1; then
  fail "a client trusting only the removed key still accepted the index"
fi
pass "after the rotation the old key is removed and its signature is no longer made"

# 12. A staged tree holds only what a run adds, so the pre-publish check verifies the index ("-")
#     and each staged package's own version and architecture, not every architecture.
existing
deb_b7="$(make_deb 1.0.6 arm64)"
sh "$BUILD" --out "$work/out12" --keyring "$work/both-keyring.gpg" --signing-key "$other_fpr" \
  --existing "$work/existing" "$deb_b7"
sh "$VERIFY" "file:$work/out12" "$work/both-keyring.gpg" - >/dev/null
APT_VERIFY_ARCHES=arm64 sh "$VERIFY" "file:$work/out12" "$work/both-keyring.gpg" 1.0.6 >/dev/null
if sh "$VERIFY" "file:$work/out12" "$work/both-keyring.gpg" 1.0.6 >/dev/null 2>&1; then
  fail "a staged tree without the amd64 package passed a check for both architectures"
fi
pass "the staged tree is checked for the packages it holds"

echo "all apt repository checks passed"
